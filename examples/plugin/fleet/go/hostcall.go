package main

// hostcall.go: synchronous host API bridge. The host stores its callback via
// fleet_host_store at init; callHost crosses back into it for capabilities
// like host.http.do so the plugin never needs its own network policy.

/*
#include "hostapi.h"
*/
import "C"

import (
	"encoding/json"
	"fmt"
	"net/http"
	"unsafe"

	pluginabi "github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginabi"
	pluginapi "github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
)

func callHost(method string, payload []byte) ([]byte, error) {
	if C.fleet_host_ref() == nil {
		return nil, fmt.Errorf("host api unavailable")
	}
	cMethod := C.CString(method)
	defer C.free(unsafe.Pointer(cMethod))
	var reqPtr *C.uint8_t
	if len(payload) > 0 {
		reqPtr = (*C.uint8_t)(C.CBytes(payload))
		defer C.free(unsafe.Pointer(reqPtr))
	}
	var resp C.cliproxy_buffer
	if rc := C.fleet_host_call(cMethod, reqPtr, C.size_t(len(payload)), &resp); rc != 0 {
		return nil, fmt.Errorf("host call %s failed: rc=%d", method, int(rc))
	}
	if resp.ptr == nil || resp.len == 0 {
		return nil, nil
	}
	out := C.GoBytes(unsafe.Pointer(resp.ptr), C.int(resp.len))
	C.fleet_host_free(resp.ptr, resp.len)
	return out, nil
}

// hostHTTPRequest is the host.http.do wire shape — the host decodes lowercase
// keys, not the pluginapi.HTTPRequest Go field names.
type hostHTTPRequest struct {
	Method  string      `json:"method"`
	URL     string      `json:"url"`
	Headers http.Header `json:"headers,omitempty"`
	Body    []byte      `json:"body,omitempty"`
}

var hostHTTP = func(req pluginapi.HTTPRequest) (*pluginapi.HTTPResponse, error) {
	raw, err := json.Marshal(hostHTTPRequest{
		Method: req.Method, URL: req.URL, Headers: req.Headers, Body: req.Body,
	})
	if err != nil {
		return nil, err
	}
	out, err := callHost(pluginabi.MethodHostHTTPDo, raw)
	if err != nil {
		return nil, err
	}
	var env envelope
	if len(out) == 0 || json.Unmarshal(out, &env) != nil {
		return nil, fmt.Errorf("host http.do returned undecodable response")
	}
	if !env.OK {
		if env.Error != nil {
			return nil, fmt.Errorf("host http.do: %s", env.Error.Message)
		}
		return nil, fmt.Errorf("host http.do failed")
	}
	var resp pluginapi.HTTPResponse
	if err := json.Unmarshal(env.Result, &resp); err != nil {
		return nil, fmt.Errorf("host http.do result decode: %w", err)
	}
	return &resp, nil
}
