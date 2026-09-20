package main

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	pluginabi "github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginabi"
	pluginapi "github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
)

func TestLookupUnusable(t *testing.T) {
	if lookupUnusable("ocg/muse-spark-1.3-contributor") == nil {
		t.Fatal("prefixed id")
	}
	if lookupUnusable("muse-spark-1.2-contributor") == nil {
		t.Fatal("bare id")
	}
	if lookupUnusable("or/muse-spark-1.3-contributor") != nil {
		t.Fatal("openrouter id must stay usable")
	}
	if lookupUnusable("ocg/glm-5.3") != nil {
		t.Fatal("glm must stay usable")
	}
}

func TestUnusableIntercept(t *testing.T) {
	resp := runIntercept(t, pluginabi.MethodRequestInterceptBefore, pluginapi.RequestInterceptRequest{
		SourceFormat: "openai",
		Model:        "ocg/muse-spark-1.3-contributor",
		Headers:      http.Header{"Authorization": []string{"Bearer sk-legacy-not-a-grant"}},
		Body:         []byte(`{"model":"ocg/muse-spark-1.3-contributor","messages":[{"role":"user","content":"hi"}]}`),
	})
	if !resp.Terminate || resp.StatusCode != http.StatusNotFound {
		t.Fatalf("terminate=%v status=%d", resp.Terminate, resp.StatusCode)
	}
	if !strings.Contains(string(resp.ResponseBody), "model_unusable") {
		t.Fatalf("body=%s", resp.ResponseBody)
	}
	if !strings.Contains(string(resp.ResponseBody), "or/muse-spark-1.3-contributor") {
		t.Fatalf("missing replacement: %s", resp.ResponseBody)
	}
	ok := runIntercept(t, pluginabi.MethodRequestInterceptBefore, pluginapi.RequestInterceptRequest{
		SourceFormat: "openai",
		Model:        "ocg/glm-5.3",
		Headers:      http.Header{"Authorization": []string{"Bearer sk-legacy-not-a-grant"}},
		Body:         []byte(`{"model":"ocg/glm-5.3","messages":[{"role":"user","content":"hi"}]}`),
	})
	if ok.Terminate {
		t.Fatal("glm terminated")
	}
}

func TestUnusableModelsFilter(t *testing.T) {
	req := pluginapi.ResponseInterceptRequest{
		RequestHeaders:  http.Header{"Authorization": []string{"Bearer sk-legacy-not-a-grant"}},
		ResponseHeaders: http.Header{"Content-Type": []string{"application/json"}},
		Body:            []byte(`{"object":"list","data":[{"id":"ocg/muse-spark-1.3-contributor"},{"id":"ocg/glm-5.3"},{"id":"or/muse-spark-1.3-contributor"}]}`),
	}
	raw, _ := json.Marshal(req)
	out, err := handleMethod(pluginabi.MethodResponseInterceptAfter, raw)
	if err != nil {
		t.Fatal(err)
	}
	var env envelope
	if json.Unmarshal(out, &env) != nil || !env.OK {
		t.Fatalf("%s", out)
	}
	var resp pluginapi.ResponseInterceptResponse
	if json.Unmarshal(env.Result, &resp) != nil {
		t.Fatal("result")
	}
	if strings.Contains(string(resp.Body), "ocg/muse-spark-1.3-contributor") {
		t.Fatalf("still listed: %s", resp.Body)
	}
	if !strings.Contains(string(resp.Body), "ocg/glm-5.3") {
		t.Fatalf("dropped glm: %s", resp.Body)
	}
	if !strings.Contains(string(resp.Body), "or/muse-spark-1.3-contributor") {
		t.Fatalf("dropped openrouter replacement: %s", resp.Body)
	}
}
