package main

// The cpa router's provider key (fleet-router) has no upstream credential: it
// orchestrates local Herdr agents. The conductor still requires a real auth
// before it will dispatch to an executor, so the plugin claims a marker auth
// file ("type": "fleet-router") from the auth store and returns a synthetic
// record. The file carries no secrets; it only proves the router was
// deliberately enabled on this install.

import (
	"encoding/json"
	"strings"

	pluginapi "github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
)

const routerAuthLabel = "Fleet CPA Router"

func authIdentifier() ([]byte, error) {
	return okEnvelopeJSON(`{"identifier":"` + routerProvider + `"}`)
}

func authParse(raw []byte) ([]byte, error) {
	var req pluginapi.AuthParseRequest
	if err := json.Unmarshal(raw, &req); err != nil {
		return nil, err
	}
	var meta map[string]any
	_ = json.Unmarshal(req.RawJSON, &meta)
	typ, _ := meta["type"].(string)
	if strings.ToLower(strings.TrimSpace(typ)) != routerProvider &&
		strings.ToLower(strings.TrimSpace(req.Provider)) != routerProvider {
		return okEnvelope(pluginapi.AuthParseResponse{Handled: false})
	}
	fileName := strings.TrimSpace(req.FileName)
	if fileName == "" {
		fileName = routerProvider + ".json"
	}
	return okEnvelope(pluginapi.AuthParseResponse{
		Handled: true,
		Auth: pluginapi.AuthData{
			Provider:    routerProvider,
			ID:          routerProvider,
			FileName:    fileName,
			Label:       routerAuthLabel,
			StorageJSON: req.RawJSON,
			Metadata:    map[string]any{"type": routerProvider},
		},
	})
}

func authRefresh(raw []byte) ([]byte, error) {
	var req pluginapi.AuthRefreshRequest
	if err := json.Unmarshal(raw, &req); err != nil {
		return nil, err
	}
	return okEnvelope(pluginapi.AuthRefreshResponse{
		Auth: pluginapi.AuthData{
			Provider:    routerProvider,
			ID:          routerProvider,
			FileName:    routerProvider + ".json",
			Label:       routerAuthLabel,
			StorageJSON: req.StorageJSON,
			Metadata:    req.Metadata,
			Attributes:  req.Attributes,
		},
	})
}
