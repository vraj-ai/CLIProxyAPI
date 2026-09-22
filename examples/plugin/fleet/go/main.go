// fleet plugin: injects caveman + ponytail instructions into every routed
// request, and exposes a /savings management resource aggregating pxpipe and
// pi-subagents compression telemetry.
package main

/*
#include <stdint.h>
#include <stdlib.h>

#include "hostapi.h"

typedef int (*cliproxy_plugin_call_fn)(char*, uint8_t*, size_t, cliproxy_buffer*);
typedef void (*cliproxy_plugin_free_fn)(void*, size_t);
typedef void (*cliproxy_plugin_shutdown_fn)(void);

typedef struct {
	uint32_t abi_version;
	cliproxy_plugin_call_fn call;
	cliproxy_plugin_free_fn free_buffer;
	cliproxy_plugin_shutdown_fn shutdown;
} cliproxy_plugin_api;

extern int cliproxyPluginCall(char*, uint8_t*, size_t, cliproxy_buffer*);
extern void cliproxyPluginFree(void*, size_t);
extern void cliproxyPluginShutdown(void);

*/
import "C"

import (
	"bufio"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
	"unsafe"

	pluginabi "github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginabi"
	pluginapi "github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
	"gopkg.in/yaml.v3"
)

func main() {}

//export cliproxy_plugin_init
func cliproxy_plugin_init(host *C.cliproxy_host_api, plugin *C.cliproxy_plugin_api) C.int {
	if plugin == nil {
		return 1
	}
	C.fleet_host_store(host)
	plugin.abi_version = C.uint32_t(pluginabi.ABIVersion)
	plugin.call = C.cliproxy_plugin_call_fn(C.cliproxyPluginCall)
	plugin.free_buffer = C.cliproxy_plugin_free_fn(C.cliproxyPluginFree)
	plugin.shutdown = C.cliproxy_plugin_shutdown_fn(C.cliproxyPluginShutdown)
	return 0
}

//export cliproxyPluginCall
func cliproxyPluginCall(method *C.char, request *C.uint8_t, requestLen C.size_t, response *C.cliproxy_buffer) C.int {
	if response != nil {
		response.ptr = nil
		response.len = 0
	}
	if method == nil {
		writeResponse(response, errorEnvelope("invalid_method", "method is required"))
		return 1
	}
	raw, errHandle := handleMethod(C.GoString(method), C.GoBytes(unsafe.Pointer(request), C.int(requestLen)))
	if errHandle != nil {
		writeResponse(response, errorEnvelope("plugin_error", errHandle.Error()))
		return 1
	}
	writeResponse(response, raw)
	return 0
}

//export cliproxyPluginFree
func cliproxyPluginFree(ptr unsafe.Pointer, len C.size_t) {
	if ptr != nil {
		C.free(ptr)
	}
	_ = len
}

//export cliproxyPluginShutdown
func cliproxyPluginShutdown() {}

// ---------------------------------------------------------------------------
// config

type pluginConfig struct {
	Caveman           bool             `yaml:"caveman"`
	Ponytail          bool             `yaml:"ponytail"`
	ClientTools       clientToolConfig `yaml:"client_tools"`
	Models            []string         `yaml:"models"` // model prefixes to inject; empty = all
	ModelPoliciesPath string           `yaml:"model_policies_path"`
	PxpipeEvents      string           `yaml:"pxpipe_events"`     // events.jsonl path
	UsageGain         string           `yaml:"usage_gain"`        // pi usage-gain.jsonl path
	RouterEnabled     bool             `yaml:"router_enabled"`    // expose the "cpa router" virtual model
	RouterAgent       string           `yaml:"router_agent"`      // herdr agent selector used as lead
	RouterTimeoutMS   int              `yaml:"router_timeout_ms"` // bound on the lead wait
	RouterReadLines   int              `yaml:"router_read_lines"` // read-back window for marker extraction
	RedactSentinels   []string         `yaml:"redact_sentinels"`  // literals that must never appear in diagnostics
	PxpipeEnabled     bool             `yaml:"pxpipe_enabled"`    // route eligible bodies through the pxpipe transform shim
	PxpipeURL         string           `yaml:"pxpipe_url"`        // pxpipe-transform shim base URL
}

var state = struct {
	mu               sync.Mutex
	config           pluginConfig
	policies         map[string]modelFeaturePolicy
	policyPath       string
	injections       int64
	cavemanEdits     int64
	ponytailEdits    int64
	lastEditModel    string
	lastEditAt       time.Time
	lastEditCaveman  bool
	lastEditPonytail bool
	pace             paceState
	backend          leadBackend
	quota            quotaSource
	decisions        []routeDecision
	sentinels        []string
	quotaCache       struct {
		at       time.Time
		snap     map[string]providerQuota
		err      error
		fetching bool
	}
}{}

type lifecycleRequest struct {
	SchemaVersion int    `json:"schema_version"`
	ConfigYAML    []byte `json:"config_yaml"`
}

func configure(raw []byte) error {
	var req lifecycleRequest
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, &req); err != nil {
			return err
		}
	}
	cfg := pluginConfig{
		Caveman:  true,
		Ponytail: true,
	}
	if len(req.ConfigYAML) > 0 {
		if err := yaml.Unmarshal(req.ConfigYAML, &cfg); err != nil {
			return err
		}
	}
	home, _ := os.UserHomeDir()
	if cfg.PxpipeEvents == "" {
		cfg.PxpipeEvents = filepath.Join(home, ".pxpipe", "events.jsonl")
	}
	if cfg.UsageGain == "" {
		cfg.UsageGain = filepath.Join(home, ".pi", "agent", "usage-gain.jsonl")
	}
	if cfg.PxpipeURL == "" {
		cfg.PxpipeURL = "http://127.0.0.1:47822"
	}
	policyPath := featurePolicyPath(cfg)
	policies, err := loadModelPolicies(policyPath)
	if err != nil {
		return fmt.Errorf("load fleet model policies: %w", err)
	}
	state.mu.Lock()
	state.config = cfg
	state.policies = policies
	state.policyPath = policyPath
	state.sentinels = cfg.RedactSentinels
	state.mu.Unlock()
	return nil
}

// ---------------------------------------------------------------------------
// instruction injection

const cavemanText = "Reply in ultra-compressed technical English: drop filler, articles, and pleasantries while keeping full technical accuracy. Never compress security warnings, irreversible-action confirmations, or multi-step instructions."

const ponytailText = "Choose the laziest solution that actually works: prefer deletion and minimal diffs, no speculative abstraction, no boilerplate. Mark deliberate simplifications with a `ponytail:` comment naming the ceiling and upgrade path."

func instructionText(cfg pluginConfig) string {
	var parts []string
	if cfg.Caveman {
		parts = append(parts, cavemanText)
	}
	if cfg.Ponytail {
		parts = append(parts, ponytailText)
	}
	return strings.Join(parts, "\n\n")
}

func instructionTextForModel(cfg pluginConfig, model string) string {
	parts := make([]string, 0, 2)
	if modelFeatureEnabled(cfg, model, featureCaveman) {
		parts = append(parts, cavemanText)
	}
	if modelFeatureEnabled(cfg, model, featurePonytail) {
		parts = append(parts, ponytailText)
	}
	return strings.Join(parts, "\n\n")
}

func modelAllowed(model string, prefixes []string) bool {
	if len(prefixes) == 0 {
		return true
	}
	for _, p := range prefixes {
		if strings.HasPrefix(model, p) {
			return true
		}
	}
	return false
}

// inject prepends text into the request body's system context. The body shape
// is detected by key presence — the protocol translation layer handles the
// rest downstream.
func inject(text, format string, body []byte) ([]byte, bool) {
	if text == "" || len(body) == 0 {
		return body, false
	}
	var doc map[string]any
	if json.Unmarshal(body, &doc) != nil || doc == nil {
		return body, false
	}
	var out []byte
	var changed bool
	switch format {
	case "openai":
		out, changed = injectOpenAI(text, doc)
	case "claude":
		out, changed = injectAnthropic(text, doc)
	case "openai-response", "codex":
		out, changed = injectResponses(text, doc)
	default:
		return body, false
	}
	if !changed {
		return body, false
	}
	return out, true
}

func injectOpenAI(text string, doc map[string]any) ([]byte, bool) {
	msgs, ok := doc["messages"].([]any)
	if !ok {
		return nil, false
	}
	if len(msgs) > 0 {
		if first, ok := msgs[0].(map[string]any); ok && first["role"] == "system" {
			switch c := first["content"].(type) {
			case string:
				first["content"] = c + "\n\n" + text
			case []any:
				first["content"] = append([]any{map[string]any{"type": "text", "text": text}}, c...)
			default:
				return nil, false
			}
			return marshalDoc(doc)
		}
	}
	doc["messages"] = append([]any{map[string]any{"role": "system", "content": text}}, msgs...)
	return marshalDoc(doc)
}

func injectAnthropic(text string, doc map[string]any) ([]byte, bool) {
	switch s := doc["system"].(type) {
	case string:
		doc["system"] = s + "\n\n" + text
	case []any:
		doc["system"] = append([]any{map[string]any{"type": "text", "text": text}}, s...)
	case nil:
		doc["system"] = text
	default:
		return nil, false
	}
	return marshalDoc(doc)
}

func injectResponses(text string, doc map[string]any) ([]byte, bool) {
	if v, exists := doc["instructions"]; exists && v != nil {
		ins, ok := v.(string)
		if !ok {
			return nil, false
		}
		doc["instructions"] = text + "\n\n" + ins
		return marshalDoc(doc)
	}
	doc["instructions"] = text
	return marshalDoc(doc)
}

func marshalDoc(doc map[string]any) ([]byte, bool) {
	raw, err := json.Marshal(doc)
	return raw, err == nil
}

// ---------------------------------------------------------------------------
// rpc dispatch

// handleMethod recovers panics: a panic crossing the cgo boundary aborts the
// whole host process, not just this call.
func handleMethod(method string, request []byte) (out []byte, err error) {
	defer func() {
		if r := recover(); r != nil {
			out, err = nil, fmt.Errorf("panic in %s: %v", method, r)
		}
	}()
	switch method {
	case pluginabi.MethodPluginRegister, pluginabi.MethodPluginReconfigure:
		if err := configure(request); err != nil {
			return nil, err
		}
		return okEnvelope(buildRegistration())
	case pluginabi.MethodRequestInterceptBefore:
		return intercept(request)
	case pluginabi.MethodRequestInterceptAfter:
		return interceptAfter(request)
	case pluginabi.MethodResponseInterceptAfter:
		return interceptModelsResponse(request)
	case pluginabi.MethodUsageHandle:
		return noteUsage(request)
	case pluginabi.MethodModelRegister:
		return okEnvelope(modelRegistration())
	case pluginabi.MethodExecutorIdentifier:
		return okEnvelopeJSON(`{"identifier":"` + routerProvider + `"}`)
	case pluginabi.MethodExecutorExecute:
		return executorExecute(request)
	case pluginabi.MethodExecutorExecuteStream:
		return executorExecuteStream(request)
	case pluginabi.MethodExecutorCountTokens:
		return errorEnvelope("unsupported_capability", "cpa router does not report token counts"), nil
	case pluginabi.MethodExecutorHTTPRequest:
		return errorEnvelope("unsupported_capability", "cpa router does not serve raw http requests"), nil
	case pluginabi.MethodAuthIdentifier:
		return authIdentifier()
	case pluginabi.MethodAuthParse:
		return authParse(request)
	case pluginabi.MethodAuthRefresh:
		return authRefresh(request)
	case pluginabi.MethodAuthLoginStart, pluginabi.MethodAuthLoginPoll:
		return errorEnvelope("unsupported_capability", "fleet-router auth is file-based; no login flow"), nil
	case "management.register":
		return okEnvelopeJSON(`{"routes":[
			{"Method":"GET","Path":"/fleet/state"},
			{"Method":"GET","Path":"/fleet/savings"},
			{"Method":"GET","Path":"/fleet/router"},
			{"Method":"GET","Path":"/fleet/keys"},
			{"Method":"POST","Path":"/fleet/keys"},
			{"Method":"PATCH","Path":"/fleet/keys"},
			{"Method":"DELETE","Path":"/fleet/keys"},
			{"Method":"POST","Path":"/fleet/keys/adopt"},
			{"Method":"POST","Path":"/fleet/pxpipe/scope"},
			{"Method":"POST","Path":"/fleet/pxpipe/scope/toggle"},
			{"Method":"POST","Path":"/fleet/pxpipe/compression"},
			{"Method":"POST","Path":"/fleet/features"}
		],"resources":[
			{"Path":"/hub","Menu":"Fleet Hub","Description":"Path controls, quota pace, and the model running through the proxy."},
			{"Path":"/keys","Menu":"Keys","Description":"Mint API keys for the models this proxy currently serves."},
			{"Path":"/savings","Menu":"Savings","Description":"pxpipe rows and instruction body edits. Not token or dollar savings."}
		]}`)
	case "management.handle":
		var req pluginapi.ManagementRequest
		_ = json.Unmarshal(request, &req)
		return managementDispatch(&req, request)
	default:
		return errorEnvelope("unknown_method", "unknown method: "+method), nil
	}
}

type registrationCapability struct {
	RequestInterceptor    bool     `json:"request_interceptor"`
	ResponseInterceptor   bool     `json:"response_interceptor"`
	ManagementAPI         bool     `json:"management_api"`
	ModelRegistrar        bool     `json:"model_registrar"`
	AuthProvider          bool     `json:"auth_provider"`
	Executor              bool     `json:"executor"`
	UsagePlugin           bool     `json:"usage_plugin"`
	ExecutorModelScope    string   `json:"executor_model_scope,omitempty"`
	ExecutorInputFormats  []string `json:"executor_input_formats,omitempty"`
	ExecutorOutputFormats []string `json:"executor_output_formats,omitempty"`
}

type pluginRegistration struct {
	SchemaVersion uint32                 `json:"schema_version"`
	Metadata      pluginapi.Metadata     `json:"metadata"`
	Capabilities  registrationCapability `json:"capabilities"`
}

func buildRegistration() pluginRegistration {
	return pluginRegistration{
		SchemaVersion: pluginabi.SchemaVersion,
		Metadata: pluginapi.Metadata{
			Name:             "fleet",
			Version:          "0.1.0",
			Author:           "vraj",
			GitHubRepository: "https://github.com/vraj-ai/CLIProxyAPI",
			ConfigFields: []pluginapi.ConfigField{
				{Name: "caveman", Type: pluginapi.ConfigFieldTypeBoolean, Description: "Inject caveman compressed-reply instruction into routed requests."},
				{Name: "ponytail", Type: pluginapi.ConfigFieldTypeBoolean, Description: "Inject ponytail lazy-solution instruction into routed requests."},
				{Name: "models", Type: pluginapi.ConfigFieldTypeArray, Description: "Model prefixes to inject; empty means all."},
				{Name: "router_enabled", Type: pluginapi.ConfigFieldTypeBoolean, Description: "Expose the cpa router virtual model through the Herdr-orchestrated executor."},
				{Name: "router_agent", Type: pluginapi.ConfigFieldTypeString, Description: "Herdr agent selector (agent kind or pane id) used as the lead."},
				{Name: "router_timeout_ms", Type: pluginapi.ConfigFieldTypeInteger, Description: "Bound on the lead wait in milliseconds."},
				{Name: "router_read_lines", Type: pluginapi.ConfigFieldTypeInteger, Description: "Terminal read-back window for result marker extraction."},
				{Name: "pxpipe_enabled", Type: pluginapi.ConfigFieldTypeBoolean, Description: "Route in-scope request bodies through the pxpipe transform shim so the cliproxyapi link includes pxpipe."},
				{Name: "pxpipe_url", Type: pluginapi.ConfigFieldTypeString, Description: "pxpipe-transform shim base URL (default http://127.0.0.1:47822)."},
				{Name: "client_tools", Type: pluginapi.ConfigFieldTypeObject, Description: "Opt-in client layers. Headroom is a local context proxy and RTK rewrites shell output."},
				{Name: "model_policies_path", Type: pluginapi.ConfigFieldTypeString, Description: "Private path for exact-model feature overrides."},
			},
		},
		Capabilities: registrationCapability{
			RequestInterceptor:    true,
			ResponseInterceptor:   true,
			ManagementAPI:         true,
			ModelRegistrar:        true,
			AuthProvider:          true,
			Executor:              true,
			UsagePlugin:           true,
			ExecutorModelScope:    string(pluginapi.ExecutorModelScopeStatic),
			ExecutorInputFormats:  []string{"openai"},
			ExecutorOutputFormats: []string{"openai"},
		},
	}
}

func intercept(raw []byte) ([]byte, error) {
	var req pluginapi.RequestInterceptRequest
	if err := json.Unmarshal(raw, &req); err != nil {
		return nil, err
	}
	if raw, hit, err := terminateUnusable(req); hit || err != nil {
		return raw, err
	}
	if !grantAllowsRequest(extractAPIKey(req.Headers, req.Metadata), req.Model, req.RequestedModel) {
		return okEnvelope(pluginapi.RequestInterceptResponse{
			Headers:         req.Headers,
			Body:            req.Body,
			Terminate:       true,
			StatusCode:      http.StatusForbidden,
			ResponseHeaders: http.Header{"Content-Type": []string{"application/json"}},
			ResponseBody:    deniedModelBody,
		})
	}
	state.mu.Lock()
	cfg := state.config
	state.mu.Unlock()
	body := pxpipeTransform(cfg, req.SourceFormat, req.Model, req.Body)
	if modelAllowed(req.Model, cfg.Models) || modelAllowed(req.RequestedModel, cfg.Models) {
		cavemanOn := modelFeatureEnabled(cfg, req.Model, featureCaveman)
		ponytailOn := modelFeatureEnabled(cfg, req.Model, featurePonytail)
		if injected, ok := inject(instructionTextForModel(cfg, req.Model), req.SourceFormat, body); ok {
			body = injected
			state.mu.Lock()
			state.injections++
			if cavemanOn {
				state.cavemanEdits++
			}
			if ponytailOn {
				state.ponytailEdits++
			}
			state.lastEditModel = req.Model
			state.lastEditAt = time.Now()
			state.lastEditCaveman = cavemanOn
			state.lastEditPonytail = ponytailOn
			state.mu.Unlock()
		}
	}
	if req.Model != "" && req.Model != virtualRouterModel {
		markRunning(req.Model)
	}
	return okEnvelope(pluginapi.RequestInterceptResponse{Headers: req.Headers, Body: body})
}

func interceptAfter(raw []byte) ([]byte, error) {
	var req pluginapi.RequestInterceptRequest
	if err := json.Unmarshal(raw, &req); err != nil {
		return nil, err
	}
	if rawOut, hit, err := terminateUnusable(req); hit || err != nil {
		return rawOut, err
	}
	if !grantAllowsRequest(extractAPIKey(req.Headers, req.Metadata), req.Model, req.RequestedModel) {
		return okEnvelope(pluginapi.RequestInterceptResponse{
			Headers:         req.Headers,
			Body:            req.Body,
			Terminate:       true,
			StatusCode:      http.StatusForbidden,
			ResponseHeaders: http.Header{"Content-Type": []string{"application/json"}},
			ResponseBody:    deniedModelBody,
		})
	}
	return okEnvelope(pluginapi.RequestInterceptResponse{Headers: req.Headers, Body: req.Body})
}

func interceptModelsResponse(raw []byte) ([]byte, error) {
	var req pluginapi.ResponseInterceptRequest
	if err := json.Unmarshal(raw, &req); err != nil {
		return nil, err
	}
	secret := extractAPIKey(req.RequestHeaders, req.Metadata)
	g, err := grantForSecret(secret)
	allow := func(id string) bool { return allowServedModel(id, g) }
	if err != nil {
		allow = func(string) bool { return false }
	}
	body := filterModelList(req.Body, allow)
	return okEnvelope(pluginapi.ResponseInterceptResponse{Headers: req.ResponseHeaders, Body: body})
}

// ---------------------------------------------------------------------------
// savings resource

type pxEvent struct {
	Method            string `json:"method"`
	Model             string `json:"model"`
	Compressed        bool   `json:"compressed"`
	Status            int    `json:"status"`
	OrigChars         *int64 `json:"orig_chars"`
	OutgoingTextChars *int64 `json:"outgoing_text_chars"`
	InputTokens       int64  `json:"input_tokens"`
	OutputTokens      int64  `json:"output_tokens"`
}

type ugEvent struct {
	Source        string `json:"source"`
	OriginalBytes *int64 `json:"originalBytes"`
	OutputBytes   *int64 `json:"outputBytes"`
}

type pxRecent struct {
	Model        string `json:"model"`
	Compressed   bool   `json:"compressed"`
	OrigChars    *int64 `json:"orig_chars,omitempty"`
	OutChars     *int64 `json:"outgoing_text_chars,omitempty"`
	InputTokens  int64  `json:"input_tokens,omitempty"`
	OutputTokens int64  `json:"output_tokens,omitempty"`
}

type pxStats struct {
	Requests     int64            `json:"requests"`
	Compressed   int64            `json:"compressed"`
	CharsBefore  int64            `json:"chars_before"`
	CharsAfter   int64            `json:"chars_after"`
	SavedPct     float64          `json:"saved_pct"`
	InputTokens  int64            `json:"input_tokens"`
	OutputTokens int64            `json:"output_tokens"`
	ByModel      map[string]int64 `json:"saved_chars_by_model"`
	Recent       []pxRecent       `json:"recent"`
}

type ugStats struct {
	Events   int64   `json:"events"`
	Before   int64   `json:"bytes_before"`
	After    int64   `json:"bytes_after"`
	SavedPct float64 `json:"saved_pct"`
}

var savingsMemo struct {
	mu    sync.Mutex
	pxKey string
	ugKey string
	px    pxStats
	ug    ugStats
}

func fileKey(path string) string {
	st, err := os.Stat(path)
	if err != nil {
		return ""
	}
	return fmt.Sprintf("%d:%d", st.ModTime().UnixNano(), st.Size())
}

func savingsJSON() ([]byte, error) {
	px, ug, inj, err := loadSavings()
	if err != nil {
		return nil, err
	}
	payload := map[string]any{
		"pxpipe":                  px,
		"subagent_context":        ug,
		"injections_this_process": inj,
		"instructions":            instructionProof(),
		"note":                    "Historical local logs, not attributed to this proxy route. Character/byte reductions exclude image token cost and do not measure token or dollar savings. Injection counts record body edits, not model compliance or savings.",
	}
	out, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}
	return okEnvelope(pluginapi.ManagementResponse{
		StatusCode: http.StatusOK,
		Headers:    http.Header{"Content-Type": []string{"application/json"}},
		Body:       out,
	})
}

func loadSavings() (pxStats, ugStats, int64, error) {
	state.mu.Lock()
	cfg := state.config
	inj := state.injections
	state.mu.Unlock()
	pxKey, ugKey := fileKey(cfg.PxpipeEvents), fileKey(cfg.UsageGain)
	savingsMemo.mu.Lock()
	if pxKey != "" && ugKey != "" && pxKey == savingsMemo.pxKey && ugKey == savingsMemo.ugKey {
		px, ug := savingsMemo.px, savingsMemo.ug
		savingsMemo.mu.Unlock()
		return px, ug, inj, nil
	}
	savingsMemo.mu.Unlock()

	px := pxStats{ByModel: map[string]int64{}, Recent: []pxRecent{}}

	if f, err := os.Open(cfg.PxpipeEvents); err == nil {
		sc := bufio.NewScanner(f)
		sc.Buffer(make([]byte, 1<<20), 1<<20)
		for sc.Scan() {
			var e pxEvent
			if json.Unmarshal(sc.Bytes(), &e) != nil || e.Method != "POST" {
				continue
			}
			px.Requests++
			px.InputTokens += e.InputTokens
			px.OutputTokens += e.OutputTokens
			px.Recent = append(px.Recent, pxRecent{
				Model: e.Model, Compressed: e.Compressed && e.Status >= 200 && e.Status < 300,
				OrigChars: e.OrigChars, OutChars: e.OutgoingTextChars,
				InputTokens: e.InputTokens, OutputTokens: e.OutputTokens,
			})
			if len(px.Recent) > 8 {
				px.Recent = px.Recent[len(px.Recent)-8:]
			}
			if e.Compressed && e.Status >= 200 && e.Status < 300 && e.OrigChars != nil && e.OutgoingTextChars != nil && *e.OrigChars > 0 && *e.OutgoingTextChars >= 0 {
				px.Compressed++
				px.CharsBefore += *e.OrigChars
				px.CharsAfter += *e.OutgoingTextChars
				px.ByModel[e.Model] += *e.OrigChars - *e.OutgoingTextChars
			}
		}
		errScan := sc.Err()
		errClose := f.Close()
		if errScan != nil || errClose != nil {
			return pxStats{}, ugStats{}, 0, fmt.Errorf("telemetry read failed; measurements unavailable")
		}
	} else {
		return pxStats{}, ugStats{}, 0, fmt.Errorf("telemetry unavailable; measurements not reported")
	}
	if px.CharsBefore > 0 {
		px.SavedPct = float64(px.CharsBefore-px.CharsAfter) / float64(px.CharsBefore) * 100
	}

	ug := ugStats{}
	if f, err := os.Open(cfg.UsageGain); err == nil {
		sc := bufio.NewScanner(f)
		sc.Buffer(make([]byte, 1<<20), 1<<20)
		for sc.Scan() {
			var e ugEvent
			if json.Unmarshal(sc.Bytes(), &e) != nil || e.Source != "compression" || e.OriginalBytes == nil || e.OutputBytes == nil || *e.OriginalBytes <= 0 || *e.OutputBytes < 0 {
				continue
			}
			ug.Events++
			ug.Before += *e.OriginalBytes
			ug.After += *e.OutputBytes
		}
		errScan := sc.Err()
		errClose := f.Close()
		if errScan != nil || errClose != nil {
			return pxStats{}, ugStats{}, 0, fmt.Errorf("telemetry read failed; measurements unavailable")
		}
	} else {
		return pxStats{}, ugStats{}, 0, fmt.Errorf("telemetry unavailable; measurements not reported")
	}
	if ug.Before > 0 {
		ug.SavedPct = float64(ug.Before-ug.After) / float64(ug.Before) * 100
	}
	savingsMemo.mu.Lock()
	savingsMemo.pxKey, savingsMemo.ugKey = pxKey, ugKey
	savingsMemo.px, savingsMemo.ug = px, ug
	savingsMemo.mu.Unlock()
	return px, ug, inj, nil
}

// modelRow is one line of the per-model reduction table.
type modelRow struct {
	Model   string
	Reduced int64
}

// savingsView is the page data shape: measured pxpipe and subagent-context
// reductions, the process injection count, and sorted per-model rows.
type savingsView struct {
	Px         pxStats
	Ug         ugStats
	Injections int64
	Models     []modelRow
}

func newSavingsView(px pxStats, ug ugStats, inj int64) savingsView {
	models := make([]modelRow, 0, len(px.ByModel))
	for m, n := range px.ByModel {
		models = append(models, modelRow{Model: m, Reduced: n})
	}
	sort.Slice(models, func(i, j int) bool {
		if models[i].Reduced != models[j].Reduced {
			return models[i].Reduced > models[j].Reduced
		}
		return models[i].Model < models[j].Model
	})
	return savingsView{Px: px, Ug: ug, Injections: inj, Models: models}
}

func instructionProof() map[string]any {
	state.mu.Lock()
	defer state.mu.Unlock()
	out := map[string]any{
		"edits_this_process": state.injections,
		"caveman_edits":      state.cavemanEdits,
		"ponytail_edits":     state.ponytailEdits,
		"note":               "A body edit means the instruction was inserted. It does not show that the model complied.",
	}
	if state.lastEditModel != "" && !state.lastEditAt.IsZero() {
		out["last"] = map[string]any{
			"model":    state.lastEditModel,
			"at":       state.lastEditAt.UTC().Format(time.RFC3339),
			"caveman":  state.lastEditCaveman,
			"ponytail": state.lastEditPonytail,
		}
	}
	return out
}

func humanize(n int64) string {
	switch {
	case n >= 1_000_000:
		return fmt.Sprintf("%.1fM", float64(n)/1_000_000)
	case n >= 1_000:
		return fmt.Sprintf("%.1fk", float64(n)/1_000)
	default:
		return fmt.Sprintf("%d", n)
	}
}

// ---------------------------------------------------------------------------
// envelope helpers

type envelope struct {
	OK     bool            `json:"ok"`
	Result json.RawMessage `json:"result,omitempty"`
	Error  *envelopeError  `json:"error,omitempty"`
}

type envelopeError struct {
	Code       string `json:"code"`
	Message    string `json:"message"`
	Retryable  bool   `json:"retryable,omitempty"`
	HTTPStatus int    `json:"http_status,omitempty"`
}

func okEnvelope(v any) ([]byte, error) {
	raw, err := json.Marshal(v)
	if err != nil {
		return nil, err
	}
	return json.Marshal(envelope{OK: true, Result: raw})
}

func okEnvelopeJSON(result string) ([]byte, error) {
	return json.Marshal(envelope{OK: true, Result: json.RawMessage(result)})
}

func errorEnvelope(code, message string) []byte {
	raw, _ := json.Marshal(envelope{OK: false, Error: &envelopeError{Code: code, Message: message}})
	return raw
}

func errorEnvelopeStatus(code, message string, status int, retryable bool) []byte {
	raw, _ := json.Marshal(envelope{OK: false, Error: &envelopeError{
		Code: code, Message: message, HTTPStatus: status, Retryable: retryable,
	}})
	return raw
}

func writeResponse(response *C.cliproxy_buffer, raw []byte) {
	if response == nil || len(raw) == 0 {
		return
	}
	ptr := C.CBytes(raw)
	if ptr == nil {
		return
	}
	response.ptr = ptr
	response.len = C.size_t(len(raw))
}
