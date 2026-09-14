package main

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"testing"

	pluginapi "github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
)

func TestHandleMethodNilQuotaUsesDefaultSource(t *testing.T) {
	bin := t.TempDir()
	stub := "#!/bin/sh\n[ \"$1\" = --force ] || exit 1\nprintf '%s\\n' '{\"schema\":\"openusage.limits.v1\",\"providers\":{}}'\n"
	if err := os.WriteFile(filepath.Join(bin, "openusage"), []byte(stub), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin)
	resetRouter(pluginConfig{}, nil)
	t.Cleanup(func() { resetRouter(pluginConfig{}, nil) })
	state.mu.Lock()
	state.quota = nil
	state.quotaCache.at = testNow.AddDate(-1, 0, 0)
	state.quotaCache.fetching = false
	state.mu.Unlock()

	env := callMethod(t, "management.handle", pluginapi.ManagementRequest{Method: http.MethodGet, Path: "/v0/management/fleet/state"})
	if !env.OK {
		t.Fatalf("nil quota source broke hub state: %+v", env)
	}
	state.mu.Lock()
	defer state.mu.Unlock()
	if state.quotaCache.fetching || state.quotaCache.err != nil || state.quotaCache.snap == nil {
		t.Fatalf("default quota source was not fetched successfully: %+v", state.quotaCache)
	}
}

type panicQuota struct{}

func (panicQuota) fetch(context.Context) (map[string]providerQuota, error) {
	panic("quota source exploded")
}

func TestHandleMethodPanicReturnsErrorAndDoesNotWedgeQuota(t *testing.T) {
	resetRouter(pluginConfig{}, nil)
	state.mu.Lock()
	state.quota = panicQuota{}
	state.quotaCache.at = testNow.AddDate(-1, 0, 0)
	state.quotaCache.fetching = false
	state.mu.Unlock()
	t.Cleanup(func() { resetRouter(pluginConfig{}, nil) })

	raw, _ := json.Marshal(pluginapi.ManagementRequest{Method: http.MethodGet, Path: "/v0/management/fleet/state"})
	if _, err := handleMethod("management.handle", raw); err == nil {
		t.Fatal("expected panic to surface as an error")
	}

	state.mu.Lock()
	fetching := state.quotaCache.fetching
	state.quota = fakeQuota{snap: healthyQuota()}
	state.mu.Unlock()
	if fetching {
		t.Fatal("quota cache left in fetching state after panic")
	}
	if snap, err := quotaCached(); err != nil || len(snap) == 0 {
		t.Fatalf("quota did not recover after panic: %v %v", snap, err)
	}
}
