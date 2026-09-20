package auth

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/registry"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
)

func TestWrapUnavailablePreservesUpstream5xx(t *testing.T) {
	upstream := &Error{HTTPStatus: http.StatusInternalServerError, Message: "Internal server error"}
	got := wrapUnavailable(upstream)
	if got != upstream {
		t.Fatalf("wrapUnavailable(500) = %v, want the upstream error", got)
	}
	if statusCodeFromError(got) != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", statusCodeFromError(got))
	}
	if strings.Contains(got.Error(), "no auth available") {
		t.Fatalf("500 wrapped as missing auth: %v", got)
	}

	missing := wrapUnavailable(nil)
	if !strings.Contains(missing.Error(), "no auth available") {
		t.Fatalf("nil last error = %v, want auth_unavailable", missing)
	}
}

type upstream500Executor struct{}

func (upstream500Executor) Identifier() string { return "openai-compatible-opencode-go" }

func (upstream500Executor) Execute(context.Context, *Auth, cliproxyexecutor.Request, cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	return cliproxyexecutor.Response{}, &Error{HTTPStatus: http.StatusInternalServerError, Message: "Internal server error"}
}

func (upstream500Executor) ExecuteStream(context.Context, *Auth, cliproxyexecutor.Request, cliproxyexecutor.Options) (*cliproxyexecutor.StreamResult, error) {
	return nil, &Error{HTTPStatus: http.StatusInternalServerError, Message: "Internal server error"}
}

func (upstream500Executor) Refresh(context.Context, *Auth) (*Auth, error) { return nil, nil }

func (upstream500Executor) CountTokens(context.Context, *Auth, cliproxyexecutor.Request, cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	return cliproxyexecutor.Response{}, nil
}

func (upstream500Executor) HttpRequest(context.Context, *Auth, *http.Request) (*http.Response, error) {
	return nil, nil
}

func TestManager_Execute_Upstream500IsNotAuthUnavailable(t *testing.T) {
	const authID = "ocg-key"
	const model = "ocg/muse-spark-1.3-contributor"
	m := NewManager(nil, nil, nil)
	m.RegisterExecutor(upstream500Executor{})
	reg := registry.GetGlobalRegistry()
	reg.RegisterClient(authID, "openai-compatible-opencode-go", []*registry.ModelInfo{{ID: model}})
	t.Cleanup(func() { reg.UnregisterClient(authID) })
	if _, err := m.Register(context.Background(), &Auth{
		ID:       authID,
		Provider: "openai-compatible-opencode-go",
		Metadata: map[string]any{"disable_cooling": true},
	}); err != nil {
		t.Fatal(err)
	}
	_, err := m.Execute(context.Background(), []string{"openai-compatible-opencode-go"}, cliproxyexecutor.Request{Model: model}, cliproxyexecutor.Options{})
	if err == nil {
		t.Fatal("expected upstream 500")
	}
	if statusCodeFromError(err) != http.StatusInternalServerError {
		t.Fatalf("status = %d err=%v, want 500", statusCodeFromError(err), err)
	}
	if strings.Contains(err.Error(), "no auth available") {
		t.Fatalf("upstream 500 reported as missing auth: %v", err)
	}
}
