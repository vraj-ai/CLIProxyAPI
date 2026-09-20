package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	pluginapi "github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
)

// unusableCompat is a compatibility model that must not be served. Prefix is
// the Cliproxy provider prefix. ID is the bare upstream id. Use is the
// working Cliproxy wire id.
type unusableCompat struct {
	Prefix string
	ID     string
	Use    string
	Reason string
}

// Observed 2026-09-20: OpenCode Go zen returns HTTP 500 for these with a
// working key. OpenRouter completes the replacement when max_tokens >= 16.
var unusableCompatModels = []unusableCompat{
	{
		Prefix: "ocg",
		ID:     "muse-spark-1.3-contributor",
		Use:    "or/muse-spark-1.3-contributor",
		Reason: "OpenCode Go zen returns HTTP 500 Internal server error",
	},
	{
		Prefix: "ocg",
		ID:     "muse-spark-1.2-contributor",
		Use:    "or/muse-spark-1.2-contributor",
		Reason: "OpenCode Go zen returns HTTP 500 Internal server error",
	},
}

func lookupUnusable(model string) *unusableCompat {
	model = strings.TrimSpace(model)
	if model == "" {
		return nil
	}
	prefix, id := "", model
	if i := strings.Index(model, "/"); i >= 0 {
		prefix, id = model[:i], model[i+1:]
	}
	for i := range unusableCompatModels {
		u := &unusableCompatModels[i]
		if id != u.ID {
			continue
		}
		if prefix == "" || prefix == u.Prefix {
			return u
		}
	}
	return nil
}

func terminateUnusable(req pluginapi.RequestInterceptRequest) ([]byte, bool, error) {
	u := lookupUnusable(req.Model)
	if u == nil {
		u = lookupUnusable(req.RequestedModel)
	}
	if u == nil {
		return nil, false, nil
	}
	resp := unusableDeny(u)
	resp.Headers = req.Headers
	resp.Body = req.Body
	raw, err := okEnvelope(resp)
	return raw, true, err
}

func allowServedModel(id string, g *fleetKeyGrant) bool {
	if lookupUnusable(id) != nil {
		return false
	}
	if g == nil {
		return true
	}
	return catalogAllows(g, id)
}

func unusableDeny(u *unusableCompat) pluginapi.RequestInterceptResponse {
	msg := fmt.Sprintf("%s/%s is unusable (%s). Use %s.", u.Prefix, u.ID, u.Reason, u.Use)
	body, _ := json.Marshal(map[string]any{
		"error": map[string]any{
			"message": msg,
			"type":    "invalid_request_error",
			"code":    "model_unusable",
		},
	})
	return pluginapi.RequestInterceptResponse{
		Terminate:  true,
		StatusCode: http.StatusNotFound,
		ResponseHeaders: http.Header{
			"Content-Type": []string{"application/json"},
		},
		ResponseBody: body,
	}
}
