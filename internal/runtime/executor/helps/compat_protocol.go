package helps

import (
	"strings"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
)

// CompatUpstreamProtocol resolves the wire protocol for a compatibility model.
// The requested alias wins over the upstream name, so rows that share an upstream name resolve per alias. Only "responses" opts into the
// Responses wire; every other value keeps the chat-completions path.
func CompatUpstreamProtocol(compat *config.OpenAICompatibility, upstreamModel, requestedModel string) string {
	if compat == nil {
		return ""
	}
	upstream := strings.TrimSpace(upstreamModel)
	requested := strings.TrimSpace(requestedModel)
	if requested != "" {
		requested = strings.TrimPrefix(requested, compat.Name+"/")
	}
	if requested != "" {
		for i := range compat.Models {
			if strings.EqualFold(strings.TrimSpace(compat.Models[i].Alias), requested) {
				return normalizeCompatProtocol(compat.Models[i].Protocol)
			}
		}
	}
	if upstream != "" {
		for i := range compat.Models {
			if strings.EqualFold(strings.TrimSpace(compat.Models[i].Name), upstream) {
				return normalizeCompatProtocol(compat.Models[i].Protocol)
			}
		}
	}
	return ""
}

func normalizeCompatProtocol(protocol string) string {
	if strings.EqualFold(strings.TrimSpace(protocol), "responses") {
		return "responses"
	}
	return ""
}
