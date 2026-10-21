// todos.go: read-only passthrough for the engineering todo board. The box owns
// the file; this plugin only ships its raw text to the console over the
// existing Tailscale SSH link and never parses or validates it.
package main

import (
	"bytes"
	"context"
	"net/http"
	"os"
	"sync"
	"time"
)

const (
	defaultTodosSSHTarget = "box@grokbot-box"
	defaultTodosPath      = "/home/box/agent-data/engineering/todos.json"
	todosCacheTTL         = 15 * time.Second
	todosFetchTimeout     = 8 * time.Second
)

// todosResponse is the stable wire shape; every field is always present.
type todosResponse struct {
	Raw       string `json:"raw"`
	FetchedAt string `json:"fetched_at"`
	Stale     bool   `json:"stale"`
	Error     string `json:"error"`
}

type todosReader struct {
	run commandRunner
	now func() time.Time

	// ponytail: one mutex serializes cache access AND the ssh call, so
	// concurrent console requests can wait up to 8s. Fleet console traffic is
	// a handful of operators; split into singleflight if that changes.
	mu        sync.Mutex
	lastGood  string
	haveGood  bool
	fetchedAt time.Time
	cachedAt  time.Time
}

func newTodosReader() *todosReader {
	return &todosReader{run: execRunner, now: nowFunc}
}

var todosHandler = newTodosReader()

func todosSSHTarget() string {
	if v := os.Getenv("FLEET_TODOS_SSH_TARGET"); v != "" {
		return v
	}
	return defaultTodosSSHTarget
}

func todosPath() string {
	if v := os.Getenv("FLEET_TODOS_PATH"); v != "" {
		return v
	}
	return defaultTodosPath
}

// argv is a fixed vector handed straight to exec, never to a shell. Env
// overrides can only replace the target or the remote path.
func (t *todosReader) argv() []string {
	return []string{
		"ssh",
		"-o", "BatchMode=yes",
		"-o", "ConnectTimeout=4",
		"-o", "StrictHostKeyChecking=accept-new",
		todosSSHTarget(),
		"cat", todosPath(),
	}
}

func (t *todosReader) snapshot() todosResponse {
	t.mu.Lock()
	defer t.mu.Unlock()
	now := t.now()
	if t.haveGood && now.Sub(t.cachedAt) < todosCacheTTL {
		return todosResponse{Raw: t.lastGood, FetchedAt: t.fetchedAt.Format(time.RFC3339)}
	}
	ctx, cancel := context.WithTimeout(context.Background(), todosFetchTimeout)
	defer cancel()
	out, err := t.run(ctx, t.argv()...)
	if err != nil {
		return t.failure(now, "ssh failed: "+shortErr(err))
	}
	if len(bytes.TrimSpace(out)) == 0 {
		return t.failure(now, "empty todos file")
	}
	t.lastGood = string(out)
	t.haveGood = true
	t.fetchedAt, t.cachedAt = now, now
	return todosResponse{Raw: t.lastGood, FetchedAt: t.fetchedAt.Format(time.RFC3339)}
}

// failure serves the last good payload when one exists. The cache timestamp is
// left alone so the next request retries ssh instead of trusting a failure.
func (t *todosReader) failure(now time.Time, msg string) todosResponse {
	resp := todosResponse{FetchedAt: now.Format(time.RFC3339), Stale: true, Error: msg}
	if t.haveGood {
		resp.Raw = t.lastGood
		resp.FetchedAt = t.fetchedAt.Format(time.RFC3339)
	}
	return resp
}

// handle always answers HTTP 200 with the envelope shape; failures are data.
func (t *todosReader) handle() ([]byte, error) {
	return jsonResp(t.snapshot(), http.StatusOK)
}

func shortErr(err error) string {
	msg := err.Error()
	if len(msg) > 200 {
		msg = msg[:200]
	}
	return msg
}
