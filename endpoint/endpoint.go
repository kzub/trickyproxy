package endpoint

import (
	"bytes"
	"crypto/tls"
	"fmt"
	"io/ioutil"
	"log"
	"net/http"
	"net/url"
)

// Instance connection client
type Instance struct {
	readonly bool
	protocol string
	host     string
	port     string
	client   *http.Client
}

// New make new enfpoint
func New(host, port, protocol string) *Instance {
	if protocol != "http" && protocol != "https" {
		log.Fatal("bad protocol", protocol)
	}

	return &Instance{
		readonly: false,
		protocol: protocol,
		host:     host,
		port:     port,
		client:   &http.Client{},
	}
}

// NewTLS make new tls Instance
func NewTLS(host, port, keyfile, crtfile string) *Instance {
	cert, err := tls.LoadX509KeyPair(crtfile, keyfile)
	if err != nil {
		log.Fatal("Cannot load key/cert", err)
	}
	config := &tls.Config{
		Certificates:       []tls.Certificate{cert},
		InsecureSkipVerify: true,
	}
	Instance := New(host, port, "https")
	Instance.client.Transport = &http.Transport{TLSClientConfig: config}
	return Instance
}

// MakeReadOnly make Instance readonly
func (inst *Instance) MakeReadOnly() *Instance {
	inst.readonly = true
	return inst
}

func (inst *Instance) getRequest(method, path string) *http.Request {
	fmt.Println(method + " " + inst.protocol + "://" + inst.host + ":" + inst.port + path)
	return &http.Request{
		Method: method,
		URL: &url.URL{
			Scheme: inst.protocol,
			Host:   inst.host + ":" + inst.port,
			Path:   path,
		},
	}
}

// Get load data from path
func (inst *Instance) Get(path string) (resp *http.Response, err error) {
	rq := inst.getRequest("GET", path)
	return inst.client.Do(rq)
}

// Head load data from path
// func (inst *Instance) Head(path string) (resp *http.Response, err error) {
// 	rq := inst.getRequest("HEAD", path)
// 	return inst.client.Do(rq)
// }

// Put something
func (inst *Instance) Put(path string, headers http.Header, body []byte) (resp *http.Response, err error) {
	rq := inst.getRequest("PUT", path)
	rq.Header = headers
	rq.ContentLength = int64(len(body))
	rq.Body = ioutil.NopCloser(bytes.NewBuffer(body))
	return inst.client.Do(rq)
}

// Post something
func (inst *Instance) Post(path string, headers http.Header, body []byte) (resp *http.Response, err error) {
	rq := inst.getRequest("POST", path)
	rq.Header = headers
	rq.ContentLength = int64(len(body))
	rq.Body = ioutil.NopCloser(bytes.NewBuffer(body))
	return inst.client.Do(rq)
}

// Do something
func (inst *Instance) Do(originalRq *http.Request) (resp *http.Response, err error) {
	rq := inst.getRequest(originalRq.Method, originalRq.URL.Path)
	rq.Header = originalRq.Header
	rq.Body = originalRq.Body
	rq.ContentLength = originalRq.ContentLength
	return inst.client.Do(rq)
}

// Instances holds serveral endpoints
type Instances struct {
	instances []*Instance
	counter   int
	length    int
}

// Add instance to the pool
func (i *Instances) Add(inst *Instance) {
	i.instances = append(i.instances, inst)
	i.length++
}

// Random get random instance
func (i *Instances) Random() *Instance {
	i.counter++
	if i.counter >= i.length {
		i.counter = 0
	}
	return i.instances[i.counter]
}
