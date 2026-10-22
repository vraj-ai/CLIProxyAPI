package openai

import (
	"encoding/json"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/registry"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/api/handlers"
)

func TestOpenAIModelsPreservesInputModalities(t *testing.T) {
	gin.SetMode(gin.TestMode)
	clientID := "openai-models-input-modalities-test"
	modelRegistry := registry.GetGlobalRegistry()
	modelRegistry.RegisterClient(clientID, "openai-compatibility", []*registry.ModelInfo{
		{
			ID:                       "or/muse-spark-1.3-contributor",
			OwnedBy:                  "openrouter",
			ExplicitInputModalities:  true,
			SupportedInputModalities: []string{"text", "image"},
		},
		{ID: "or/text-only", OwnedBy: "openrouter"},
	})
	t.Cleanup(func() {
		modelRegistry.UnregisterClient(clientID)
	})

	base := handlers.NewBaseAPIHandlers(&config.SDKConfig{}, nil)
	handler := NewOpenAIAPIHandler(base)
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest("GET", "/v1/models", nil)

	handler.OpenAIModels(c)

	var doc struct {
		Object string `json:"object"`
		Data   []struct {
			ID              string   `json:"id"`
			InputModalities []string `json:"input_modalities"`
		} `json:"data"`
	}
	raw := rec.Body.Bytes()
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatalf("invalid json: %v", err)
	}
	if doc.Object != "list" {
		t.Fatalf("object = %q", doc.Object)
	}
	var shape map[string]any
	if err := json.Unmarshal(raw, &shape); err != nil {
		t.Fatalf("invalid json: %v", err)
	}
	byID := map[string]map[string]any{}
	for _, item := range shape["data"].([]any) {
		row := item.(map[string]any)
		id, _ := row["id"].(string)
		byID[id] = row
	}
	declared, ok := byID["or/muse-spark-1.3-contributor"]
	if !ok {
		t.Fatalf("missing model: %s", raw)
	}
	mods, exists := declared["input_modalities"]
	if !exists {
		t.Fatalf("dropped input_modalities: %s", raw)
	}
	list, ok := mods.([]any)
	if !ok || len(list) != 2 || list[0] != "text" || list[1] != "image" {
		t.Fatalf("input_modalities = %#v", mods)
	}
	if _, exists := byID["or/text-only"]["input_modalities"]; exists {
		t.Fatalf("empty row must omit the field: %s", raw)
	}
	if declared["id"] != "or/muse-spark-1.3-contributor" || declared["object"] != "model" || declared["owned_by"] != "openrouter" {
		t.Fatalf("base fields changed: %#v", declared)
	}
}
