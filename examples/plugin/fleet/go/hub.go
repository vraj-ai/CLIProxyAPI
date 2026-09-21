package main

// hub.go: the fleet control plane. One page, one state endpoint, and the
// write relays that need a server-side hop (pxpipe's dashboard API is not
// CORS-enabled, so the plugin relays through host.http.do).
//
// State the plugin owns directly: router decisions/readiness, plugin flags,
// pxpipe scope (config.json + live), quota (openusage), provider inventory
// (config.yaml + auth files, metadata only). Writes the plugin cannot do
// itself (provider toggles, its own config) stay on the key-gated core
// management API — the page calls those with the user's management key.

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	pluginapi "github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
	"gopkg.in/yaml.v3"
)

const (
	pxpipeProxyURL   = "http://127.0.0.1:47821"
	pxpipeConfigPath = ".config/pxpipe/config.json"
)

// ---------------------------------------------------------------------------
// state

type hubProvider struct {
	Name     string   `json:"name"`
	Kind     string   `json:"kind"` // "oauth" | "compat" | "plugin"
	Disabled bool     `json:"disabled"`
	Models   []string `json:"models"`
	Detail   string   `json:"detail,omitempty"`
	PatchKey string   `json:"patch_key,omitempty"`
}

type hubQuotaResource struct {
	Name      string  `json:"name"`
	Kind      string  `json:"kind"`
	Remaining float64 `json:"remaining"`
	Limit     float64 `json:"limit"`
	UsedFrac  float64 `json:"used_frac"`
	PacedFrac float64 `json:"elapsed_frac"`
	Paced     bool    `json:"paced"`
	ResetsAt  string  `json:"resets_at,omitempty"`
}

type hubQuotaProvider struct {
	Name      string             `json:"name"`
	Plan      string             `json:"plan"`
	Stale     bool               `json:"stale"`
	Resources []hubQuotaResource `json:"resources"`
}

type hubRouter struct {
	Enabled    bool            `json:"enabled"`
	Eligible   string          `json:"eligible,omitempty"`
	Decisions  []routeDecision `json:"decisions"`
	Candidates []candidateView `json:"candidates"`
}

type candidateView struct {
	Label  string `json:"label"`
	Model  string `json:"model"`
	Effort string `json:"effort"`
	OK     bool   `json:"ok"`
	Reason string `json:"reason,omitempty"`
}

// servedModels calls the proxy's own /v1/models through the host bridge —
// the authoritative "what's served right now" list, already reflecting
// disabled auths and compat entries.
func servedModels(cfg hubConfig) []string {
	if len(cfg.APIKeys) == 0 {
		return nil
	}
	resp, err := hostHTTPTimeout(pluginapi.HTTPRequest{
		Method:  http.MethodGet,
		URL:     "http://127.0.0.1:8317/v1/models",
		Headers: http.Header{"authorization": []string{"Bearer " + cfg.APIKeys[0]}},
	}, 3*time.Second)
	if err != nil || resp == nil || resp.StatusCode != http.StatusOK {
		return nil
	}
	var doc struct {
		Data []struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	if json.Unmarshal(resp.Body, &doc) != nil {
		return nil
	}
	out := make([]string, 0, len(doc.Data))
	for _, m := range doc.Data {
		if m.ID != "" {
			out = append(out, m.ID)
		}
	}
	return out
}

type hubPxpipe struct {
	ProxyURL    string   `json:"proxy_url"`
	Reachable   bool     `json:"reachable"`
	Compressing bool     `json:"compressing"`
	Scope       []string `json:"scope"`
	ScopeSource string   `json:"scope_source"` // "config.json" | "env" | "default"
	ShimURL     string   `json:"shim_url"`
	ShimEnabled bool     `json:"shim_enabled"`
	ShimUp      bool     `json:"shim_up"`
}

type hubFlags struct {
	Caveman  bool `json:"caveman"`
	Ponytail bool `json:"ponytail"`
	Router   bool `json:"router"`
	Pxpipe   bool `json:"pxpipe"`
}

type hubState struct {
	GeneratedAt string             `json:"generated_at"`
	Link        string             `json:"link"`
	Flags       hubFlags           `json:"flags"`
	Providers   []hubProvider      `json:"providers"`
	Models      []string           `json:"models"`
	Quota       []hubQuotaProvider `json:"quota"`
	Router      hubRouter          `json:"router"`
	Pxpipe      hubPxpipe          `json:"pxpipe"`
	Features    hubFeatureState    `json:"features"`
	Keys        []publicGrant      `json:"keys"`
}

// hubConfig mirrors the config.yaml fields the hub reads. apiKeys is consumed
// server-side only to call the proxy's own /v1/models — it is never copied
// into hubState or any response body.
type hubConfig struct {
	APIKeys       []string            `yaml:"api-keys"`
	OAuthExcluded map[string][]string `yaml:"oauth-excluded-models"`
	Compat        []struct {
		Name     string `yaml:"name"`
		Prefix   string `yaml:"prefix"`
		Disabled bool   `yaml:"disabled"`
		Models   []struct {
			Name  string `yaml:"name"`
			Alias string `yaml:"alias"`
		} `yaml:"models"`
	} `yaml:"openai-compatibility"`
}

func loadHubConfig() hubConfig {
	var out hubConfig
	home, _ := os.UserHomeDir()
	raw, err := os.ReadFile(filepath.Join(home, ".cli-proxy-api", "config.yaml"))
	if err != nil {
		return out
	}
	_ = yaml.Unmarshal(raw, &out)
	return out
}

// authFileView is the read-only view of a file-backed auth. Only the provider
// type, model ids, and disabled flag are extracted — credential material
// never leaves the file. File is the on-disk name, the PATCH key for
// /auth-files/status.
type authFileView struct {
	File     string `json:"-"`
	Provider string `json:"type"`
	Disabled bool   `json:"disabled"`
	Models   []struct {
		ID string `json:"id"`
	} `json:"models"`
}

func listAuthFiles() []authFileView {
	home, _ := os.UserHomeDir()
	dir := filepath.Join(home, ".cli-proxy-api")
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	var out []authFileView
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".json") || e.Name() == "fleet-router.json" {
			continue
		}
		raw, err := os.ReadFile(filepath.Join(dir, e.Name()))
		if err != nil {
			continue
		}
		var v authFileView
		if json.Unmarshal(raw, &v) != nil || v.Provider == "" {
			continue
		}
		v.File = e.Name()
		out = append(out, v)
	}
	return out
}

func maskAuthFile(name string) string {
	at := strings.Index(name, "@")
	if at >= 2 {
		return name[:2] + "…@" + name[at+1:]
	}
	if strings.HasSuffix(name, ".json") {
		return strings.TrimSuffix(name, ".json")
	}
	return name
}

func buildProviders(cfg hubConfig, auths []authFileView, routerEnabled bool) []hubProvider {
	var out []hubProvider
	excludedAll := map[string]bool{}
	for p, pats := range cfg.OAuthExcluded {
		for _, pat := range pats {
			if strings.TrimSpace(pat) == "*" {
				excludedAll[p] = true
			}
		}
	}
	for _, a := range auths {
		models := make([]string, 0, len(a.Models))
		for _, m := range a.Models {
			if m.ID != "" {
				models = append(models, m.ID)
			}
		}
		out = append(out, hubProvider{
			Name: a.Provider, Kind: "oauth",
			Disabled: a.Disabled || excludedAll[a.Provider],
			Models:   models, Detail: maskAuthFile(a.File), PatchKey: a.File,
		})
	}
	for _, c := range cfg.Compat {
		models := make([]string, 0, len(c.Models))
		for _, m := range c.Models {
			id := m.Alias
			if id == "" {
				id = m.Name
			}
			if c.Prefix != "" {
				id = c.Prefix + "/" + id
			}
			models = append(models, id)
		}
		out = append(out, hubProvider{
			Name: c.Name, Kind: "compat", Disabled: c.Disabled,
			Models: models, Detail: "prefix " + c.Prefix,
		})
	}
	out = append(out, hubProvider{
		Name: routerProvider, Kind: "plugin", Disabled: !routerEnabled,
		Models: []string{virtualRouterModel}, Detail: "herdr-orchestrated virtual model",
	})
	return out
}

// quotaCached serves the dashboard: a 15s TTL on the --force fetch keeps a
// polling page from hammering openusage's live refresh. Routing decisions
// still fetch fresh per request.
func quotaCached() (map[string]providerQuota, error) {
	now := nowFunc()
	state.mu.Lock()
	age := now.Sub(state.quotaCache.at)
	if !state.quotaCache.at.IsZero() && age >= 0 && age < 15*time.Second {
		snap, err := state.quotaCache.snap, state.quotaCache.err
		state.mu.Unlock()
		return snap, err
	}
	if state.quotaCache.fetching {
		snap, err := state.quotaCache.snap, state.quotaCache.err
		state.mu.Unlock()
		return snap, err
	}
	src := state.quota
	state.quotaCache.fetching = true
	state.mu.Unlock()
	defer func() {
		state.mu.Lock()
		state.quotaCache.fetching = false
		state.mu.Unlock()
	}()
	if src == nil {
		src = defaultQuota()
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	snap, err := src.fetch(ctx)
	state.mu.Lock()
	state.quotaCache.at = nowFunc()
	state.quotaCache.snap = snap
	state.quotaCache.err = err
	state.mu.Unlock()
	return snap, err
}

func buildQuota() []hubQuotaProvider {
	snap, err := quotaCached()
	if err != nil {
		return nil
	}
	now := nowFunc()
	out := make([]hubQuotaProvider, 0, len(snap))
	for name, p := range snap {
		qp := hubQuotaProvider{Name: name, Plan: p.Plan, Stale: p.Stale}
		for rn, r := range p.Resources {
			qr := hubQuotaResource{
				Name: rn, Kind: r.Kind, Remaining: r.Remaining, Limit: r.Limit,
			}
			if r.Limit > 0 {
				qr.UsedFrac = 1 - r.Remaining/r.Limit
			}
			if r.WindowSeconds > 0 && !r.ResetsAt.IsZero() {
				left := r.ResetsAt.Sub(now).Seconds()
				if left < 0 {
					left = 0
				}
				lf := left / float64(r.WindowSeconds)
				if lf > 1 {
					lf = 1
				}
				qr.PacedFrac = 1 - lf
				qr.ResetsAt = r.ResetsAt.UTC().Format(time.RFC3339)
				qr.Paced = qr.PacedFrac >= 0.05 && qr.UsedFrac/qr.PacedFrac > 1
			}
			qp.Resources = append(qp.Resources, qr)
		}
		out = append(out, qp)
	}
	return out
}

func buildRouterView(live []herdrAgent) hubRouter {
	state.mu.Lock()
	cfg := state.config
	decisions := append([]routeDecision(nil), state.decisions...)
	state.mu.Unlock()
	view := hubRouter{Enabled: cfg.RouterEnabled, Decisions: decisions}
	if !cfg.RouterEnabled {
		return view
	}
	snap, qerr := quotaCached()
	for _, c := range preferenceOrder {
		cv := candidateView{Label: c.Label, Model: c.Model, Effort: c.Effort}
		var reason string
		if qerr != nil {
			reason = "quota_unavailable"
		} else {
			_, _, reason = candidateGate(c, live, "", snap, nowFunc(), false)
		}
		cv.Reason = reason
		cv.OK = reason == ""
		if cv.OK && view.Eligible == "" {
			view.Eligible = c.Label
		}
		view.Candidates = append(view.Candidates, cv)
	}
	return view
}

func pxpipeScope() ([]string, string) {
	home, _ := os.UserHomeDir()
	raw, err := os.ReadFile(filepath.Join(home, pxpipeConfigPath))
	if err == nil {
		var doc struct {
			Models any `json:"models"`
		}
		if json.Unmarshal(raw, &doc) == nil {
			switch v := doc.Models.(type) {
			case []any:
				out := make([]string, 0, len(v))
				for _, m := range v {
					if s, ok := m.(string); ok && s != "" {
						out = append(out, s)
					}
				}
				return out, "config.json"
			case string:
				if strings.EqualFold(strings.TrimSpace(v), "off") {
					return nil, "config.json"
				}
			}
		}
	}
	if env := strings.TrimSpace(os.Getenv("PXPIPE_MODELS")); env != "" {
		return strings.Split(env, ","), "env"
	}
	return []string{"claude-fable-5"}, "default"
}

var pxpipeScopeMu sync.Mutex

func writePxpipeScope(models []string) error {
	home, _ := os.UserHomeDir()
	path := filepath.Join(home, pxpipeConfigPath)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	raw, err := json.Marshal(map[string]any{"models": models})
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), "pxpipe-config-*.tmp")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	if _, err := tmp.Write(raw); err != nil {
		tmp.Close()
		os.Remove(tmpName)
		return err
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmpName)
		return err
	}
	return os.Rename(tmpName, path)
}

func pxpipeBaseID(id string) string {
	if i := strings.LastIndex(id, "/"); i >= 0 {
		return id[i+1:]
	}
	return id
}

func inPxpipeScope(scope []string, served string) bool {
	base := pxpipeBaseID(served)
	for _, s := range scope {
		if s == served || s == base || pxpipeBaseID(s) == base {
			return true
		}
	}
	return false
}

func pxpipeReachable() bool {
	resp, err := hostHTTPTimeout(pluginapi.HTTPRequest{
		Method: http.MethodGet,
		URL:    pxpipeProxyURL + "/api/stats.json",
	}, 2*time.Second)
	return err == nil && resp != nil && resp.StatusCode == http.StatusOK
}

func shimUp(cfg pluginConfig) bool {
	resp, err := hostHTTPTimeout(pluginapi.HTTPRequest{
		Method: http.MethodGet,
		URL:    strings.TrimRight(cfg.PxpipeURL, "/") + "/health",
	}, 2*time.Second)
	return err == nil && resp != nil && resp.StatusCode == http.StatusOK
}

func buildHubState() hubState {
	cfg := loadHubConfig()
	auths := listAuthFiles()
	scope, scopeSrc := pxpipeScope()
	agentCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	live, _ := defaultHerdr().agents(agentCtx)
	cancel()
	state.mu.Lock()
	pluginCfg := state.config
	state.mu.Unlock()
	models := servedModels(cfg)
	return hubState{
		GeneratedAt: nowFunc().UTC().Format(time.RFC3339),
		Link:        "http://127.0.0.1:8317",
		Flags: hubFlags{
			Caveman:  pluginCfg.Caveman,
			Ponytail: pluginCfg.Ponytail,
			Router:   pluginCfg.RouterEnabled,
			Pxpipe:   pluginCfg.PxpipeEnabled,
		},
		Providers: buildProviders(cfg, auths, pluginCfg.RouterEnabled),
		Models:    models,
		Features:  buildFeatureState(models),
		Quota:     buildQuota(),
		Router:    buildRouterView(live),
		Pxpipe: hubPxpipe{
			ProxyURL:    pxpipeProxyURL,
			Reachable:   pxpipeReachable(),
			Scope:       scope,
			ScopeSource: scopeSrc,
			ShimURL:     pluginCfg.PxpipeURL,
			ShimEnabled: pluginCfg.PxpipeEnabled,
			ShimUp:      shimUp(pluginCfg),
		},
		Keys: publicGrantsForHub(),
	}
}

// ---------------------------------------------------------------------------
// handlers

func jsonResp(v any, code int) ([]byte, error) {
	raw, err := json.Marshal(v)
	if err != nil {
		return nil, err
	}
	return okEnvelope(pluginapi.ManagementResponse{
		StatusCode: code,
		Headers:    http.Header{"Content-Type": []string{"application/json"}},
		Body:       raw,
	})
}

func hubStateHandler() ([]byte, error) {
	return jsonResp(buildHubState(), http.StatusOK)
}

// pxpipeScopeSet persists the scope to config.json and pushes each delta to
// the live pxpipe dashboard API so the change applies without a restart.
func pxpipeScopeSet(raw []byte) ([]byte, error) {
	var req struct {
		Models []string `json:"models"`
	}
	if err := json.Unmarshal(raw, &req); err != nil {
		return jsonResp(map[string]string{"error": "invalid_body"}, http.StatusBadRequest)
	}
	pxpipeScopeMu.Lock()
	defer pxpipeScopeMu.Unlock()
	before, _ := pxpipeScope()
	if err := writePxpipeScope(req.Models); err != nil {
		return jsonResp(map[string]string{"error": err.Error()}, http.StatusInternalServerError)
	}
	want := map[string]bool{}
	for _, m := range req.Models {
		want[m] = true
	}
	var pushErrs []string
	for _, m := range before {
		if !want[m] {
			if err := pushPxpipeModel(m, false); err != nil {
				pushErrs = append(pushErrs, m)
			}
		}
	}
	for _, m := range req.Models {
		if err := pushPxpipeModel(m, true); err != nil {
			pushErrs = append(pushErrs, m)
		}
	}
	out := map[string]any{"scope": req.Models, "persisted": true}
	if len(pushErrs) > 0 {
		out["live_push_failed"] = pushErrs
	}
	return jsonResp(out, http.StatusOK)
}

// pxpipeScopeToggle flips one model in the scope — the page's per-chip path.
func pxpipeScopeToggle(raw []byte) ([]byte, error) {
	var req struct {
		Model string `json:"model"`
		On    bool   `json:"on"`
	}
	if err := json.Unmarshal(raw, &req); err != nil || strings.TrimSpace(req.Model) == "" {
		return jsonResp(map[string]string{"error": "invalid_body"}, http.StatusBadRequest)
	}
	pxpipeScopeMu.Lock()
	defer pxpipeScopeMu.Unlock()
	scope, _ := pxpipeScope()
	set := map[string]bool{}
	for _, m := range scope {
		set[pxpipeBaseID(m)] = true
	}
	key := pxpipeBaseID(req.Model)
	if req.On {
		set[key] = true
	} else {
		delete(set, key)
	}
	next := make([]string, 0, len(set))
	for m := range set {
		next = append(next, m)
	}
	if err := writePxpipeScope(next); err != nil {
		return jsonResp(map[string]string{"error": err.Error()}, http.StatusInternalServerError)
	}
	out := map[string]any{"scope": next, "persisted": true}
	if err := pushPxpipeModel(key, req.On); err != nil {
		out["live_push_failed"] = []string{key}
	}
	return jsonResp(out, http.StatusOK)
}

func pushPxpipeModel(model string, on bool) error {
	body, _ := json.Marshal(map[string]any{"model": model, "on": on})
	resp, err := hostHTTP(pluginapi.HTTPRequest{
		Method:  http.MethodPost,
		URL:     pxpipeProxyURL + "/fragments/models",
		Headers: http.Header{"content-type": []string{"application/json"}},
		Body:    body,
	})
	if err != nil || resp == nil {
		return fmt.Errorf("pxpipe unreachable")
	}
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return fmt.Errorf("pxpipe status %d", resp.StatusCode)
	}
	return nil
}

func htmlPage(body string) ([]byte, error) {
	return okEnvelope(pluginapi.ManagementResponse{
		StatusCode: http.StatusOK,
		Headers:    http.Header{"Content-Type": []string{"text/html; charset=utf-8"}},
		Body:       []byte(body),
	})
}

func fleetRelPath(path string) (kind, rel string) {
	path = strings.TrimRight(path, "/")
	const resourcePrefix = "/v0/resource/plugins/fleet"
	const managementPrefix = "/v0/management"
	switch {
	case path == resourcePrefix:
		return "resource", "/"
	case strings.HasPrefix(path, resourcePrefix+"/"):
		return "resource", strings.TrimPrefix(path, resourcePrefix)
	case strings.HasPrefix(path, managementPrefix+"/"):
		return "management", strings.TrimPrefix(path, managementPrefix)
	case path == "/hub" || path == "/savings" || path == "/router" || path == "/keys":
		return "resource", path
	case strings.HasPrefix(path, "/fleet/"):
		return "management", path
	default:
		return "", path
	}
}

// managementDispatch routes resource HTML shells (unauthenticated) and
// management JSON/writes (key-gated by the host) by exact registered path.
func managementDispatch(req *pluginapi.ManagementRequest, raw []byte) ([]byte, error) {
	kind, rel := fleetRelPath(req.Path)
	switch {
	case kind == "resource" && rel == "/hub":
		return htmlPage(withTheme(hubPageHTML))
	case kind == "resource" && rel == "/savings":
		return htmlPage(withTheme(savingsPageHTML))
	case kind == "resource" && rel == "/router":
		return htmlPage(withTheme(routerPageHTML))
	case kind == "resource" && rel == "/keys":
		return htmlPage(withTheme(keysPageHTML))
	case kind == "management" && rel == "/fleet/state":
		return hubStateHandler()
	case kind == "management" && rel == "/fleet/savings":
		return savingsJSON()
	case kind == "management" && rel == "/fleet/router":
		return routerDiagnostics()
	case kind == "management" && rel == "/fleet/pxpipe/scope/toggle" && req.Method == http.MethodPost:
		return pxpipeScopeToggle(req.Body)
	case kind == "management" && rel == "/fleet/pxpipe/scope" && req.Method == http.MethodPost:
		return pxpipeScopeSet(req.Body)
	case kind == "management" && rel == "/fleet/pxpipe/compression" && req.Method == http.MethodPost:
		return pxpipeCompression(req.Body)
	case kind == "management" && rel == "/fleet/features" && req.Method == http.MethodPost:
		return setModelFeature(req.Body)
	case kind == "management" && rel == "/fleet/keys" && req.Method == http.MethodGet:
		return keysListHandler(req)
	case kind == "management" && rel == "/fleet/keys" && req.Method == http.MethodPost:
		return keysCreateHandler(req)
	case kind == "management" && rel == "/fleet/keys" && req.Method == http.MethodPatch:
		return keysPatchHandler(req)
	case kind == "management" && rel == "/fleet/keys" && req.Method == http.MethodDelete:
		return keysDeleteHandler(req)
	case kind == "management" && rel == "/fleet/keys/adopt" && req.Method == http.MethodPost:
		return keysAdoptHandler(req)
	}
	return errorEnvelope("not_found", "no fleet handler for "+req.Method+" "+req.Path), nil
}

func pxpipeCompression(raw []byte) ([]byte, error) {
	var req struct {
		Enabled bool `json:"enabled"`
	}
	if err := json.Unmarshal(raw, &req); err != nil {
		return jsonResp(map[string]string{"error": "invalid_body"}, http.StatusBadRequest)
	}
	resp, err := hostHTTP(pluginapi.HTTPRequest{
		Method:  http.MethodPost,
		URL:     pxpipeProxyURL + "/api/compression",
		Headers: http.Header{"content-type": []string{"application/json"}},
		Body:    []byte(fmt.Sprintf(`{"enabled":%t}`, req.Enabled)),
	})
	if err != nil || resp == nil {
		return jsonResp(map[string]string{"error": "pxpipe unreachable"}, http.StatusBadGateway)
	}
	return jsonResp(map[string]any{"compression_enabled": req.Enabled, "upstream_status": resp.StatusCode}, http.StatusOK)
}
