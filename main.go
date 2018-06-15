package main

import (
	"errors"
	"flag"
	"fmt"
	"github.com/tonymadbrain/trickyproxy/endpoint"
	log "github.com/sirupsen/logrus"
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
	version   string       = "2.2.1"
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
	logtype := flag.String("logtype", "text", "log messages formatting: [text | json]")
	flag.Parse()

	if *logtype == "json" {
		log.SetFormatter(&log.JSONFormatter{
			TimestampFormat:  time.RFC3339Nano,
			DisableTimestamp: true,
			FieldMap: log.FieldMap{
				log.FieldKeyMsg:  "message",
				log.FieldKeyTime: "@timestamp",
			}})
	} else if *logtype == "text" {
		log.SetFormatter(&log.TextFormatter{})
	} else {
		log.WithField("logtype", logtype).Fatal("Given logtype is invalid!")
		os.Exit(1)
	}

	if len(os.Args) > 1 && os.Args[1] == "version" {
		log.Infof("version %s", version)
		return
	}

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
			panic("ERR: CANNOT READ CONFIG FILE:" + filename)
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
		log.Infof("adding donor upstream %s %s", host, port)

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
	log.Infof("adding target upstream %s %s %s", host, port, space)
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
		log.Infof("No paths for: %s", name)
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
			log.Errorf("BAD RegExp in %s, config:%s", name, v)
			os.Exit(1)
		}
		log.Infof("adding path %s:%s", name, v)
		exceptions = append(exceptions, expr)
	}

	return func(rURL *url.URL) bool {
		path := rURL.String()
		for _, v := range exceptions {
			if v.MatchString(path) {
				log.Infof("skip donor request in case of %s:%s", name, v)
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
		if stopList(r.URL) {
			writeErrorResponse("URL_IN_STOP_LIST "+r.Method, r, w, errors.New("FORBIDDEN REQUEST"))
			return
		}
		for callCount, res := 3, servRetry; res == servRetry && callCount >= 0; callCount-- {
			res = serveRequest(donors.Next(), target, w, r, exceptions, callCount)
		}
	}
}

func setupServer(donors *endpoint.Instances, target *endpoint.Instance, exceptionsPaths, stopListPaths, serverAddr string) {
	http.HandleFunc("/", makeHandler(donors, target, exceptionsPaths, stopListPaths))
	log.Infof("Ready on [%s]", serverAddr)
	err := http.ListenAndServe(serverAddr, nil)
	if err != nil {
		log.Errorf("ERR:%s", err)
		log.Errorf("CANNOT SETUP SERVER AT %s", serverAddr)
		os.Exit(1)
	}
}

func serveRequest(donor *endpoint.Instance, target *endpoint.Instance, w http.ResponseWriter, r *http.Request, noProxyPass checkFunc, callCount int) resultStatus {
	resp, body, err := target.Do(r)
	if err != nil {
		writeErrorResponse("TARGET_DO_METHOD "+r.Method, r, w, err)
		return servFail
	}

	if !isNeedProxyPass(resp, r, body) || noProxyPass(r.URL) {
		writeResponse(w, resp, body)
		return servOk
	}

	log.Infof("FETCH donor: %s", r.URL.Host)
	resp, body, err = donor.Do(r)

	if err != nil {
		if callCount > 0 {
			return servRetry
		}
		writeErrorResponse("DONOR_DO "+r.Method, r, w, err)
		return servFail
	}

	storeResult, err := postProcess(donor, target, resp, r, body)
	if err != nil {
		writeErrorResponse("POST_PROCESS", r, w, err)
		return servFail
	}

	if storeResult {
		err = storeResponse(target, r.URL.String(), resp.Header, body)
		if err != nil {
			writeErrorResponse("TARGET_STORE", r, w, err)
			return servFail
		}
	}

	writeResponse(w, resp, body)
	return servOk
}

func writeErrorResponse(msg string, r *http.Request, w http.ResponseWriter, err error) {
	log.Errorf("ERR: %s (%s) <<< %s", msg, r.URL.String(), err)
	w.WriteHeader(http.StatusInternalServerError)
	fmt.Fprintln(w, msg)
}

func writeResponse(w http.ResponseWriter, resp *http.Response, respBody []byte) {
	defer func() {
		if resp.StatusCode >= 500 {
			log.Infof("CLI resp:%s %s %s", resp.Status, resp.Request.URL.String(), string(respBody))
		} else {
			log.Infof("CLI resp:%s %s", resp.Status, resp.Request.URL.String())
		}
	}()

	headers := w.Header()
	for k, v := range resp.Header {
		headers[k] = v
	}

	w.WriteHeader(resp.StatusCode)
	w.Write(respBody)
}
