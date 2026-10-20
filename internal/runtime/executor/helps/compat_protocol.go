package helps

import (
	"strings"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
)

// CompatUpstreamProtocol resolves the wire protocol for a compatibility model.
// The upstream name wins over the client alias. Only "responses" opts into the
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
	for i := range compat.Models {
		model := compat.Models[i]
		if upstream != "" && strings.EqualFold(strings.TrimSpace(model.Name), upstream) {
			return normalizeCompatProtocol(model.Protocol)
		}
		if requested != "" && strings.EqualFold(strings.TrimSpace(model.Alias), requested) {
			return normalizeCompatProtocol(model.Protocol)
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
