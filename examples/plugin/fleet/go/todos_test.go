package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
)

type fakeTodosRunner struct {
	out   []byte
	err   error
	calls int
	argv  [][]string
}

func (f *fakeTodosRunner) run(_ context.Context, argv ...string) ([]byte, error) {
	f.calls++
	f.argv = append(f.argv, append([]string(nil), argv...))
	return f.out, f.err
}

const goodTodos = `{"todos":[{"id":1}]}`

func TestTodosReaderTable(t *testing.T) {
	tests := []struct {
		name        string
		prewarm     bool
		out         []byte
		runErr      error
		advance     time.Duration
		wantRaw     string
		wantStale   bool
		wantErr     bool
		wantFetched time.Duration
		wantCalls   int
	}{
		{
			name: "success", out: []byte(goodTodos),
			wantRaw: goodTodos, wantCalls: 1,
		},
		{
			name: "ssh error without cache", runErr: errors.New("exit status 255"),
			wantStale: true, wantErr: true, wantCalls: 1,
		},
		{
			name: "ssh error after success serves last good", prewarm: true,
			runErr: errors.New("exit status 255"), advance: 20 * time.Second,
			wantRaw: goodTodos, wantStale: true, wantErr: true, wantCalls: 1,
		},
		{
			name: "empty output", out: []byte("  \n"),
			wantStale: true, wantErr: true, wantCalls: 1,
		},
		{
			name: "cache hit inside ttl", prewarm: true, advance: 10 * time.Second,
			wantRaw: goodTodos, wantCalls: 0,
		},
		{
			name: "cache expired after ttl", prewarm: true, advance: 16 * time.Second,
			out: []byte(`{"todos":[]}`), wantRaw: `{"todos":[]}`,
			wantFetched: 16 * time.Second, wantCalls: 1,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			clock := testNow
			runner := &fakeTodosRunner{out: []byte(goodTodos)}
			todos := &todosReader{run: runner.run, now: func() time.Time { return clock }}
			if tc.prewarm {
				if got := todos.snapshot(); got.Raw != goodTodos || got.Stale {
					t.Fatalf("prewarm = %+v", got)
				}
				runner.calls, runner.argv = 0, nil
			}
			clock = testNow.Add(tc.advance)
			runner.out, runner.err = tc.out, tc.runErr
			got := todos.snapshot()
			if got.Raw != tc.wantRaw {
				t.Fatalf("raw = %q, want %q", got.Raw, tc.wantRaw)
			}
			if got.Stale != tc.wantStale {
				t.Fatalf("stale = %v, want %v", got.Stale, tc.wantStale)
			}
			if (got.Error != "") != tc.wantErr {
				t.Fatalf("error = %q, wantErr %v", got.Error, tc.wantErr)
			}
			if want := testNow.Add(tc.wantFetched).Format(time.RFC3339); got.FetchedAt != want {
				t.Fatalf("fetched_at = %q, want %q", got.FetchedAt, want)
			}
			if runner.calls != tc.wantCalls {
				t.Fatalf("runner calls = %d, want %d", runner.calls, tc.wantCalls)
			}
		})
	}
}

func TestTodosArgvIsFixedVector(t *testing.T) {
	t.Setenv("FLEET_TODOS_SSH_TARGET", "root@evil")
	t.Setenv("FLEET_TODOS_PATH", "/etc/shadow")
	runner := &fakeTodosRunner{out: []byte("{}")}
	todos := &todosReader{run: runner.run, now: func() time.Time { return testNow }}
	if _, err := todos.handle(); err != nil {
		t.Fatal(err)
	}
	if runner.calls != 1 {
		t.Fatalf("calls = %d", runner.calls)
	}
	want := []string{
		"ssh",
		"-o", "BatchMode=yes",
		"-o", "ConnectTimeout=4",
		"-o", "StrictHostKeyChecking=accept-new",
		"--", "box@grokbot-box",
		"cat", "/home/box/agent-data/engineering/todos.json",
	}
	got := runner.argv[0]
	if len(got) != len(want) {
		t.Fatalf("argv = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("argv[%d] = %q, want %q", i, got[i], want[i])
		}
	}
}

func TestTodosOversizeOutputIsFailure(t *testing.T) {
	runner := &fakeTodosRunner{out: []byte(goodTodos)}
	todos := &todosReader{run: runner.run, now: func() time.Time { return testNow }}
	if got := todos.snapshot(); got.Raw != goodTodos || got.Stale {
		t.Fatalf("prewarm = %+v", got)
	}

	runner.out = bytes.Repeat([]byte("x"), maxTodosBytes+1)
	clock := testNow.Add(todosCacheTTL)
	todos.now = func() time.Time { return clock }
	got := todos.snapshot()
	if !got.Stale || got.Error != "todos file too large" {
		t.Fatalf("oversize = %+v", got)
	}
	if got.Raw != goodTodos {
		t.Fatalf("raw = %q, want last good", got.Raw)
	}

	fresh := &todosReader{run: runner.run, now: func() time.Time { return testNow }}
	if got := fresh.snapshot(); !got.Stale || got.Raw != "" || got.Error != "todos file too large" {
		t.Fatalf("oversize without cache = %+v", got)
	}

	runner.out = bytes.Repeat([]byte("x"), maxTodosBytes)
	if got := fresh.snapshot(); got.Stale || len(got.Raw) != maxTodosBytes {
		t.Fatalf("exact cap = %+v", got)
	}
}

func TestTodosHandleAlwaysOK(t *testing.T) {
	runner := &fakeTodosRunner{err: errors.New("boom")}
	todos := &todosReader{run: runner.run, now: func() time.Time { return testNow }}
	envRaw, err := todos.handle()
	if err != nil {
		t.Fatal(err)
	}
	resp := decodeTodosResponse(t, envRaw)
	if resp.StatusCode != 200 {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	var body todosResponse
	if err := json.Unmarshal(resp.Body, &body); err != nil {
		t.Fatal(err)
	}
	if !body.Stale || body.Raw != "" || body.Error == "" {
		t.Fatalf("body = %+v", body)
	}
}

func TestTodosRouteRegisteredAndDispatched(t *testing.T) {
	old := todosHandler
	t.Cleanup(func() { todosHandler = old })
	todosHandler = &todosReader{
		run: func(_ context.Context, _ ...string) ([]byte, error) { return []byte(goodTodos), nil },
		now: func() time.Time { return testNow },
	}

	regRaw, err := handleMethod("management.register", nil)
	if err != nil {
		t.Fatal(err)
	}
	var reg envelope
	if err := json.Unmarshal(regRaw, &reg); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(reg.Result), `"/fleet/todos"`) {
		t.Fatalf("registration missing /fleet/todos: %s", reg.Result)
	}

	raw, err := json.Marshal(pluginapi.ManagementRequest{Method: "GET", Path: "/v0/management/fleet/todos"})
	if err != nil {
		t.Fatal(err)
	}
	envRaw, err := handleMethod("management.handle", raw)
	if err != nil {
		t.Fatal(err)
	}
	resp := decodeTodosResponse(t, envRaw)
	if resp.StatusCode != 200 {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	var body todosResponse
	if err := json.Unmarshal(resp.Body, &body); err != nil {
		t.Fatal(err)
	}
	if body.Raw != goodTodos || body.Stale {
		t.Fatalf("body = %+v", body)
	}
}

func decodeTodosResponse(t *testing.T, envRaw []byte) pluginapi.ManagementResponse {
	t.Helper()
	var env envelope
	if err := json.Unmarshal(envRaw, &env); err != nil {
		t.Fatal(err)
	}
	if !env.OK {
		t.Fatalf("envelope error: %+v", env.Error)
	}
	var resp pluginapi.ManagementResponse
	if err := json.Unmarshal(env.Result, &resp); err != nil {
		t.Fatal(err)
	}
	return resp
}
