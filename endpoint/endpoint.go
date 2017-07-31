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

// URLModifier modify given URL to separate requests into several virtual spaces
type URLModifier func(text string) string

// HeaderModifier replace urls inside headers (if the are exists)
type HeaderModifier func(h http.Header) http.Header

// Instance connection client
type Instance struct {
	readonly      bool
	protocol      string
	host          string
	port          string
	auth          string
	urlEncoder    URLModifier
	headerEncoder HeaderModifier
	headerDecoder HeaderModifier
	client        *http.Client
}

// New make new enfpoint
func New(host, port, protocol, auth string, urlEncoder URLModifier, headerEncoder, headerDecoder HeaderModifier) *Instance {
	if protocol != "http" && protocol != "https" {
		log.Fatal("bad protocol", protocol)
	}

	return &Instance{
		readonly:      false,
		protocol:      protocol,
		host:          host,
		port:          port,
		auth:          auth,
		urlEncoder:    urlEncoder,
		headerEncoder: headerEncoder,
		headerDecoder: headerDecoder,
		client: &http.Client{
			Timeout: time.Second * 4,
		},
	}
}

// NewTLS make new tls Instance
func NewTLS(host, port, auth, keyfile, crtfile string) *Instance {
	cert, err := tls.LoadX509KeyPair(crtfile, keyfile)
	if err != nil {
		log.Fatal("ERR: Cannot load key/cert", err)
	}
	config := &tls.Config{
		Certificates:       []tls.Certificate{cert},
		InsecureSkipVerify: true,
	}
	Instance := New(host, port, "https", auth, nil, nil, nil)
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

func getURLText(inst *Instance, method string, u *url.URL) string {
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

	return text
}

func (inst *Instance) getRequest(method, path string, header http.Header, verbose bool) *http.Request {
	u, err := url.Parse(path)
	if err != nil {
		fmt.Println("ERR: cant parse URL", path)
	}

	// modify input headers and url (add space prefix to the url)
	if inst.urlEncoder != nil {
		u.Path = inst.urlEncoder(u.Path)
		u.RawPath = inst.urlEncoder(u.RawPath)
	}
	if inst.headerEncoder != nil {
		header = inst.headerEncoder(header)
	}
	if inst.auth != "" {
		header["Authorization"] = append(header["Authorization"], "Basic "+inst.auth)
	}
	if verbose {
		fmt.Println(getURLText(inst, method, u))
	}

	return &http.Request{
		Method: method,
		Header: header,
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
	return inst.Do(&http.Request{
		Method: "GET",
		URL: &url.URL{
			Path: path,
		},
	})
}

// Post something
func (inst *Instance) Post(path string, headers http.Header, body []byte) (resp *http.Response, err error) {
	return inst.Do(&http.Request{
		Method:        "POST",
		Header:        headers,
		ContentLength: int64(len(body)),
		Body:          ioutil.NopCloser(bytes.NewBuffer(body)),
		URL: &url.URL{
			Path: path,
		},
	})
}

// Do something
func (inst *Instance) Do(originalRq *http.Request) (resp *http.Response, err error) {
	if inst.readonly {
		if strings.ToUpper(originalRq.Method) == "POST" || strings.ToUpper(originalRq.Method) == "PUT" ||
			strings.ToUpper(originalRq.Method) == "PATCH" || strings.ToUpper(originalRq.Method) == "DELETE" {
			fmt.Println("ERR: CANNOT WRITE TO READONLY ENDPOINT", originalRq.URL.String())
			return nil, errors.New("CANNOT WRITE TO READONLY ENDPOINT")
		}
	}
	originalPath := originalRq.URL.Path
	if len(originalRq.URL.RawPath) > 0 {
		originalPath = originalRq.URL.RawPath
	}

	rq := inst.getRequest(originalRq.Method, originalPath, originalRq.Header, true)
	rq.Body = originalRq.Body
	rq.ContentLength = originalRq.ContentLength

	res, err := inst.client.Do(rq)
	counter := 50
	for err != nil {
		fmt.Println(">>> retry left:", counter, rq.URL.Path)
		time.Sleep(200 * time.Millisecond)
		res, err = inst.client.Do(rq)
		counter--
		if counter == 0 {
			break
		}
	}

	// modify output headers (remove virtual space prefixes from headers)
	if inst.headerDecoder != nil {
		res.Header = inst.headerDecoder(res.Header)
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
