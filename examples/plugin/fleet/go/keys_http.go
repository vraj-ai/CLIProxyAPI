package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	pluginapi "github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
)

var listProxyKeys = func(h http.Header) ([]string, error) {
	resp, err := hostHTTP(pluginapi.HTTPRequest{
		Method:  http.MethodGet,
		URL:     "http://127.0.0.1:8317/v0/management/api-keys",
		Headers: h,
	})
	if err != nil || resp == nil {
		return nil, fmt.Errorf("proxy api-keys unreachable")
	}
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return nil, fmt.Errorf("proxy api-keys status %d", resp.StatusCode)
	}
	var wrap struct {
		Keys []string `json:"api-keys"`
	}
	if json.Unmarshal(resp.Body, &wrap) != nil {
		return nil, fmt.Errorf("proxy api-keys decode")
	}
	return wrap.Keys, nil
}

var putProxyKeys = func(h http.Header, keys []string) error {
	body, err := json.Marshal(keys)
	if err != nil {
		return err
	}
	resp, err := hostHTTP(pluginapi.HTTPRequest{
		Method:  http.MethodPut,
		URL:     "http://127.0.0.1:8317/v0/management/api-keys",
		Headers: withJSON(h),
		Body:    body,
	})
	if err != nil || resp == nil {
		return fmt.Errorf("proxy api-keys write unreachable")
	}
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return fmt.Errorf("proxy api-keys write status %d", resp.StatusCode)
	}
	return nil
}

func withJSON(h http.Header) http.Header {
	out := h.Clone()
	if out == nil {
		out = http.Header{}
	}
	out.Set("Content-Type", "application/json")
	return out
}

func keysListHandler(req *pluginapi.ManagementRequest) ([]byte, error) {
	grantsMu.Lock()
	store, err := loadGrantStore()
	grantsMu.Unlock()
	if err != nil {
		return jsonResp(map[string]string{"error": "grant_store_unreadable"}, http.StatusInternalServerError)
	}
	proxyKeys, err := listProxyKeys(req.Headers)
	if err != nil {
		return jsonResp(map[string]string{"error": err.Error()}, http.StatusBadGateway)
	}
	live := hashSet(proxyKeys)
	out := make([]publicGrant, 0, len(store.Grants))
	granted := map[string]struct{}{}
	for _, g := range store.Grants {
		out = append(out, publicFromGrant(g, live))
		granted[g.KeyHash] = struct{}{}
	}
	var legacy []legacyKeyView
	for i, k := range proxyKeys {
		h := hashSecret(strings.TrimSpace(k))
		if _, ok := granted[h]; ok {
			continue
		}
		legacy = append(legacy, legacyKeyView{Index: i, Prefix: displayPrefix(k)})
	}
	if legacy == nil {
		legacy = []legacyKeyView{}
	}
	return jsonResp(map[string]any{"keys": out, "legacy": legacy}, http.StatusOK)
}

func keysCreateHandler(req *pluginapi.ManagementRequest) ([]byte, error) {
	var body struct {
		Name   string   `json:"name"`
		Mode   string   `json:"mode"`
		Models []string `json:"models"`
	}
	if len(req.Body) > 0 && json.Unmarshal(req.Body, &body) != nil {
		return jsonResp(map[string]string{"error": "invalid_body"}, http.StatusBadRequest)
	}
	secret, err := generateSecret()
	if err != nil {
		return jsonResp(map[string]string{"error": "secret_failed"}, http.StatusInternalServerError)
	}
	id, err := generateGrantID()
	if err != nil {
		return jsonResp(map[string]string{"error": "id_failed"}, http.StatusInternalServerError)
	}
	g, err := normalizeGrant(fleetKeyGrant{
		ID:        id,
		Name:      body.Name,
		Prefix:    displayPrefix(secret),
		KeyHash:   hashSecret(secret),
		Mode:      body.Mode,
		Models:    body.Models,
		CreatedAt: nowFunc().UTC(),
	})
	if err != nil {
		return jsonResp(map[string]string{"error": err.Error()}, http.StatusBadRequest)
	}
	if g.Name == "" {
		g.Name = "untitled"
	}
	grantsMu.Lock()
	store, err := loadGrantStore()
	if err != nil {
		grantsMu.Unlock()
		return jsonResp(map[string]string{"error": "grant_store_unreadable"}, http.StatusInternalServerError)
	}
	store.Grants = append(store.Grants, g)
	if err := saveGrantStore(store); err != nil {
		grantsMu.Unlock()
		return jsonResp(map[string]string{"error": "grant_store_write"}, http.StatusInternalServerError)
	}
	grantsMu.Unlock()

	proxyKeys, err := listProxyKeys(req.Headers)
	if err != nil {
		_ = deleteGrant(g.ID)
		return jsonResp(map[string]string{"error": err.Error()}, http.StatusBadGateway)
	}
	next := append(append([]string{}, proxyKeys...), secret)
	if err := putProxyKeys(req.Headers, next); err != nil {
		_ = deleteGrant(g.ID)
		return jsonResp(map[string]string{"error": err.Error()}, http.StatusBadGateway)
	}
	live := hashSet(next)
	return jsonResp(map[string]any{
		"key":    publicFromGrant(g, live),
		"secret": secret,
	}, http.StatusOK)
}

func keysPatchHandler(req *pluginapi.ManagementRequest) ([]byte, error) {
	var body struct {
		ID       string   `json:"id"`
		Name     *string  `json:"name"`
		Mode     *string  `json:"mode"`
		Models   []string `json:"models"`
		Disabled *bool    `json:"disabled"`
	}
	if json.Unmarshal(req.Body, &body) != nil || strings.TrimSpace(body.ID) == "" {
		return jsonResp(map[string]string{"error": "invalid_body"}, http.StatusBadRequest)
	}
	grantsMu.Lock()
	defer grantsMu.Unlock()
	store, err := loadGrantStore()
	if err != nil {
		return jsonResp(map[string]string{"error": "grant_store_unreadable"}, http.StatusInternalServerError)
	}
	idx := -1
	for i := range store.Grants {
		if store.Grants[i].ID == body.ID {
			idx = i
			break
		}
	}
	if idx < 0 {
		return jsonResp(map[string]string{"error": "not_found"}, http.StatusNotFound)
	}
	g := store.Grants[idx]
	if body.Name != nil {
		g.Name = *body.Name
	}
	if body.Mode != nil {
		g.Mode = *body.Mode
	}
	if body.Models != nil {
		g.Models = body.Models
	}
	if body.Disabled != nil {
		g.Disabled = *body.Disabled
	}
	g, err = normalizeGrant(g)
	if err != nil {
		return jsonResp(map[string]string{"error": err.Error()}, http.StatusBadRequest)
	}
	store.Grants[idx] = g
	if err := saveGrantStore(store); err != nil {
		return jsonResp(map[string]string{"error": "grant_store_write"}, http.StatusInternalServerError)
	}
	proxyKeys, _ := listProxyKeys(req.Headers)
	return jsonResp(map[string]any{"key": publicFromGrant(g, hashSet(proxyKeys))}, http.StatusOK)
}

func keysDeleteHandler(req *pluginapi.ManagementRequest) ([]byte, error) {
	id := strings.TrimSpace(req.Query.Get("id"))
	if id == "" {
		var body struct {
			ID string `json:"id"`
		}
		_ = json.Unmarshal(req.Body, &body)
		id = strings.TrimSpace(body.ID)
	}
	if id == "" {
		return jsonResp(map[string]string{"error": "missing_id"}, http.StatusBadRequest)
	}
	grantsMu.Lock()
	store, err := loadGrantStore()
	if err != nil {
		grantsMu.Unlock()
		return jsonResp(map[string]string{"error": "grant_store_unreadable"}, http.StatusInternalServerError)
	}
	var target *fleetKeyGrant
	for i := range store.Grants {
		if store.Grants[i].ID == id {
			g := store.Grants[i]
			target = &g
			break
		}
	}
	grantsMu.Unlock()
	if target == nil {
		return jsonResp(map[string]string{"error": "not_found"}, http.StatusNotFound)
	}
	proxyKeys, err := listProxyKeys(req.Headers)
	if err != nil {
		return jsonResp(map[string]string{"error": err.Error()}, http.StatusBadGateway)
	}
	next := make([]string, 0, len(proxyKeys))
	for _, k := range proxyKeys {
		if hashSecret(strings.TrimSpace(k)) == target.KeyHash {
			continue
		}
		next = append(next, k)
	}
	if err := putProxyKeys(req.Headers, next); err != nil {
		return jsonResp(map[string]string{"error": err.Error()}, http.StatusBadGateway)
	}
	if err := deleteGrant(id); err != nil {
		return jsonResp(map[string]string{"error": "grant_store_write"}, http.StatusInternalServerError)
	}
	return jsonResp(map[string]any{"revoked": id}, http.StatusOK)
}

func keysAdoptHandler(req *pluginapi.ManagementRequest) ([]byte, error) {
	var body struct {
		Index  int      `json:"index"`
		Name   string   `json:"name"`
		Mode   string   `json:"mode"`
		Models []string `json:"models"`
	}
	if json.Unmarshal(req.Body, &body) != nil {
		return jsonResp(map[string]string{"error": "invalid_body"}, http.StatusBadRequest)
	}
	proxyKeys, err := listProxyKeys(req.Headers)
	if err != nil {
		return jsonResp(map[string]string{"error": err.Error()}, http.StatusBadGateway)
	}
	if body.Index < 0 || body.Index >= len(proxyKeys) {
		return jsonResp(map[string]string{"error": "index_out_of_range"}, http.StatusBadRequest)
	}
	secret := strings.TrimSpace(proxyKeys[body.Index])
	if secret == "" {
		return jsonResp(map[string]string{"error": "empty_key"}, http.StatusBadRequest)
	}
	id, err := generateGrantID()
	if err != nil {
		return jsonResp(map[string]string{"error": "id_failed"}, http.StatusInternalServerError)
	}
	name := strings.TrimSpace(body.Name)
	if name == "" {
		name = "adopted"
	}
	g, err := normalizeGrant(fleetKeyGrant{
		ID:        id,
		Name:      name,
		Prefix:    displayPrefix(secret),
		KeyHash:   hashSecret(secret),
		Mode:      body.Mode,
		Models:    body.Models,
		CreatedAt: nowFunc().UTC(),
	})
	if err != nil {
		return jsonResp(map[string]string{"error": err.Error()}, http.StatusBadRequest)
	}
	grantsMu.Lock()
	defer grantsMu.Unlock()
	store, err := loadGrantStore()
	if err != nil {
		return jsonResp(map[string]string{"error": "grant_store_unreadable"}, http.StatusInternalServerError)
	}
	for _, existing := range store.Grants {
		if existing.KeyHash == g.KeyHash {
			return jsonResp(map[string]string{"error": "already_managed"}, http.StatusConflict)
		}
	}
	store.Grants = append(store.Grants, g)
	if err := saveGrantStore(store); err != nil {
		return jsonResp(map[string]string{"error": "grant_store_write"}, http.StatusInternalServerError)
	}
	return jsonResp(map[string]any{"key": publicFromGrant(g, hashSet(proxyKeys))}, http.StatusOK)
}

func deleteGrant(id string) error {
	grantsMu.Lock()
	defer grantsMu.Unlock()
	store, err := loadGrantStore()
	if err != nil {
		return err
	}
	next := store.Grants[:0]
	for _, g := range store.Grants {
		if g.ID != id {
			next = append(next, g)
		}
	}
	store.Grants = next
	return saveGrantStore(store)
}

func publicGrantsForHub() []publicGrant {
	grantsMu.Lock()
	store, err := loadGrantStore()
	grantsMu.Unlock()
	if err != nil {
		return nil
	}
	out := make([]publicGrant, 0, len(store.Grants))
	for _, g := range store.Grants {
		out = append(out, publicFromGrant(g, nil))
	}
	return out
}
