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

type tryResult int

const (
	tryComplete tryResult = iota
	tryDonor    tryResult = iota
)

func main() {
	keyfile := flag.String("key", "certs/service.key", "service private key")
	crtfile := flag.String("cert", "certs/service.pem", "service public cert")
	dnrfile := flag.String("donors", "donors.conf", "donors hosts list")
	trgfile := flag.String("target", "target.conf", "target host address")
	srvfile := flag.String("srvaddr", "srvaddr.conf", "server host & port to listen")
	flag.Parse()

	if len(os.Args) > 1 && os.Args[1] == "version" {
		fmt.Println("version 1.5")
		return
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
		fmt.Println("adding donor upstream", host, port)
		donors.Add(endpoint.NewTLS(host, port, keyfile, crtfile).MakeReadOnly())
	}
	return &donors
}

func setupTarget(targetConfig string) *endpoint.Instance {
	data := strings.Split(targetConfig, ":")
	host := data[0]
	port := cleanString(data[1])
	fmt.Println("adding target upstream", host, port)
	target := endpoint.New(host, port, "http")
	return target
}

func cleanString(str string) string {
	str = strings.TrimRight(str, "\n")
	str = strings.TrimRight(str, "\r")
	return str
}

func serveRequest(donor *endpoint.Instance, target *endpoint.Instance, w http.ResponseWriter, r *http.Request) {
	if tryReadTargetAndAnswer(target, w, r) == tryComplete {
		// fmt.Println("---] no fetch from donor", r.URL.Path)
		return
	}
	fmt.Println("---> fetch donor:", r.URL.Path)
	readDonorWriteTargetAndAnswer(donor, target, w, r)
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

func endWithStatusCode(code int, msg string, w http.ResponseWriter) {
	w.WriteHeader(http.StatusInternalServerError)
	fmt.Fprintln(w, msg)
}

func endWithHTTPResponse(w http.ResponseWriter, resp *http.Response, respBody []byte) {
	defer fmt.Println("resp:", resp.StatusCode, resp.Request.URL.Path)

	headers := w.Header()
	for k, v := range resp.Header {
		headers[k] = v
	}

	if respBody != nil {
		w.WriteHeader(resp.StatusCode)
		w.Write(respBody)
		return
	}

	body, err := ioutil.ReadAll(resp.Body)
	resp.Body.Close()
	if err != nil {
		fmt.Printf("ERR: Failed to read response body: %s\n", err)
		endWithStatusCode(500, "TRPROXY_ERROR_READ_RESPONSE_1", w)
		return
	}

	w.WriteHeader(resp.StatusCode)
	w.Write(body)
}

func tryReadTargetAndAnswer(target *endpoint.Instance, w http.ResponseWriter, r *http.Request) tryResult {
	// read target
	tResp, err := target.Do(r)
	if err != nil {
		fmt.Printf("ERR: Failed to proxy: %s\n%s\n", r.URL.Path, err)
		endWithStatusCode(500, "TRPROXY_ERROR_TARGET_METHOD_1", w)
		return tryComplete
	}
	if tResp.StatusCode == http.StatusNotFound {
		if r.Method == "GET" || r.Method == "HEAD" {
			return tryDonor
		}
	}
	fmt.Println("tryTarget:", tResp.StatusCode, r.URL.Path)

	// answer to client
	endWithHTTPResponse(w, tResp, nil)
	return tryComplete
}

func readDonorWriteTargetAndAnswer(donor, target *endpoint.Instance, w http.ResponseWriter, r *http.Request) {
	path := r.URL.Path

	// read from donor
	dResp, err := donor.Do(r)
	if err != nil && err != io.EOF {
		fmt.Printf("ERR: Failed to donor GET path: %s\n(%s)\n", path, err)
		endWithStatusCode(500, "TRPROXY_ERROR_DONOR_GET_1", w)
		return
	}

	body, err := ioutil.ReadAll(dResp.Body)
	dResp.Body.Close()
	if err != nil {
		fmt.Printf("ERR: Failed to read response body: %s\n%s\n", path, err)
		endWithStatusCode(500, "TRPROXY_ERROR_DONOR_GET_2", w)
		return
	}

	// write to target
	if dResp.StatusCode == http.StatusOK {
		tResp, err := target.Post(path, dResp.Header, body)
		if err != nil {
			fmt.Printf("ERR: Failed to POST: %s\n%s\n", path, err)
			endWithStatusCode(500, "TRPROXY_ERROR_TARGET_POST_1", w)
			return
		}
		fmt.Println("POST status:", tResp.Status)
	}

	// answer to client
	endWithHTTPResponse(w, dResp, body)
}
