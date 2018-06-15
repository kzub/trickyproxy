package main

import (
	"encoding/json"
	"errors"
	"github.com/tonymadbrain/trickyproxy/endpoint"
	log "github.com/sirupsen/logrus"
	"net/http"
	"net/url"
	"regexp"
	"strings"
)

var riakSecondaryIndexSearch *regexp.Regexp

// -- DEFAULT ---------------------------------------------
var isNeedProxyPass = isNeedProxyPassDefault
var postProcess = postProcessDefault
var urlEncoder = urlNoEncoder
var headerEncoder = headerNoEncoder
var headerDecoder = headerNoEncoder

func setRiakProxyMode() {
	var err error
	riakSecondaryIndexSearch, err = regexp.Compile("^/buckets/.*/index/")

	if err != nil {
		panic("setRiakProxyMode, cannot compile regexp")
	}

	isNeedProxyPass = isNeedProxyPassRiak
	postProcess = postProcessRiak
	urlEncoder = riakURLEncoder
	headerEncoder = riakHeaderEncoder
	headerDecoder = riakHeaderDecoder
}

func urlNoEncoder(space string) endpoint.URLModifier {
	return nil
}
func headerNoEncoder(space string) endpoint.HeaderModifier {
	return nil
}

func isNeedProxyPassDefault(resp *http.Response, r *http.Request, body []byte) bool {
	if resp.StatusCode == http.StatusNotFound {
		if r.Method == "GET" || r.Method == "HEAD" {
			return true
		}
	}
	return false
}
func isNeedProxyPassRiak(resp *http.Response, r *http.Request, body []byte) bool {
	if r.Method == "GET" && resp.StatusCode == http.StatusOK && riakSecondaryIndexSearch.MatchString(getPathFromURL(r.URL)) {
		var keys, err = getKeysFrom2iResponse(body)
		if err != nil {
			log.Errorf("ERROR PARSING 2i BODY (isNeedProxyPassRiak) %s %s", getPathFromURL(r.URL), body)
			return false
		}
		if len(keys) == 0 {
			return true
		}
		return false
	}
	return isNeedProxyPassDefault(resp, r, body)
}

func postProcessDefault(donor, target *endpoint.Instance, resp *http.Response, r *http.Request, body []byte) (storeResult bool, err error) {
	storeResult = resp.StatusCode == http.StatusOK
	if r.Method == "HEAD" {
		err = retrieveKey(donor, target, getPathFromURL(r.URL)) // update full key, not onlyHEAD
		storeResult = false
	}
	return storeResult, err
}
func postProcessRiak(donor, target *endpoint.Instance, resp *http.Response, r *http.Request, body []byte) (storeResult bool, err error) {
	if riakSecondaryIndexSearch.MatchString(getPathFromURL(r.URL)) {
		storeSecondaryIndexeResponse(donor, target, resp, r, body)
		return false, nil // exit without errors (no storing second time needed)
	}
	return postProcessDefault(donor, target, resp, r, body)
}

func storeSecondaryIndexeResponse(donor, target *endpoint.Instance, resp *http.Response, r *http.Request, body []byte) (err error) {
	keys, err := getKeysFrom2iResponse(body)
	if err != nil {
		return err
	}

	indexBucket, err := get2iBucket(getPathFromURL(r.URL))
	if err != nil {
		return err
	}

	log.Infof("GOT 2i KEYS: %s %s", len(keys), getPathFromURL(r.URL))
	for _, key := range keys {
		var keyPath = "/riak/" + indexBucket + "/" + key
		err = retrieveKey(donor, target, keyPath)
		if err != nil {
			log.Errorf("ERROR RETRIEVE KEY 2i %s %s", keyPath, err)
		}
	}

	return nil
}

func getKeysFrom2iResponse(body []byte) (result []string, err error) {
	var jsonData map[string][]string

	if err = json.Unmarshal(body, &jsonData); err != nil {
		return nil, err
	}

	for _, v := range jsonData["keys"] {
		result = append(result, v)
	}
	return result, nil
}

func get2iBucket(path string) (res string, err error) {
	if res = riakSecondaryIndexSearch.FindString(path); res == "" {
		return res, errors.New("BUCKET_NOT_FOUND get2iBucket")
	}

	var parts = strings.Split(res, "/")
	if len(parts) < 3 {
		return res, errors.New("BUCKET_NOT_FOUND get2iBucket wrong size")
	}

	return parts[2], nil
}

func get2iNameValue(path string) (name string, value string, err error) {
	var matches []int
	if matches = riakSecondaryIndexSearch.FindStringIndex(path); matches == nil {
		return name, value, errors.New("BUCKET_NOT_FOUND_2 get2iNameValue")
	}

	if len(matches) < 2 {
		return name, value, errors.New("BUCKET_NOT_FOUND_2 get2iNameValue wrong size")
	}

	var pos = matches[1]
	if pos > len(path) {
		return name, value, errors.New("BUCKET_NOT_FOUND_2 get2iNameValue wrong position")
	}

	var tail = path[pos:]
	var parts = strings.Split(tail, "/")

	if len(parts) < 2 {
		return name, value, errors.New("BUCKET_NOT_FOUND_2 get2iNameValue wrong key/value size")
	}
	name = parts[0]
	value = parts[1]

	if name == "" || value == "" {
		return name, value, errors.New("NAME_OR_VALUE_NOT_FOUND get2iNameValue")
	}

	return name, value, nil
}

// -- HELP FUNCTIONS ---------------------------------------
func storeResponse(target *endpoint.Instance, path string, headers http.Header, body []byte) (err error) {
	resp, respBody, err := target.Post(path, headers, body)
	if err != nil {
		return err
	}

	if (resp.StatusCode != http.StatusOK) && (resp.StatusCode != http.StatusNoContent) {
		log.Infof("store status: %s %s", resp.Status, respBody)
	} else {
		log.Infof("store status: %s", resp.Status)
	}
	return
}

func retrieveKey(donor, target *endpoint.Instance, keyPath string) (err error) {
	log.Infof("RETRIEVE KEY >>>>")
	resp, body, err := target.Get(keyPath)
	if err != nil {
		return errors.New("TARGET_GET_KEY")
	}

	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusNotFound {
		resp, body, err = donor.Get(keyPath)
		if err != nil || resp.StatusCode != http.StatusOK {
			return errors.New("DONOR_GET_KEY")
		}
	}

	if resp.StatusCode == http.StatusOK {
		err = storeResponse(target, keyPath, resp.Header, body)
		if err != nil {
			return errors.New("TARGET_WRITE_KEY")
		}
	}

	return nil
}

func riakURLEncoder(space string) endpoint.URLModifier {
	if space == "" {
		return nil
	}
	rexp, err := regexp.Compile("(<|^)/([^/­]+)/")
	if err != nil {
		panic("COULD NOT MAKE REGEXP ENCODER")
	}
	replaceString := "$1/$2/" + space
	return replacerFunc(rexp, replaceString)
}

func riakURLDecoder(space string) endpoint.URLModifier {
	if space == "" {
		return nil
	}

	rexp, err := regexp.Compile("(<|^)/([^/­]+)/" + space)
	if err != nil {
		panic("COULD NOT MAKE REGEXP ENCODER")
	}
	replaceString := "$1/$2/"
	return replacerFunc(rexp, replaceString)
}

func replacerFunc(rexp *regexp.Regexp, replaceString string) endpoint.URLModifier {
	return func(path string) string {
		if len(path) > 0 {
			return rexp.ReplaceAllString(path, replaceString)
		}
		return path
	}
}

func riakHeaderEncoder(space string) endpoint.HeaderModifier {
	if space == "" {
		return nil
	}
	return getHeaderCoder(riakURLEncoder(space))
}

func riakHeaderDecoder(space string) endpoint.HeaderModifier {
	if space == "" {
		return nil
	}
	return getHeaderCoder(riakURLDecoder(space))
}

// getHeaderCoder return specified encoder
func getHeaderCoder(encoder endpoint.URLModifier) endpoint.HeaderModifier {
	return func(headers http.Header) http.Header {
		h := make(http.Header)
		for k, v := range headers {
			if k == "Link" {
				h[k] = make([]string, len(headers[k]))
				for lk, lv := range v {
					h[k][lk] = encoder(lv)
				}
				continue
			}
			h[k] = v
		}
		return h
	}
}

func getPathFromURL(rURL *url.URL) string {
	path := rURL.Path
	if len(rURL.RawPath) > 0 {
		path = rURL.RawPath
	}
	return path
}
