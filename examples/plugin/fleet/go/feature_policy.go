package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	pluginapi "github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
)

const (
	featurePxpipe   = "pxpipe"
	featureCaveman  = "caveman"
	featurePonytail = "ponytail"
	featureHeadroom = "headroom"
	featureRTK      = "rtk"
)

var featureNames = []string{featurePxpipe, featureCaveman, featurePonytail, featureHeadroom, featureRTK}

type clientToolConfig struct {
	Headroom bool `yaml:"headroom"`
	RTK      bool `yaml:"rtk"`
}

type modelFeaturePolicy struct {
	Pxpipe   *bool `json:"pxpipe,omitempty" yaml:"pxpipe,omitempty"`
	Caveman  *bool `json:"caveman,omitempty" yaml:"caveman,omitempty"`
	Ponytail *bool `json:"ponytail,omitempty" yaml:"ponytail,omitempty"`
	Headroom *bool `json:"headroom,omitempty" yaml:"headroom,omitempty"`
	RTK      *bool `json:"rtk,omitempty" yaml:"rtk,omitempty"`
}

type featurePolicyFile struct {
	Policies map[string]modelFeaturePolicy `json:"policies"`
}

type featureDefaults struct {
	Pxpipe   bool `json:"pxpipe"`
	Caveman  bool `json:"caveman"`
	Ponytail bool `json:"ponytail"`
	Headroom bool `json:"headroom"`
	RTK      bool `json:"rtk"`
}

type featureValues struct {
	Pxpipe   bool `json:"pxpipe"`
	Caveman  bool `json:"caveman"`
	Ponytail bool `json:"ponytail"`
	Headroom bool `json:"headroom"`
	RTK      bool `json:"rtk"`
}

type clientToolState struct {
	Available  bool   `json:"available"`
	Active     bool   `json:"active"`
	Configured bool   `json:"configured"`
	Kind       string `json:"kind"`
	Note       string `json:"note"`
}

type hubFeatureState struct {
	Defaults  featureDefaults               `json:"defaults"`
	Overrides map[string]modelFeaturePolicy `json:"overrides"`
	Effective map[string]featureValues      `json:"effective"`
	Tools     map[string]clientToolState    `json:"tools"`
}

func defaultFeatureDefaults(cfg pluginConfig) featureDefaults {
	return featureDefaults{
		Pxpipe:   cfg.PxpipeEnabled,
		Caveman:  cfg.Caveman,
		Ponytail: cfg.Ponytail,
		Headroom: cfg.ClientTools.Headroom,
		RTK:      cfg.ClientTools.RTK,
	}
}

func (f featureDefaults) value(name string) bool {
	switch name {
	case featurePxpipe:
		return f.Pxpipe
	case featureCaveman:
		return f.Caveman
	case featurePonytail:
		return f.Ponytail
	case featureHeadroom:
		return f.Headroom
	case featureRTK:
		return f.RTK
	default:
		return false
	}
}

func (p modelFeaturePolicy) value(name string) (*bool, bool) {
	var value *bool
	switch name {
	case featurePxpipe:
		value = p.Pxpipe
	case featureCaveman:
		value = p.Caveman
	case featurePonytail:
		value = p.Ponytail
	case featureHeadroom:
		value = p.Headroom
	case featureRTK:
		value = p.RTK
	default:
		return nil, false
	}
	return value, value != nil
}

func (p *modelFeaturePolicy) set(name string, value *bool) bool {
	switch name {
	case featurePxpipe:
		p.Pxpipe = value
	case featureCaveman:
		p.Caveman = value
	case featurePonytail:
		p.Ponytail = value
	case featureHeadroom:
		p.Headroom = value
	case featureRTK:
		p.RTK = value
	default:
		return false
	}
	return true
}

func validFeature(name string) bool {
	for _, known := range featureNames {
		if name == known {
			return true
		}
	}
	return false
}

func modelScopedFeature(name string) bool {
	return name == featurePxpipe || name == featureCaveman || name == featurePonytail
}

func validModelID(model string) bool {
	model = strings.TrimSpace(model)
	return model != "" && len(model) <= 200 && !strings.ContainsAny(model, "\r\n")
}

func validatePolicyMap(policies map[string]modelFeaturePolicy) error {
	for model, policy := range policies {
		if !validModelID(model) {
			return fmt.Errorf("invalid model id")
		}
		for _, name := range featureNames {
			if value, ok := policy.value(name); ok && value == nil {
				return fmt.Errorf("invalid %s policy", name)
			}
		}
	}
	return nil
}

func loadModelPolicies(path string) (map[string]modelFeaturePolicy, error) {
	if path == "" {
		return map[string]modelFeaturePolicy{}, nil
	}
	raw, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return map[string]modelFeaturePolicy{}, nil
	}
	if err != nil {
		return nil, err
	}
	var file featurePolicyFile
	if err := json.Unmarshal(raw, &file); err != nil {
		return nil, err
	}
	if file.Policies == nil {
		file.Policies = map[string]modelFeaturePolicy{}
	}
	if err := validatePolicyMap(file.Policies); err != nil {
		return nil, err
	}
	return file.Policies, nil
}

func saveModelPolicies(path string, policies map[string]modelFeaturePolicy) error {
	if err := validatePolicyMap(policies); err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	raw, err := json.Marshal(featurePolicyFile{Policies: policies})
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), "fleet-policies-*.tmp")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)
	if err := tmp.Chmod(0o600); err != nil {
		tmp.Close()
		return err
	}
	if _, err := tmp.Write(raw); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmpName, path)
}

func modelFeatureEnabled(cfg pluginConfig, model, feature string) bool {
	defaults := defaultFeatureDefaults(cfg)
	if !modelScopedFeature(feature) {
		return defaults.value(feature)
	}
	state.mu.Lock()
	policy := state.policies[model]
	state.mu.Unlock()
	if value, ok := policy.value(feature); ok {
		return *value
	}
	return defaults.value(feature)
}

func featureValuesFor(cfg pluginConfig, model string) featureValues {
	return featureValues{
		Pxpipe:   modelFeatureEnabled(cfg, model, featurePxpipe),
		Caveman:  modelFeatureEnabled(cfg, model, featureCaveman),
		Ponytail: modelFeatureEnabled(cfg, model, featurePonytail),
		Headroom: modelFeatureEnabled(cfg, model, featureHeadroom),
		RTK:      modelFeatureEnabled(cfg, model, featureRTK),
	}
}

func copyPolicies() map[string]modelFeaturePolicy {
	state.mu.Lock()
	defer state.mu.Unlock()
	out := make(map[string]modelFeaturePolicy, len(state.policies))
	for model, policy := range state.policies {
		out[model] = policy
	}
	return out
}

func toolAvailable(name string) bool {
	path := resolveBin(name)
	return path != name || fileExists(path)
}

func headroomActive() bool {
	resp, err := hostHTTPTimeout(pluginapi.HTTPRequest{
		Method: http.MethodGet,
		URL:    "http://127.0.0.1:8787/livez",
	}, 800*time.Millisecond)
	return err == nil && resp != nil && resp.StatusCode == http.StatusOK
}

func buildFeatureState(models []string) hubFeatureState {
	state.mu.Lock()
	cfg := state.config
	state.mu.Unlock()
	effective := make(map[string]featureValues, len(models))
	for _, model := range models {
		effective[model] = featureValuesFor(cfg, model)
	}
	headroomIsActive := headroomActive()
	return hubFeatureState{
		Defaults:  defaultFeatureDefaults(cfg),
		Overrides: copyPolicies(),
		Effective: effective,
		Tools: map[string]clientToolState{
			featureHeadroom: {
				Available: toolAvailable("headroom") || headroomIsActive,
				Active:    headroomIsActive, Configured: cfg.ClientTools.Headroom,
				Kind: "client proxy", Note: "runs before the Fleet link",
			},
			featureRTK: {
				Available: toolAvailable("rtk"), Active: toolAvailable("rtk"),
				Configured: cfg.ClientTools.RTK, Kind: "agent tool",
				Note: "rewrites shell output, not model requests",
			},
		},
	}
}

func setModelFeature(raw []byte) ([]byte, error) {
	var input map[string]json.RawMessage
	if json.Unmarshal(raw, &input) != nil {
		return jsonResp(map[string]string{"error": "invalid_body"}, http.StatusBadRequest)
	}
	var model, feature string
	if json.Unmarshal(input["model"], &model) != nil || !validModelID(model) ||
		json.Unmarshal(input["feature"], &feature) != nil || !validFeature(feature) {
		return jsonResp(map[string]string{"error": "invalid_feature_request"}, http.StatusBadRequest)
	}
	if !modelScopedFeature(feature) {
		return jsonResp(map[string]string{"error": "feature_is_global_only"}, http.StatusBadRequest)
	}
	valueRaw, ok := input["value"]
	if !ok {
		return jsonResp(map[string]string{"error": "value_required"}, http.StatusBadRequest)
	}
	var value *bool
	if string(valueRaw) != "null" {
		var parsed bool
		if json.Unmarshal(valueRaw, &parsed) != nil {
			return jsonResp(map[string]string{"error": "value_must_be_boolean_or_null"}, http.StatusBadRequest)
		}
		value = &parsed
	}
	state.mu.Lock()
	path := state.policyPath
	policies := make(map[string]modelFeaturePolicy, len(state.policies))
	for key, policy := range state.policies {
		policies[key] = policy
	}
	policy := policies[model]
	policy.set(feature, value)
	if policy.Pxpipe != nil || policy.Caveman != nil || policy.Ponytail != nil || policy.Headroom != nil || policy.RTK != nil {
		policies[model] = policy
	}
	if policy.Pxpipe == nil && policy.Caveman == nil && policy.Ponytail == nil && policy.Headroom == nil && policy.RTK == nil {
		delete(policies, model)
	}
	state.mu.Unlock()
	if err := saveModelPolicies(path, policies); err != nil {
		return jsonResp(map[string]string{"error": "policy_write_failed"}, http.StatusInternalServerError)
	}
	state.mu.Lock()
	state.policies = policies
	cfg := state.config
	state.mu.Unlock()
	if feature == featurePxpipe {
		if _, err := pxpipeScopeToggle(json.RawMessage(fmt.Sprintf(`{"model":%q,"on":%t}`, model, modelFeatureEnabled(cfg, model, featurePxpipe)))); err != nil {
			return jsonResp(map[string]string{"error": "pxpipe_scope_update_failed"}, http.StatusBadGateway)
		}
	}
	return jsonResp(map[string]any{"model": model, "feature": feature, "value": value, "effective": featureValuesFor(cfg, model)}, http.StatusOK)
}

func featurePolicyPath(cfg pluginConfig) string {
	if cfg.ModelPoliciesPath != "" {
		return cfg.ModelPoliciesPath
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".cli-proxy-api", "fleet-policies.json")
}
