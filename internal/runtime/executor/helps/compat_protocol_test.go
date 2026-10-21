package helps

import (
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
)

func TestCompatUpstreamProtocol(t *testing.T) {
	compat := &config.OpenAICompatibility{
		Name: "opencode-go",
		Models: []config.OpenAICompatibilityModel{
			{Name: "muse-spark-1.3-contributor", Alias: "muse-spark-1.3-contributor", Protocol: "responses"},
			{Name: "glm-5.3", Alias: "glm-5.3"},
			{Name: "alias-only", Alias: "alias-only", Protocol: "responses"},
			{Name: "other", Alias: "muse-spark-1.3-contributor"},
			{Name: "alias-only", Alias: "alias-only-chat"},
		},
	}
	cases := []struct {
		name      string
		upstream  string
		requested string
		want      string
	}{
		{"upstream name", "muse-spark-1.3-contributor", "muse-spark-1.3-contributor", "responses"},
		{"alias", "", "alias-only", "responses"},
		{"prefixed alias", "", "opencode-go/alias-only", "responses"},
		{"chat default", "glm-5.3", "glm-5.3", ""},
		{"alias beats shared upstream name", "glm-5.3", "muse-spark-1.3-contributor", "responses"},
		{"shared upstream name, other alias", "alias-only", "alias-only-chat", ""},
		{"unknown", "nope", "nope", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := CompatUpstreamProtocol(compat, tc.upstream, tc.requested); got != tc.want {
				t.Fatalf("got %q want %q", got, tc.want)
			}
		})
	}
	if got := CompatUpstreamProtocol(nil, "x", "x"); got != "" {
		t.Fatalf("nil compat = %q", got)
	}
}
