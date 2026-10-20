package registry

import (
	"reflect"
	"testing"
)

func TestOpenAIModelsInputModalities(t *testing.T) {
	r := newTestModelRegistry()
	r.RegisterClient("modality-client", "OpenAI", []*ModelInfo{
		{
			ID:                      "or/muse-spark-1.3-contributor",
			OwnedBy:                 "openrouter",
			ExplicitInputModalities: true,
			SupportedInputModalities: []string{
				"TEXT", "image", "image",
			},
		},
		{ID: "or/text-only", OwnedBy: "openrouter"},
		{
			ID:                       "devin/implicit",
			OwnedBy:                  "devin",
			SupportedInputModalities: []string{"text"},
		},
	})

	byID := map[string]map[string]any{}
	for _, m := range r.GetAvailableModels("openai") {
		id, _ := m["id"].(string)
		byID[id] = m
	}

	declared, ok := byID["or/muse-spark-1.3-contributor"]
	if !ok {
		t.Fatal("missing declared model")
	}
	if got := declared["input_modalities"]; !reflect.DeepEqual(got, []string{"text", "image"}) {
		t.Fatalf("input_modalities = %#v", got)
	}

	if _, exists := byID["or/text-only"]["input_modalities"]; exists {
		t.Fatal("model without modalities must omit the field")
	}
	if _, exists := byID["devin/implicit"]["input_modalities"]; exists {
		t.Fatal("undeclared modalities must not leak template defaults")
	}
}
