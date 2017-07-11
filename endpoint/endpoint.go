package endpoint

import (
	"bytes"
	"crypto/tls"
	"errors"
	"fmt"
	"io/ioutil"
	"log"
	"net/http"
	"net/url"
	"strings"
	"time"
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
		client: &http.Client{
			Timeout: time.Second * 60,
		},
	}
}

// NewTLS make new tls Instance
func NewTLS(host, port, keyfile, crtfile string) *Instance {
	cert, err := tls.LoadX509KeyPair(crtfile, keyfile)
	if err != nil {
		log.Fatal("ERR: Cannot load key/cert", err)
	}
	config := &tls.Config{
		Certificates:       []tls.Certificate{cert},
		InsecureSkipVerify: true,
	}
	Instance := New(host, port, "https")
	Instance.client.Transport = &http.Transport{
		TLSClientConfig:    config,
		DisableCompression: true,
	}
	return Instance
}

// MakeReadOnly make Instance readonly
func (inst *Instance) MakeReadOnly() *Instance {
	inst.readonly = true
	return inst
}

func (inst *Instance) getRequest(method, path string) *http.Request {
	u, err := url.Parse(path)
	if err != nil {
		fmt.Println("ERR: cant parse URL", path)
	}

	text := method + " " + inst.protocol + "://" + inst.host + ":" + inst.port
	if len(u.RawPath) > 0 {
		text += u.RawPath
	} else {
		text += u.Path
	}
	if len(u.RawQuery) > 0 {
		text += "?" + u.RawQuery
	}
	if len(u.Fragment) > 0 {
		text += "#" + u.Fragment
	}
	fmt.Println(text)

	return &http.Request{
		Method: method,
		URL: &url.URL{
			Scheme:   inst.protocol,
			Host:     inst.host + ":" + inst.port,
			Path:     u.Path,
			RawPath:  u.RawPath,
			RawQuery: u.RawQuery,
			Fragment: u.Fragment,
		},
	}
}

// Get load data from path
func (inst *Instance) Get(path string) (resp *http.Response, err error) {
	rq := inst.getRequest("GET", path)
	return inst.client.Do(rq)
}

// Post something
func (inst *Instance) Post(path string, headers http.Header, body []byte) (resp *http.Response, err error) {
	if inst.readonly {
		fmt.Println("ERR: CANNOT WRITE TO READONLY ENDPOINT", path)
		return nil, errors.New("CANNOT WRITE TO READONLY ENDPOINT")
	}
	rq := inst.getRequest("POST", path)
	rq.Header = headers
	rq.ContentLength = int64(len(body))
	rq.Body = ioutil.NopCloser(bytes.NewBuffer(body))
	return inst.client.Do(rq)
}

// Do something
func (inst *Instance) Do(originalRq *http.Request) (resp *http.Response, err error) {
	if inst.readonly {
		if strings.ToUpper(originalRq.Method) == "POST" || strings.ToUpper(originalRq.Method) == "PUT" ||
			strings.ToUpper(originalRq.Method) == "PATCH" {
			fmt.Println("ERR: CANNOT WRITE TO READONLY ENDPOINT", originalRq.URL.String())
			return nil, errors.New("CANNOT WRITE TO READONLY ENDPOINT")
		}
	}
	originalPath := originalRq.URL.Path
	if len(originalRq.URL.RawPath) > 0 {
		originalPath = originalRq.URL.RawPath
	}

	rq := inst.getRequest(originalRq.Method, originalPath)
	rq.Header = originalRq.Header
	rq.Body = originalRq.Body
	rq.ContentLength = originalRq.ContentLength

	res, err := inst.client.Do(rq)
	counter := 50
	for err != nil {
		fmt.Println(">>> retry left:", counter, originalPath)
		time.Sleep(100 * time.Millisecond)
		res, err = inst.client.Do(rq)
		counter--
		if counter == 0 {
			break
		}
	}

	return res, err
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
