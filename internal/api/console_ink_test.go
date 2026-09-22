package api

import (
	"strings"
	"testing"
)

func TestInjectConsoleInk(t *testing.T) {
	raw := []byte("<html><head><title>x</title></head><body>management app</body></html>")
	got := string(InjectConsoleInk(raw))
	if !strings.Contains(got, "management app") {
		t.Fatal("injection dropped the original body")
	}
	for _, want := range []string{
		"--bg-primary:#070b14",
		"--primary-color:#8eb0ff",
		"fleet-native-nav",
		"fleet.hub.key",
		"enc::v1::",
		"cli-proxy-api-webui::secure-storage",
		`id="fleet-ink"`,
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("injected document missing %q", want)
		}
	}
	if strings.Contains(got, "sk-") {
		t.Fatal("injection embedded a credential")
	}
	if !strings.HasSuffix(strings.TrimSpace(got), "</html>") {
		t.Fatal("injection did not stay inside the document")
	}
}
