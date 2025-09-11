package main

import (
	"errors"
	"flag"
	"fmt"
	"github.com/kzub/trickyproxy/endpoint"
	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
	"io/ioutil"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"strings"
	"time"
)

type resultStatus int

const (
	version   string       = "2.3.0"
	servOk    resultStatus = iota
	servFail  resultStatus = iota
	servRetry resultStatus = iota
)

func main() {
	keyfile := flag.String("key", "certs/service.key", "service private key")
	crtfile := flag.String("cert", "certs/service.pem", "service public cert")
	dnrfile := flag.String("donors", "donors.conf", "donors hosts list")
	trgfile := flag.String("target", "target.conf", "target host address")
	srvfile := flag.String("srvaddr", "srvaddr.conf", "server host & port to listen")
	excfile := flag.String("noproxy", "noproxy.conf", "request path exceptions list")
	stopfile := flag.String("stoplist", "stoplist.conf", "requests stop list")
	proxmod := flag.String("mode", "riak", "proxy mode: [http | riak]")
	logformat := flag.String("logformat", "json", "change logformat to console")
	flag.Parse()

	if len(os.Args) > 1 && os.Args[1] == "version" {
		fmt.Println("version " + version)
		return
	}

	encoderCfg := zapcore.EncoderConfig{
		TimeKey:        "@timestamp",
		LevelKey:       "level",
		NameKey:        "logger",
		CallerKey:      "caller",
		MessageKey:     "message",
		StacktraceKey:  "stacktrace",
		LineEnding:     zapcore.DefaultLineEnding,
		EncodeLevel:    zapcore.LowercaseLevelEncoder,
		EncodeTime:     zapcore.ISO8601TimeEncoder,
		EncodeDuration: zapcore.SecondsDurationEncoder,
		EncodeCaller:   zapcore.ShortCallerEncoder,
	}

	logConfig := zap.Config{
		Level:       zap.NewAtomicLevelAt(zap.InfoLevel),
		Development: false,
		Sampling: &zap.SamplingConfig{
			Initial:    100,
			Thereafter: 100,
		},
		Encoding:         *logformat,
		EncoderConfig:    encoderCfg,
		OutputPaths:      []string{"stdout"},
		ErrorOutputPaths: []string{"stdout"},
	}

	// logger, _ := zap.NewProduction()
	logger, _ := logConfig.Build()
	defer logger.Sync()

	undo := zap.ReplaceGlobals(logger)
	defer undo()

	if *proxmod == "riak" {
		setRiakProxyMode()
	}

	donorsConfig := readConfig(*dnrfile, true)
	targetConfig := readConfig(*trgfile, true)
	serverConfig := readConfig(*srvfile, true)
	exceptionsPaths := readConfig(*excfile, false)
	stopListPaths := readConfig(*stopfile, false)

	donors := setupDonors(donorsConfig, *keyfile, *crtfile)
	target := setupTarget(targetConfig)
	setupServer(donors, target, exceptionsPaths, stopListPaths, serverConfig)
}

func readConfig(filename string, required bool) string {
	data, err := ioutil.ReadFile(filename)
	if err != nil {
		if required {
			zap.L().Error("cannot read config file",
				zap.String("filename", filename),
			)
			os.Exit(1)
		}
		return ""
	}

	return cleanString(string(data[:]))
}

func setupDonors(donorsConfig, keyfile, crtfile string) *endpoint.Instances {
	donorsList := strings.Split(donorsConfig, "\n")
	donors := endpoint.NewInstances()

	for _, val := range donorsList {
		if len(val) == 0 {
			continue
		}
		data := strings.Split(val, ":")

		protocol := "https"
		if data[0] == "http" || data[0] == "https" {
			protocol = data[0]
			data = data[1:]
			data[0] = strings.TrimLeft(data[0], "//")
		}

		host := data[0]
		port := cleanString(data[1])
		auth := ""
		if len(data) > 2 {
			auth = cleanString(data[2])
		}
		zap.L().Info("adding donor upstream",
			zap.String("host", host),
			zap.String("port", port),
		)

		ep := endpoint.NewTLS(protocol, host, port, auth, keyfile, crtfile)
		ep.MakeReadOnly()
		donors.Add(ep)
	}
	return donors
}

func setupTarget(targetConfig string) *endpoint.Instance {
	data := strings.Split(targetConfig, ":")
	host := data[0]
	port := cleanString(data[1])
	space := ""
	if len(data) > 2 {
		space = cleanString(data[2]) + "_"
	}
	zap.L().Info("adding target upstream",
		zap.String("host", host),
		zap.String("port", port),
		zap.String("space", space),
	)
	return endpoint.New(host, port, "http", "", urlEncoder(space), headerEncoder(space), headerDecoder(space))
}

func cleanString(str string) string {
	str = strings.TrimRight(str, " ")
	str = strings.TrimRight(str, "\n")
	str = strings.TrimRight(str, "\r")
	str = strings.TrimRight(str, " ")
	str = strings.TrimLeft(str, " ")
	return str
}

type checkFunc func(rURL *url.URL) bool

func buildRegexpFromPath(name, pathListRawData string) checkFunc {
	var exceptions []*regexp.Regexp
	if len(pathListRawData) == 0 {
		zap.L().Info("no paths for",
			zap.String("name", name),
		)
		return func(rURL *url.URL) bool {
			return false
		}
	}

	data := strings.Split(pathListRawData, "\n")

	for _, v := range data {
		if len(v) == 0 {
			continue
		}
		expr, err := regexp.Compile(v)
		if err != nil {
			zap.L().Error("bad regexp",
				zap.String("name", name),
				zap.String("config", v),
			)
			os.Exit(1)
		}
		zap.L().Info("adding path",
			zap.String("name", name),
			zap.String("path", v),
		)
		exceptions = append(exceptions, expr)
	}

	return func(rURL *url.URL) bool {
		path := rURL.String()
		for _, v := range exceptions {
			if v.MatchString(path) {
				zap.L().Info("skip donor request in case of",
					zap.String("name", name),
					zap.String("path", v.String()),
				)
				return true
			}
		}
		return false
	}
}

func makeHandler(donors *endpoint.Instances, target *endpoint.Instance, exceptionsPaths, stopListPaths string) func(w http.ResponseWriter, r *http.Request) {
	exceptions := buildRegexpFromPath("exceptions", exceptionsPaths)
	stopList := buildRegexpFromPath("stoplist", stopListPaths)

	return func(w http.ResponseWriter, r *http.Request) {
		startTime := time.Now()
		if stopList(r.URL) {
			writeErrorResponse("URL_IN_STOP_LIST "+r.Method, r, w, errors.New("FORBIDDEN REQUEST"), time.Since(startTime))
			return
		}
		for callCount, res := 3, servRetry; res == servRetry && callCount >= 0; callCount-- {
			res = serveRequest(donors.Next(), target, w, r, exceptions, callCount, startTime)
		}
	}
}

func setupServer(donors *endpoint.Instances, target *endpoint.Instance, exceptionsPaths, stopListPaths, serverAddr string) {
	http.HandleFunc("/", makeHandler(donors, target, exceptionsPaths, stopListPaths))
	zap.L().Info("server ready",
		zap.String("address", serverAddr),
	)
	err := http.ListenAndServe(serverAddr, nil)
	if err != nil {
		zap.L().Error("cannot setup server",
			zap.String("address", serverAddr),
		)
		os.Exit(1)
	}
}

func serveRequest(donor *endpoint.Instance, target *endpoint.Instance, w http.ResponseWriter, r *http.Request, noProxyPass checkFunc, callCount int, startTime time.Time) resultStatus {
	resp, body, err := target.Do(r)
	if err != nil {
		writeErrorResponse("TARGET_DO_METHOD "+r.Method, r, w, err, time.Since(startTime))
		return servFail
	}

	if !isNeedProxyPass(resp, r, body) || noProxyPass(r.URL) {
		writeResponse(w, resp, body, time.Since(startTime))
		return servOk
	}

	zap.L().Info("fetch donor",
		zap.String("host", r.URL.Host),
	)
	resp, body, err = donor.Do(r)

	if err != nil {
		if callCount > 0 {
			return servRetry
		}
		writeErrorResponse("DONOR_DO "+r.Method, r, w, err, time.Since(startTime))
		return servFail
	}

	storeResult, err := postProcess(donor, target, resp, r, body)
	if err != nil {
		writeErrorResponse("POST_PROCESS", r, w, err, time.Since(startTime))
		return servFail
	}

	if storeResult {
		err = storeResponse(target, r.URL.String(), resp.Header, body)
		if err != nil {
			writeErrorResponse("TARGET_STORE", r, w, err, time.Since(startTime))
			return servFail
		}
	}

	writeResponse(w, resp, body, time.Since(startTime))
	return servOk
}

func writeErrorResponse(msg string, r *http.Request, w http.ResponseWriter, err error, responseTime time.Duration) {
	zap.L().Error(msg,
		zap.String("url", r.URL.String()),
		zap.String("error", err.Error()),
		zap.Int64("responseTime", responseTime.Milliseconds()),
	)
	w.WriteHeader(http.StatusInternalServerError)
	fmt.Fprintln(w, msg)
}

func writeResponse(w http.ResponseWriter, resp *http.Response, respBody []byte, responseTime time.Duration) {
	defer func() {
		if resp.StatusCode >= 500 {
			zap.L().Info("cli response",
				zap.String("status", resp.Status),
				zap.String("url", resp.Request.URL.String()),
				zap.String("body", string(respBody)),
				zap.Int64("responseTime", responseTime.Milliseconds()),
			)
		} else {
			zap.L().Info("cli response",
				zap.String("status", resp.Status),
				zap.String("url", resp.Request.URL.String()),
				zap.Int64("responseTime", responseTime.Milliseconds()),
			)
		}
	}()

	headers := w.Header()
	for k, v := range resp.Header {
		headers[k] = v
	}

	w.WriteHeader(resp.StatusCode)
	w.Write(respBody)
}
