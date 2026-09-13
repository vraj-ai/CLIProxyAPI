// fleet plugin: injects caveman + ponytail instructions into every routed
// request, and exposes a /savings management resource aggregating pxpipe and
// pi-subagents compression telemetry.
package main

/*
#include <stdint.h>
#include <stdlib.h>

typedef struct {
	void* ptr;
	size_t len;
} cliproxy_buffer;

typedef int (*cliproxy_host_call_fn)(void*, const char*, const uint8_t*, size_t, cliproxy_buffer*);
typedef void (*cliproxy_host_free_fn)(void*, size_t);

typedef struct {
	uint32_t abi_version;
	void* host_ctx;
	cliproxy_host_call_fn call;
	cliproxy_host_free_fn free_buffer;
} cliproxy_host_api;

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

static const cliproxy_host_api* stored_host;

static void store_host_api(const cliproxy_host_api* host) {
	stored_host = host;
}
*/
import "C"

import (
	"bufio"
	"encoding/json"
	"fmt"
	"html"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
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
	C.store_host_api(host)
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
	Caveman         bool     `yaml:"caveman"`
	Ponytail        bool     `yaml:"ponytail"`
	Models          []string `yaml:"models"`            // model prefixes to inject; empty = all
	PxpipeEvents    string   `yaml:"pxpipe_events"`     // events.jsonl path
	UsageGain       string   `yaml:"usage_gain"`        // pi usage-gain.jsonl path
	RouterEnabled   bool     `yaml:"router_enabled"`    // expose the "cpa router" virtual model
	RouterAgent     string   `yaml:"router_agent"`      // herdr agent selector used as lead
	RouterTimeoutMS int      `yaml:"router_timeout_ms"` // bound on the lead wait
	RouterReadLines int      `yaml:"router_read_lines"` // read-back window for marker extraction
}

var state = struct {
	mu         sync.Mutex
	config     pluginConfig
	injections int64
	backend    leadBackend
	quota      quotaSource
	decisions  []routeDecision
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
	state.mu.Lock()
	state.config = cfg
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

func handleMethod(method string, request []byte) ([]byte, error) {
	switch method {
	case pluginabi.MethodPluginRegister, pluginabi.MethodPluginReconfigure:
		if err := configure(request); err != nil {
			return nil, err
		}
		return okEnvelope(buildRegistration())
	case pluginabi.MethodRequestInterceptBefore:
		return intercept(request)
	case pluginabi.MethodRequestInterceptAfter:
		// body may already be translated here; injection happens once, before
		var req pluginapi.RequestInterceptRequest
		if err := json.Unmarshal(request, &req); err != nil {
			return nil, err
		}
		return okEnvelope(pluginapi.RequestInterceptResponse{Headers: req.Headers, Body: req.Body})
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
	case "management.register":
		return okEnvelopeJSON(`{"resources":[{"Path":"/savings","Menu":"Fleet","Description":"pxpipe + subagent compression savings aggregated from local telemetry."}]}`)
	case "management.handle":
		return savings(request)
	default:
		return errorEnvelope("unknown_method", "unknown method: "+method), nil
	}
}

type registrationCapability struct {
	RequestInterceptor    bool     `json:"request_interceptor"`
	ManagementAPI         bool     `json:"management_api"`
	ModelRegistrar        bool     `json:"model_registrar"`
	Executor              bool     `json:"executor"`
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
			},
		},
		Capabilities: registrationCapability{
			RequestInterceptor:    true,
			ManagementAPI:         true,
			ModelRegistrar:        true,
			Executor:              true,
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
	state.mu.Lock()
	cfg := state.config
	state.mu.Unlock()
	body := req.Body
	if modelAllowed(req.Model, cfg.Models) || modelAllowed(req.RequestedModel, cfg.Models) {
		if injected, ok := inject(instructionText(cfg), req.SourceFormat, req.Body); ok {
			body = injected
			state.mu.Lock()
			state.injections++
			state.mu.Unlock()
		}
	}
	return okEnvelope(pluginapi.RequestInterceptResponse{Headers: req.Headers, Body: body})
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

type pxStats struct {
	Requests     int64            `json:"requests"`
	Compressed   int64            `json:"compressed"`
	CharsBefore  int64            `json:"chars_before"`
	CharsAfter   int64            `json:"chars_after"`
	SavedPct     float64          `json:"saved_pct"`
	InputTokens  int64            `json:"input_tokens"`
	OutputTokens int64            `json:"output_tokens"`
	ByModel      map[string]int64 `json:"saved_chars_by_model"`
}

type ugStats struct {
	Events   int64   `json:"events"`
	Before   int64   `json:"bytes_before"`
	After    int64   `json:"bytes_after"`
	SavedPct float64 `json:"saved_pct"`
}

func savings(raw []byte) ([]byte, error) {
	state.mu.Lock()
	cfg := state.config
	inj := state.injections
	state.mu.Unlock()

	var req pluginapi.ManagementRequest
	_ = json.Unmarshal(raw, &req)

	px := pxStats{ByModel: map[string]int64{}}

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
			return nil, fmt.Errorf("telemetry read failed; measurements unavailable")
		}
	} else {
		return nil, fmt.Errorf("telemetry unavailable; measurements not reported")
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
			return nil, fmt.Errorf("telemetry read failed; measurements unavailable")
		}
	} else {
		return nil, fmt.Errorf("telemetry unavailable; measurements not reported")
	}
	if ug.Before > 0 {
		ug.SavedPct = float64(ug.Before-ug.After) / float64(ug.Before) * 100
	}

	payload := map[string]any{
		"pxpipe":                  px,
		"subagent_context":        ug,
		"injections_this_process": inj,
		"note":                    "Historical local logs, not attributed to this proxy route. Character/byte reductions exclude image token cost and do not measure token or dollar savings. Injection counts record body edits, not model compliance or savings.",
	}
	if req.Query.Get("format") == "json" {
		out, _ := json.Marshal(payload)
		return okEnvelope(pluginapi.ManagementResponse{
			StatusCode: http.StatusOK,
			Headers:    http.Header{"content-type": {"application/json"}},
			Body:       out,
		})
	}
	return okEnvelope(pluginapi.ManagementResponse{
		StatusCode: http.StatusOK,
		Headers:    http.Header{"content-type": {"text/html; charset=utf-8"}},
		Body:       []byte(savingsHTML(px, ug, inj)),
	})
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

// savingsHTML renders the Fleet dashboard: stat cards for each measured
// layer, the per-model table, and pxpipe's own dashboard embedded live.
func savingsHTML(px pxStats, ug ugStats, inj int64) string {
	v := newSavingsView(px, ug, inj)

	var rows strings.Builder
	if len(v.Models) == 0 {
		rows.WriteString(`<tr><td colspan="2" class="empty">no compressed requests recorded</td></tr>`)
	}
	for _, m := range v.Models {
		fmt.Fprintf(&rows, `<tr><td>%s</td><td class="num">%s</td></tr>`, html.EscapeString(m.Model), humanize(m.Reduced))
	}

	pxClass := "big"
	if v.Px.SavedPct < 0 {
		pxClass = "big warn"
	}
	ugClass := "big"
	if v.Ug.SavedPct < 0 {
		ugClass = "big warn"
	}

	return fmt.Sprintf(`<!doctype html><html><head><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1">
<title>Fleet — savings</title>
<style>
  :root { --bg:#0d1117; --panel:#161b22; --line:#30363d; --text:#e6edf3; --muted:#8b949e; --accent:#3fb950; --warn:#d29922; --link:#58a6ff; }
  * { box-sizing:border-box; }
  body { background:var(--bg); color:var(--text); font:14px/1.5 -apple-system,system-ui,sans-serif; margin:0; }
  .top { max-width:1100px; margin:0 auto; padding:20px 24px 32px; }
  header { display:flex; align-items:baseline; justify-content:space-between; gap:16px; flex-wrap:wrap; }
  h1 { font-size:18px; margin:0; font-weight:600; }
  nav a { color:var(--link); font-size:13px; margin-left:14px; text-decoration:none; }
  nav a:hover { text-decoration:underline; }
  a:focus-visible { outline:2px solid var(--link); outline-offset:2px; }
  .note { color:var(--muted); font-size:12px; margin:6px 0 0; max-width:72ch; }
  .grid { display:grid; grid-template-columns:repeat(auto-fit,minmax(230px,1fr)); gap:12px; margin:18px 0 12px; }
  .card { background:var(--panel); border:1px solid var(--line); border-radius:10px; padding:14px 16px; }
  .card h2, .live h2 { font-size:11px; letter-spacing:.08em; text-transform:uppercase; color:var(--muted); margin:0 0 8px; font-weight:600; }
  .big { font-size:26px; font-weight:700; color:var(--accent); font-variant-numeric:tabular-nums; }
  .big.warn { color:var(--warn); }
  .meta { font-size:12px; color:var(--muted); margin-top:4px; }
  table { width:100%%; font-size:13px; border-collapse:collapse; }
  th { font-size:11px; text-transform:uppercase; letter-spacing:.08em; color:var(--muted); text-align:left; padding:0 8px 6px 0; font-weight:600; }
  td { padding:5px 8px 5px 0; border-top:1px solid var(--line); }
  td.num, th.num { text-align:right; font-variant-numeric:tabular-nums; }
  td.num { color:var(--accent); }
  .empty { color:var(--muted); }
  .live { margin-top:18px; }
  .live h2 a { color:var(--link); text-transform:none; letter-spacing:0; }
  iframe { display:block; width:100%%; height:62vh; min-height:420px; border:1px solid var(--line); border-radius:10px; background:#0d1117; }
</style></head><body>
<div class="top">
<header>
  <h1>Fleet — savings</h1>
  <nav><a href="http://127.0.0.1:47821/" target="_blank" rel="noopener noreferrer">Open pxpipe ↗</a><a href="?format=json">JSON</a></nav>
</header>
<p class="note">Historical local telemetry, not attributed to this route. Text/byte reduction is not token or dollar savings; image token cost is excluded.</p>
<div class="grid">
  <div class="card"><h2>pxpipe compression</h2>
    <div class="%s">%.1f%%</div>
    <div class="meta">text chars reduced · %s requests · %s compressed · %s → %s</div>
  </div>
  <div class="card"><h2>subagent context</h2>
    <div class="%s">%.1f%%</div>
    <div class="meta">context bytes reduced · %d events · %s → %s</div>
  </div>
  <div class="card"><h2>instruction injection</h2>
    <div class="big">%d</div>
    <div class="meta">caveman + ponytail body edits this process · not model compliance or savings</div>
  </div>
</div>
<div class="card">
  <h2>text characters reduced by model</h2>
  <table><tr><th>model</th><th class="num">chars reduced</th></tr>%s</table>
</div>
<div class="live">
  <h2>pxpipe — live dashboard · <a href="http://127.0.0.1:47821/" target="_blank" rel="noopener noreferrer">open in tab ↗</a></h2>
  <iframe src="http://127.0.0.1:47821/" title="pxpipe live dashboard" loading="lazy"></iframe>
</div>
</div>
</body></html>`,
		pxClass, v.Px.SavedPct, humanize(v.Px.Requests), humanize(v.Px.Compressed), humanize(v.Px.CharsBefore), humanize(v.Px.CharsAfter),
		ugClass, v.Ug.SavedPct, v.Ug.Events, humanize(v.Ug.Before), humanize(v.Ug.After),
		v.Injections,
		rows.String())
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
