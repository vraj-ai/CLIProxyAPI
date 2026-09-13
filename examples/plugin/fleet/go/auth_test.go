package main

import (
	"encoding/json"
	"testing"

	pluginapi "github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
)

func callAuthParse(t *testing.T, req pluginapi.AuthParseRequest) pluginapi.AuthParseResponse {
	t.Helper()
	raw, err := json.Marshal(req)
	if err != nil {
		t.Fatalf("marshal request: %v", err)
	}
	out, err := authParse(raw)
	if err != nil {
		t.Fatalf("authParse error: %v", err)
	}
	var env envelope
	if err := json.Unmarshal(out, &env); err != nil {
		t.Fatalf("envelope decode: %v", err)
	}
	if !env.OK {
		t.Fatalf("authParse not ok: %s", string(out))
	}
	var resp pluginapi.AuthParseResponse
	if err := json.Unmarshal(env.Result, &resp); err != nil {
		t.Fatalf("result decode: %v", err)
	}
	return resp
}

func TestAuthParseClaimsRouterMarker(t *testing.T) {
	resp := callAuthParse(t, pluginapi.AuthParseRequest{
		Provider: "fleet-router",
		FileName: "fleet-router.json",
		RawJSON:  []byte(`{"type":"fleet-router","label":"Fleet CPA Router"}`),
	})
	if !resp.Handled {
		t.Fatal("fleet-router marker file was not claimed")
	}
	if resp.Auth.Provider != routerProvider || resp.Auth.ID != routerProvider {
		t.Fatalf("auth provider/id = %q/%q, want %q", resp.Auth.Provider, resp.Auth.ID, routerProvider)
	}
	if len(resp.Auth.StorageJSON) == 0 {
		t.Fatal("storage json missing from claimed auth")
	}
}

func TestAuthParseIgnoresOtherProviders(t *testing.T) {
	for _, raw := range [][]byte{
		[]byte(`{"type":"codex","token":"x"}`),
		[]byte(`{"type":"claude","token":"x"}`),
		[]byte(`{"type":"gemini-cli","token":"x"}`),
		[]byte(`{"other":true}`),
		nil,
	} {
		resp := callAuthParse(t, pluginapi.AuthParseRequest{RawJSON: raw})
		if resp.Handled {
			t.Fatalf("claimed foreign auth material: %s", string(raw))
		}
	}
}

func TestAuthRefreshEchoesStableAuth(t *testing.T) {
	raw, _ := json.Marshal(pluginapi.AuthRefreshRequest{
		StorageJSON: []byte(`{"type":"fleet-router"}`),
		Metadata:    map[string]any{"type": "fleet-router"},
		Attributes:  map[string]string{"path": "/x"},
	})
	out, err := authRefresh(raw)
	if err != nil {
		t.Fatalf("authRefresh error: %v", err)
	}
	var env envelope
	if err := json.Unmarshal(out, &env); err != nil || !env.OK {
		t.Fatalf("refresh envelope: %v", err)
	}
	var resp pluginapi.AuthRefreshResponse
	if err := json.Unmarshal(env.Result, &resp); err != nil {
		t.Fatalf("refresh decode: %v", err)
	}
	if resp.Auth.Provider != routerProvider || resp.Auth.Attributes["path"] != "/x" {
		t.Fatalf("refreshed auth lost state: %+v", resp.Auth)
	}
}
