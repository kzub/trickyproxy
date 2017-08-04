package main

import (
	"flag"
	"fmt"
	"github.com/kzub/trickyproxy/endpoint"
	"io"
	"io/ioutil"
	"net/http"
	"os"
	"strings"
)

type resultStatus int

const (
	version  string       = "2.0.3"
	success  resultStatus = iota
	fail     resultStatus = iota
	notFound resultStatus = iota
)

func main() {
	keyfile := flag.String("key", "certs/service.key", "service private key")
	crtfile := flag.String("cert", "certs/service.pem", "service public cert")
	dnrfile := flag.String("donors", "donors.conf", "donors hosts list")
	trgfile := flag.String("target", "target.conf", "target host address")
	srvfile := flag.String("srvaddr", "srvaddr.conf", "server host & port to listen")
	proxmod := flag.String("mode", "riak", "proxy mode: [http | riak]")
	flag.Parse()

	if len(os.Args) > 1 && os.Args[1] == "version" {
		fmt.Println("version " + version)
		return
	}

	if *proxmod == "riak" {
		setRiakProxyMode()
	}

	donorsConfig := readConfig(*dnrfile)
	targetConfig := readConfig(*trgfile)
	serverConfig := cleanString(readConfig(*srvfile))

	donors := setupDonors(donorsConfig, *keyfile, *crtfile)
	target := setupTarget(targetConfig)
	setupServer(donors, target, serverConfig)
}

func readConfig(filename string) string {
	data, err := ioutil.ReadFile(filename)
	if err != nil {
		panic("ERR: CANNOT READ CONFIG FILE:" + filename)
	}

	return string(data[:])
}

func setupDonors(donorsConfig, keyfile, crtfile string) *endpoint.Instances {
	donorsList := strings.Split(donorsConfig, "\n")
	donors := endpoint.Instances{}

	for _, val := range donorsList {
		if len(val) == 0 {
			continue
		}
		data := strings.Split(val, ":")
		host := data[0]
		port := cleanString(data[1])
		auth := ""
		if len(data) > 2 {
			auth = cleanString(data[2])
		}
		fmt.Println("adding donor upstream", host, port)
		donors.Add(endpoint.NewTLS(host, port, auth, keyfile, crtfile).MakeReadOnly())
	}
	return &donors
}

func setupTarget(targetConfig string) *endpoint.Instance {
	data := strings.Split(targetConfig, ":")
	host := data[0]
	port := cleanString(data[1])
	space := ""
	if len(data) > 2 {
		space = cleanString(data[2]) + "_"
	}
	fmt.Println("adding target upstream", host, port, space)
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

func makeHandler(donors *endpoint.Instances, target *endpoint.Instance) func(w http.ResponseWriter, r *http.Request) {
	return func(w http.ResponseWriter, r *http.Request) {
		serveRequest(donors.Random(), target, w, r)
	}
}

func setupServer(donors *endpoint.Instances, target *endpoint.Instance, serverAddr string) {
	http.HandleFunc("/", makeHandler(donors, target))
	fmt.Println("Ready on [" + serverAddr + "]")
	err := http.ListenAndServe(serverAddr, nil)
	if err != nil {
		fmt.Println("ERR:", err)
		panic("CANNOT SETUP SERVER AT " + serverAddr)
	}
}

func serveRequest(donor *endpoint.Instance, target *endpoint.Instance, w http.ResponseWriter, r *http.Request) {
	resp, body, err := clientDoRequest(target, r)
	if err != nil {
		writeErrorResponse("TARGET_DO_METHOD "+r.Method, r, w, err)
		return
	}

	if !isNeedProxyPass(resp, r, body) {
		writeResponse(w, resp, body)
		return
	}

	fmt.Println("FETCH donor:", r.URL.Host)
	resp, body, err = clientDoRequest(donor, r)
	if err != nil {
		writeErrorResponse("DONOR_DO "+r.Method, r, w, err)
		return
	}

	storeResult, err := postProcess(donor, target, resp, r, body)
	if err != nil {
		writeErrorResponse("POST_PROCESS", r, w, err)
		return
	}

	if storeResult {
		err = storeResponse(target, r.URL.String(), resp.Header, body)
		if err != nil {
			writeErrorResponse("TARGET_STORE", r, w, err)
			return
		}
	}

	writeResponse(w, resp, body)
}

func writeErrorResponse(msg string, r *http.Request, w http.ResponseWriter, err error) {
	fmt.Printf("ERR: %s (%s)\n^^^ %s ^^^\n", msg, r.URL.String(), err)
	w.WriteHeader(http.StatusInternalServerError)
	fmt.Fprintln(w, msg)
}

func writeResponse(w http.ResponseWriter, resp *http.Response, respBody []byte) {
	defer fmt.Println("CLI resp:", resp.StatusCode, resp.Request.URL.String())

	headers := w.Header()
	for k, v := range resp.Header {
		headers[k] = v
	}

	w.WriteHeader(resp.StatusCode)
	w.Write(respBody)
}

func clientDoRequest(client *endpoint.Instance, r *http.Request) (resp *http.Response, body []byte, err error) {
	resp, err = client.Do(r)
	if err != nil && err != io.EOF {
		return nil, nil, err
	}
	defer resp.Body.Close()

	body, err = ioutil.ReadAll(resp.Body)
	if err != nil {
		return nil, nil, err
	}

	return
}
