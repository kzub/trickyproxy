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
	"sync"
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
			Transport: &http.Transport{
				DisableCompression: true, // without this decompress plain data by default.
			},
		},
	}
}

// NewTLS make new tls Instance
func NewTLS(protocol, host, port, auth, keyfile, crtfile string) *Instance {
	cert, err := tls.LoadX509KeyPair(crtfile, keyfile)
	if err != nil {
		fmt.Println("No certificates loaded:", err)
	}
	config := &tls.Config{
		Certificates:       []tls.Certificate{cert},
		InsecureSkipVerify: true,
	}
	Instance := New(host, port, protocol, auth, nil, nil, nil)
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

func (inst *Instance) getRequest(originalRq *http.Request) *http.Request {
	newURL := &url.URL{
		Scheme:     inst.protocol,
		Opaque:     originalRq.URL.Opaque,
		User:       originalRq.URL.User,
		Host:       inst.host + ":" + inst.port,
		Path:       originalRq.URL.Path,
		RawPath:    originalRq.URL.RawPath,
		ForceQuery: originalRq.URL.ForceQuery,
		RawQuery:   originalRq.URL.RawQuery,
		Fragment:   originalRq.URL.Fragment,
	}

	// modify input headers and url (add space prefix to the url)
	if inst.urlEncoder != nil {
		newURL.Path = inst.urlEncoder(originalRq.URL.Path)
		newURL.RawPath = inst.urlEncoder(originalRq.URL.RawPath)
	}
	var header = make(http.Header)
	if inst.headerEncoder != nil {
		header = inst.headerEncoder(originalRq.Header)
	}
	if inst.auth != "" {
		header["Authorization"] = append(header["Authorization"], "Basic "+inst.auth)
	}

	fmt.Println(getURLText(inst, originalRq.Method, newURL))

	return &http.Request{
		Method:        originalRq.Method,
		Header:        header,
		URL:           newURL,
		Body:          originalRq.Body,
		ContentLength: originalRq.ContentLength,
	}
}

// Get load data from path
func (inst *Instance) Get(path string) (resp *http.Response, body []byte, err error) {
	url, _ := url.Parse(path)
	return inst.Do(&http.Request{
		Method: "GET",
		URL:    url,
	})
}

// Post something
func (inst *Instance) Post(path string, headers http.Header, body []byte) (resp *http.Response, body2 []byte, err error) {
	url, _ := url.Parse(path)
	return inst.Do(&http.Request{
		Method:        "POST",
		Header:        headers,
		ContentLength: int64(len(body)),
		Body:          ioutil.NopCloser(bytes.NewBuffer(body)),
		URL:           url,
	})
}

// Do something
func (inst *Instance) Do(originalRq *http.Request) (resp *http.Response, body []byte, err error) {
	if inst.readonly {
		if strings.ToUpper(originalRq.Method) == "POST" || strings.ToUpper(originalRq.Method) == "PUT" ||
			strings.ToUpper(originalRq.Method) == "PATCH" || strings.ToUpper(originalRq.Method) == "DELETE" {
			fmt.Println("ERR: CANNOT WRITE TO READONLY ENDPOINT", originalRq.URL.String())
			return nil, nil, errors.New("CANNOT WRITE TO READONLY ENDPOINT")
		}
	}

	rq := inst.getRequest(originalRq)
	var rqBodyData []byte

	// make body data copy for retry
	if rq.Body != nil {
		rqBodyData, err = ioutil.ReadAll(rq.Body)
		rq.Body.Close()
		if err != nil {
			fmt.Println("ERR: RQ_READ_BODY", err)
			return nil, nil, err
		}
		rq.Body = ioutil.NopCloser(bytes.NewBuffer(rqBodyData))
	}

	// make a request!
	resp, err = inst.client.Do(rq)

	counter := 10
	for err != nil {
		fmt.Println(">>> retry left:", counter, getURLText(inst, originalRq.Method, rq.URL))
		time.Sleep(500 * time.Millisecond)

		// make new reader from stored data
		if rq.Body != nil {
			rq.Body = ioutil.NopCloser(bytes.NewBuffer(rqBodyData))
		}
		// make a request again!
		resp, err = inst.client.Do(rq)
		counter--

		if err != nil && counter == 0 {
			fmt.Println("ERR: DO_FAILED", err)
			return nil, nil, err
		}
	}

	// no error here, read body
	if resp.Body != nil {
		defer resp.Body.Close()
		body, err = ioutil.ReadAll(resp.Body)
		if err != nil {
			fmt.Println("ERR: RESP_READ_BODY", err)
			return nil, nil, err
		}
	}

	// modify output headers (remove virtual space prefixes from headers)
	if inst.headerDecoder != nil {
		resp.Header = inst.headerDecoder(resp.Header)
	}

	return resp, body, err
}

// Instances holds serveral endpoints
type Instances struct {
	instances []*Instance
	counter   int
	length    int
	mutex     *sync.Mutex
}

// NewInstances make new instances list
func NewInstances() *Instances {
	return &Instances{
		mutex: &sync.Mutex{},
	}
}

// Add instance to the pool
func (i *Instances) Add(inst *Instance) {
	i.mutex.Lock()
	i.instances = append(i.instances, inst)
	i.length++
	i.mutex.Unlock()
}

// Next get next endpoint instance
func (i *Instances) Next() *Instance {
	i.mutex.Lock()
	i.counter++
	if i.counter >= i.length {
		i.counter = 0
	}
	var instanceIdx = i.counter
	i.mutex.Unlock()
	return i.instances[instanceIdx]
}
