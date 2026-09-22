package main

import (
	"encoding/base64"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	pluginabi "github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginabi"
	pluginapi "github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
)

func encodeConsoleStored(secret, host, userAgent string) string {
	plain, _ := json.Marshal(secret)
	key := consoleKeyMaterial(host, userAgent)
	buf := make([]byte, len(plain))
	for i := range plain {
		buf[i] = plain[i] ^ key[i%len(key)]
	}
	return consoleKeyPrefix + base64.StdEncoding.EncodeToString(buf)
}

func TestFleetConsoleKey(t *testing.T) {
	secret := "mgmt-secret-fixture-" + strings.Repeat("x", 80)
	stored := encodeConsoleStored(secret, "127.0.0.1:8317", "test-agent")
	got := decodeConsoleStored(stored, "127.0.0.1:8317", "test-agent")
	if got != secret {
		t.Fatalf("decoded = %q", got)
	}
	if decodeConsoleStored(stored, "127.0.0.1:8317", "other-agent") == secret {
		t.Fatal("decode must depend on the browser material")
	}
	if decodeConsoleStored("mgmt-secret-fixture", "h", "u") != "mgmt-secret-fixture" {
		t.Fatal("plaintext passthrough expected")
	}
	if decodeConsoleStored(consoleKeyPrefix+"!!!", "h", "u") != "" {
		t.Fatal("undecodable prefixed shape must yield empty")
	}
	if decodeConsoleStored("", "h", "u") != "" {
		t.Fatal("empty input must yield empty")
	}

	out, err := managementDispatch(&pluginapi.ManagementRequest{
		Method: http.MethodGet,
		Path:   "/v0/resource/plugins/fleet/hub",
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	var env envelope
	if err := json.Unmarshal(out, &env); err != nil || !env.OK {
		t.Fatalf("envelope: %v %s", err, out)
	}
	var resp pluginapi.ManagementResponse
	if err := json.Unmarshal(env.Result, &resp); err != nil {
		t.Fatal(err)
	}
	body := string(resp.Body)
	for _, want := range []string{"consoleStoredKey", consoleKeyPrefix, consoleKeySeed} {
		if !strings.Contains(body, want) {
			t.Fatalf("hub shell missing keyring piece %s", want)
		}
	}
	if strings.Contains(body, secret) {
		t.Fatal("hub shell must not carry key material")
	}
}

func TestUsagePace(t *testing.T) {
	resetState(pluginConfig{})
	now := time.Date(2026, 9, 22, 12, 0, 0, 0, time.UTC)

	usage := pluginapi.UsageRecord{
		Model:           "or/grok-4.6",
		APIKey:          "sk-secret-usage-key",
		RequestedAt:     now,
		Latency:         2 * time.Second,
		Detail:          pluginapi.UsageDetail{OutputTokens: 80, InputTokens: 10, TotalTokens: 90},
		ResponseHeaders: http.Header{"x-keep": {"1"}},
	}
	raw, err := json.Marshal(usage)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := handleMethod(pluginabi.MethodUsageHandle, raw); err != nil {
		t.Fatal(err)
	}

	state.mu.Lock()
	pace := buildPaceView()
	state.mu.Unlock()
	if pace.Model != "or/grok-4.6" {
		t.Fatalf("pace model = %q", pace.Model)
	}
	if pace.TPS != 40 {
		t.Fatalf("tps = %v, want 40 (80 output tokens over 2 seconds)", pace.TPS)
	}
	if pace.OutputTokens != 80 || pace.LatencyMS != 2000 {
		t.Fatalf("pace = %+v", pace)
	}
	encoded, err := json.Marshal(pace)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encoded), "sk-secret-usage-key") {
		t.Fatal("pace view leaked the usage record API key")
	}

	resetState(pluginConfig{})
	start := runIntercept(t, pluginabi.MethodRequestInterceptBefore, pluginapi.RequestInterceptRequest{
		RequestID:    "req-running",
		SourceFormat: "openai",
		Model:        "or/kimi-k2.7",
		Body:         []byte(`{"model":"or/kimi-k2.7","messages":[{"role":"user","content":"hi"}]}`),
	})
	if start.StatusCode != 0 {
		t.Fatalf("status = %d", start.StatusCode)
	}
	state.mu.Lock()
	running := buildPaceView()
	state.mu.Unlock()
	if !containsModel(running.Running, "or/kimi-k2.7") {
		t.Fatalf("running = %v, want or/kimi-k2.7", running.Running)
	}
	encoded, err = json.Marshal(running)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encoded), `"tps"`) {
		t.Fatalf("started request with no usage record must omit a rate: %s", encoded)
	}
	if running.Model != "" {
		t.Fatalf("no completed model expected, got %q", running.Model)
	}
}

func containsModel(list []string, want string) bool {
	for _, m := range list {
		if m == want {
			return true
		}
	}
	return false
}
