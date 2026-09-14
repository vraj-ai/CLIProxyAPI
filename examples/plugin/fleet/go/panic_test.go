package main

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"

	pluginapi "github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
)

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
