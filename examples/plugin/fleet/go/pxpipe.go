package main

// pxpipe.go: transform hop that lets the cliproxyapi link include pxpipe.
// Requests for in-scope models are posted to the pxpipe-transform shim
// (services/pxpipe-transform), which wraps the installed pxpipe-proxy
// library — pxpipe has no transform-only HTTP route of its own. The shim
// owns scope decisions from ~/.config/pxpipe/config.json so both pxpipe
// incarnations share one source of truth. Failure fails open: the transform
// is an optimization, never a hard dependency.

import (
	"bytes"
	"encoding/json"
	"net/http"
	"strings"

	pluginapi "github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
)

// pxpipeMarker marks bodies pxpipe already transformed (harness entered via
// the pxpipe proxy port instead of the canonical cliproxyapi link). Skipping
// them prevents double transforms.
const pxpipeMarker = "injected by pxpipe"

// pxpipeFormats maps plugin source formats to pxpipe transform families.
func pxpipeFormat(sourceFormat string) string {
	switch sourceFormat {
	case "openai":
		return "openai-chat"
	case "openai-response", "codex":
		return "openai-responses"
	case "claude":
		return "anthropic"
	default:
		return ""
	}
}

type pxpipeTransformRequest struct {
	Format string          `json:"format"`
	Model  string          `json:"model"`
	Body   json.RawMessage `json:"body"`
}

type pxpipeTransformResponse struct {
	Body    json.RawMessage `json:"body"`
	Applied bool            `json:"applied"`
	Reason  string          `json:"reason"`
}

// pxpipeTransform posts the body to the shim when enabled and returns the
// transformed body. Any failure returns the original body unchanged.
func pxpipeTransform(cfg pluginConfig, sourceFormat, model string, body []byte) []byte {
	if !modelFeatureEnabled(cfg, model, featurePxpipe) || len(body) == 0 {
		return body
	}
	format := pxpipeFormat(sourceFormat)
	if format == "" {
		return body
	}
	if strings.EqualFold(strings.TrimSpace(model), virtualRouterModel) || bytes.Contains(body, []byte(pxpipeMarker)) {
		return body
	}
	url := strings.TrimRight(cfg.PxpipeURL, "/") + "/transform"
	raw, err := json.Marshal(pxpipeTransformRequest{Format: format, Model: model, Body: body})
	if err != nil {
		return body
	}
	resp, err := hostHTTP(pluginapi.HTTPRequest{
		Method:  http.MethodPost,
		URL:     url,
		Headers: http.Header{"content-type": []string{"application/json"}},
		Body:    raw,
	})
	if err != nil || resp.StatusCode != http.StatusOK {
		return body
	}
	var out pxpipeTransformResponse
	if json.Unmarshal(resp.Body, &out) != nil || !out.Applied || len(out.Body) == 0 {
		return body
	}
	return out.Body
}
