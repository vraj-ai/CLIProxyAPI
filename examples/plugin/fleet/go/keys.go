package main

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

const (
	grantModeAll  = "all"
	grantModeList = "list"
	secretPrefix  = "sk-flt-"
)

type fleetKeyGrant struct {
	ID        string    `json:"id"`
	Name      string    `json:"name"`
	Prefix    string    `json:"prefix"`
	KeyHash   string    `json:"key_hash"`
	Mode      string    `json:"mode"`
	Models    []string  `json:"models,omitempty"`
	Disabled  bool      `json:"disabled,omitempty"`
	CreatedAt time.Time `json:"created_at"`
}

type fleetKeyStore struct {
	Version int             `json:"version"`
	Grants  []fleetKeyGrant `json:"grants"`
}

type publicGrant struct {
	ID         string   `json:"id"`
	Name       string   `json:"name"`
	Prefix     string   `json:"prefix"`
	Mode       string   `json:"mode"`
	Models     []string `json:"models"`
	Disabled   bool     `json:"disabled"`
	CreatedAt  string   `json:"created_at"`
	Live       bool     `json:"live"`
	ModelCount int      `json:"model_count"`
}

type legacyKeyView struct {
	Index  int    `json:"index"`
	Prefix string `json:"prefix"`
}

var grantsMu sync.Mutex

var grantFilePath = func() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".cli-proxy-api", "fleet-keys.json")
}

var generateSecret = func() (string, error) {
	var raw [16]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "", err
	}
	return secretPrefix + hex.EncodeToString(raw[:]), nil
}

var generateGrantID = func() (string, error) {
	var raw [8]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "", err
	}
	return "k_" + hex.EncodeToString(raw[:]), nil
}

func hashSecret(secret string) string {
	sum := sha256.Sum256([]byte(secret))
	return hex.EncodeToString(sum[:])
}

func displayPrefix(secret string) string {
	if len(secret) < 12 {
		return "sk-…"
	}
	return secret[:8] + "…" + secret[len(secret)-4:]
}

func normalizeGrant(g fleetKeyGrant) (fleetKeyGrant, error) {
	g.Name = strings.TrimSpace(g.Name)
	g.Mode = strings.TrimSpace(g.Mode)
	if g.Mode == "" {
		g.Mode = grantModeAll
	}
	if g.Mode != grantModeAll && g.Mode != grantModeList {
		return g, fmt.Errorf("mode must be all or list")
	}
	models := make([]string, 0, len(g.Models))
	seen := map[string]struct{}{}
	for _, m := range g.Models {
		m = strings.TrimSpace(m)
		if m == "" {
			continue
		}
		if _, ok := seen[m]; ok {
			continue
		}
		seen[m] = struct{}{}
		models = append(models, m)
	}
	g.Models = models
	if g.Mode == grantModeList && len(g.Models) == 0 {
		return g, fmt.Errorf("list mode needs at least one model")
	}
	if g.Mode == grantModeAll {
		g.Models = nil
	}
	return g, nil
}

func catalogAllows(g *fleetKeyGrant, model string) bool {
	if g == nil {
		return true
	}
	if g.Disabled {
		return false
	}
	if g.Mode == grantModeAll || g.Mode == "" {
		return true
	}
	model = strings.TrimSpace(model)
	if model == "" {
		return false
	}
	for _, pattern := range g.Models {
		pattern = strings.TrimSpace(pattern)
		if pattern == "" {
			continue
		}
		if pattern == "*" || pattern == model {
			return true
		}
		if strings.HasSuffix(pattern, "*") && strings.HasPrefix(model, strings.TrimSuffix(pattern, "*")) {
			return true
		}
		if strings.HasSuffix(pattern, "/") && strings.HasPrefix(model, pattern) {
			return true
		}
	}
	return false
}

func loadGrantStore() (fleetKeyStore, error) {
	path := grantFilePath()
	raw, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return fleetKeyStore{Version: 1}, nil
		}
		return fleetKeyStore{}, err
	}
	var store fleetKeyStore
	if json.Unmarshal(raw, &store) != nil {
		return fleetKeyStore{}, fmt.Errorf("grant store unreadable")
	}
	if store.Version == 0 {
		store.Version = 1
	}
	if store.Grants == nil {
		store.Grants = []fleetKeyGrant{}
	}
	return store, nil
}

func saveGrantStore(store fleetKeyStore) error {
	path := grantFilePath()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	store.Version = 1
	if store.Grants == nil {
		store.Grants = []fleetKeyGrant{}
	}
	raw, err := json.MarshalIndent(store, "", "  ")
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), "fleet-keys-*.tmp")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	if _, err := tmp.Write(raw); err != nil {
		tmp.Close()
		os.Remove(tmpName)
		return err
	}
	if err := tmp.Chmod(0o600); err != nil {
		tmp.Close()
		os.Remove(tmpName)
		return err
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmpName)
		return err
	}
	if err := os.Rename(tmpName, path); err != nil {
		os.Remove(tmpName)
		return err
	}
	return nil
}

func grantForSecret(secret string) (*fleetKeyGrant, error) {
	secret = strings.TrimSpace(secret)
	if secret == "" {
		return nil, nil
	}
	grantsMu.Lock()
	defer grantsMu.Unlock()
	store, err := loadGrantStore()
	if err != nil {
		return nil, err
	}
	h := hashSecret(secret)
	for i := range store.Grants {
		if store.Grants[i].KeyHash == h {
			g := store.Grants[i]
			return &g, nil
		}
	}
	return nil, nil
}

func extractBearer(header string) string {
	if header == "" {
		return ""
	}
	parts := strings.SplitN(header, " ", 2)
	if len(parts) != 2 {
		return strings.TrimSpace(header)
	}
	if strings.ToLower(parts[0]) != "bearer" {
		return strings.TrimSpace(header)
	}
	return strings.TrimSpace(parts[1])
}

func extractAPIKey(h http.Header, meta map[string]any) string {
	candidates := []string{}
	if h != nil {
		candidates = append(candidates,
			extractBearer(h.Get("Authorization")),
			strings.TrimSpace(h.Get("X-Goog-Api-Key")),
			strings.TrimSpace(h.Get("X-Api-Key")),
		)
	}
	candidates = append(candidates, metadataStrings(meta, "userApiKey", "api_key", "key", "auth_token")...)
	if q := metadataMap(meta, "query"); q != nil {
		candidates = append(candidates, metadataStrings(q, "key", "auth_token")...)
	}
	for _, c := range candidates {
		if c != "" {
			return c
		}
	}
	return ""
}

func metadataMap(meta map[string]any, key string) map[string]any {
	if meta == nil {
		return nil
	}
	m, _ := meta[key].(map[string]any)
	return m
}

func metadataStrings(meta map[string]any, keys ...string) []string {
	if meta == nil {
		return nil
	}
	out := make([]string, 0, len(keys))
	for _, key := range keys {
		s, _ := meta[key].(string)
		out = append(out, strings.TrimSpace(s))
	}
	return out
}

func grantAllowsRequest(secret, model, requested string) bool {
	g, err := grantForSecret(secret)
	if err != nil {
		return false
	}
	if g == nil {
		return true
	}
	if g.Disabled {
		return false
	}
	if model != "" && !catalogAllows(g, model) {
		return false
	}
	if requested != "" && !catalogAllows(g, requested) {
		return false
	}
	return true
}

var deniedModelBody = []byte(`{"error":{"message":"model not allowed for this API key","type":"permission_error","code":"model_not_allowed"}}`)

func filterModelList(body []byte, allow func(string) bool) []byte {
	if len(body) == 0 || allow == nil {
		return body
	}
	var doc map[string]any
	if json.Unmarshal(body, &doc) != nil {
		return body
	}
	if obj, _ := doc["object"].(string); obj != "list" {
		return body
	}
	data, ok := doc["data"].([]any)
	if !ok {
		return body
	}
	for _, item := range data {
		row, ok := item.(map[string]any)
		if !ok {
			return body
		}
		if _, ok := row["id"].(string); !ok {
			return body
		}
	}
	kept := make([]any, 0, len(data))
	for _, item := range data {
		obj := item.(map[string]any)
		id, _ := obj["id"].(string)
		if allow(id) {
			kept = append(kept, obj)
		}
	}
	doc["data"] = kept
	out, err := json.Marshal(doc)
	if err != nil {
		return body
	}
	return out
}

func publicFromGrant(g fleetKeyGrant, liveHashes map[string]struct{}) publicGrant {
	models := g.Models
	if models == nil {
		models = []string{}
	}
	_, live := liveHashes[g.KeyHash]
	count := len(models)
	if g.Mode == grantModeAll {
		count = -1
	}
	return publicGrant{
		ID:         g.ID,
		Name:       g.Name,
		Prefix:     g.Prefix,
		Mode:       g.Mode,
		Models:     models,
		Disabled:   g.Disabled,
		CreatedAt:  g.CreatedAt.UTC().Format(time.RFC3339),
		Live:       live,
		ModelCount: count,
	}
}

func hashSet(keys []string) map[string]struct{} {
	out := map[string]struct{}{}
	for _, k := range keys {
		k = strings.TrimSpace(k)
		if k == "" {
			continue
		}
		out[hashSecret(k)] = struct{}{}
	}
	return out
}
