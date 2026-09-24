package api

import (
	"bytes"
	"strings"
	"testing"
)

func TestInjectConsoleInk(t *testing.T) {
	fixture := []byte(`<!doctype html><html><head><title>Console</title></head><body><main id="app">ORIGINAL_BODY_MARKER</main></body></html>`)
	out := injectConsoleInk(fixture)

	if !bytes.Contains(out, []byte("ORIGINAL_BODY_MARKER")) {
		t.Fatal("fixture body missing after injection")
	}
	if !bytes.Contains(out, []byte("ORIGINAL_BODY_MARKER")) || bytes.Count(out, []byte("ORIGINAL_BODY_MARKER")) != 1 {
		t.Fatal("fixture body must appear exactly once")
	}
	if !bytes.Contains(out, []byte("<title>Console</title>")) {
		t.Fatal("fixture head missing after injection")
	}
	for _, want := range []string{
		`id="fleet-console-ink"`,
		`id="fleet-console-rail"`,
		"--fleet-bg:#04060b",
		`/v0/resource/plugins/fleet/hub#/overview`,
		`/v0/resource/plugins/fleet/hub#/providers`,
		`/v0/resource/plugins/fleet/hub#/activity`,
		`id="fleet-status"`,
		`rail-brand`,
	} {
		if !strings.Contains(string(out), want) {
			t.Fatalf("injection missing %s", want)
		}
	}
	if bytes.Index(out, []byte("ORIGINAL_BODY_MARKER")) > bytes.Index(out, []byte(`id="fleet-console-ink"`)) {
		t.Fatal("ink must not replace the original body")
	}
}

func TestInjectConsoleInkKeepsBodyWithoutClose(t *testing.T) {
	fixture := []byte(`<html><body>OPEN_BODY_MARKER`)
	out := injectConsoleInk(fixture)
	if !bytes.Contains(out, []byte("OPEN_BODY_MARKER")) {
		t.Fatal("open-ended fixture body missing")
	}
	if !bytes.Contains(out, []byte(`id="fleet-console-rail"`)) {
		t.Fatal("rail missing on open-ended body")
	}
}
