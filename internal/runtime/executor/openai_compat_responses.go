package executor

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/runtime/executor/helps"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/util"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/usage"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v7/sdk/translator"
	log "github.com/sirupsen/logrus"
	"github.com/tidwall/gjson"
)

const openAICompatResponsesPath = "/responses"

func (e *OpenAICompatExecutor) executeResponses(
	ctx context.Context,
	auth *cliproxyauth.Auth,
	req cliproxyexecutor.Request,
	opts cliproxyexecutor.Options,
	baseModel, baseURL, apiKey string,
	reporter *helps.UsageReporter,
) (resp cliproxyexecutor.Response, err error) {
	from := opts.SourceFormat
	if from != sdktranslator.FormatOpenAI && from != sdktranslator.FormatOpenAIResponse {
		return resp, statusErr{code: http.StatusBadRequest, msg: "responses-upstream compat models support only openai or openai-response clients"}
	}
	responseFormat := cliproxyexecutor.ResponseFormatOrSource(opts)
	to := sdktranslator.FromString("openai-response")
	originalPayload := req.Payload
	if len(opts.OriginalRequest) > 0 {
		originalPayload = opts.OriginalRequest
	}
	isCompat := helps.APIKeyModelIsCompat(req)
	originalTranslated, translated := helps.TranslateRequestPairWithAPIKeyModelCompatibility(
		ctx, opts.Headers, e.cfg, from, to, baseModel, originalPayload, req.Payload, false, isCompat,
	)
	translated, err = helps.ApplyRequestThinking(translated, req, opts, from.String(), to.String(), e.Identifier())
	if err != nil {
		return resp, err
	}
	requestedModel := helps.PayloadRequestedModel(opts, req.Model)
	requestPath := helps.PayloadRequestPath(opts)
	translated = helps.ApplyPayloadConfigWithRequest(
		e.cfg, baseModel, to.String(), from.String(), "", translated, originalTranslated, requestedModel, requestPath, opts.Headers,
	)
	reporter.SetTranslatedReasoningEffort(translated, to.String())

	url := strings.TrimSuffix(baseURL, "/") + openAICompatResponsesPath
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(translated))
	if err != nil {
		return resp, err
	}
	e.applyResponsesHeaders(httpReq, auth, apiKey, opts)
	helps.RecordAPIRequest(ctx, e.cfg, helps.UpstreamRequestLog{
		URL:       url,
		Method:    http.MethodPost,
		Headers:   httpReq.Header.Clone(),
		Body:      translated,
		Provider:  e.Identifier(),
		AuthID:    authID(auth),
		AuthLabel: authLabel(auth),
		AuthType:  authType(auth),
		AuthValue: authValue(auth),
	})

	httpClient := helps.NewProxyAwareHTTPClient(ctx, e.cfg, auth, 0)
	httpClient = reporter.TrackHTTPClient(httpClient)
	httpResp, err := httpClient.Do(httpReq)
	if err != nil {
		helps.RecordAPIResponseError(ctx, e.cfg, err)
		return resp, err
	}
	defer func() {
		if errClose := httpResp.Body.Close(); errClose != nil {
			log.Errorf("openai compat executor: close responses body error: %v", errClose)
		}
	}()
	helps.RecordAPIResponseMetadata(ctx, e.cfg, httpResp.StatusCode, httpResp.Header.Clone())
	if httpResp.StatusCode < 200 || httpResp.StatusCode >= 300 {
		body, _ := io.ReadAll(httpResp.Body)
		helps.AppendAPIResponseChunk(ctx, e.cfg, body)
		err = newOpenAICompatStatusError(httpResp.StatusCode, httpResp.Header, body)
		return resp, err
	}
	body, err := io.ReadAll(httpResp.Body)
	if err != nil {
		helps.RecordAPIResponseError(ctx, e.cfg, err)
		return resp, err
	}
	helps.AppendAPIResponseChunk(ctx, e.cfg, body)
	reporter.ObserveResponseModel(body)
	if usage := responsesUsageDetail(body); usage != nil {
		reporter.Publish(ctx, *usage)
	}
	reporter.EnsurePublished(ctx)

	var param any
	out := sdktranslator.TranslateNonStream(ctx, to, responseFormat, req.Model, opts.OriginalRequest, translated, body, &param)
	if responseFormat == sdktranslator.FormatOpenAIResponse {
		out = helps.EnsureResponsesUsageDetails(out)
	}
	return cliproxyexecutor.Response{Payload: out, Headers: httpResp.Header.Clone()}, nil
}

func (e *OpenAICompatExecutor) executeResponsesStream(
	ctx context.Context,
	auth *cliproxyauth.Auth,
	req cliproxyexecutor.Request,
	opts cliproxyexecutor.Options,
	baseModel, baseURL, apiKey string,
	reporter *helps.UsageReporter,
) (_ *cliproxyexecutor.StreamResult, err error) {
	from := opts.SourceFormat
	if from != sdktranslator.FormatOpenAI && from != sdktranslator.FormatOpenAIResponse {
		return nil, statusErr{code: http.StatusBadRequest, msg: "responses-upstream compat models support only openai or openai-response clients"}
	}
	responseFormat := cliproxyexecutor.ResponseFormatOrSource(opts)
	to := sdktranslator.FromString("openai-response")
	originalPayload := req.Payload
	if len(opts.OriginalRequest) > 0 {
		originalPayload = opts.OriginalRequest
	}
	isCompat := helps.APIKeyModelIsCompat(req)
	originalTranslated, translated := helps.TranslateRequestPairWithAPIKeyModelCompatibility(
		ctx, opts.Headers, e.cfg, from, to, baseModel, originalPayload, req.Payload, true, isCompat,
	)
	translated, err = helps.ApplyRequestThinking(translated, req, opts, from.String(), to.String(), e.Identifier())
	if err != nil {
		return nil, err
	}
	requestedModel := helps.PayloadRequestedModel(opts, req.Model)
	requestPath := helps.PayloadRequestPath(opts)
	translated = helps.ApplyPayloadConfigWithRequest(
		e.cfg, baseModel, to.String(), from.String(), "", translated, originalTranslated, requestedModel, requestPath, opts.Headers,
	)
	reporter.SetTranslatedReasoningEffort(translated, to.String())

	url := strings.TrimSuffix(baseURL, "/") + openAICompatResponsesPath
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(translated))
	if err != nil {
		return nil, err
	}
	e.applyResponsesHeaders(httpReq, auth, apiKey, opts)
	httpReq.Header.Set("Accept", "text/event-stream")
	httpReq.Header.Set("Cache-Control", "no-cache")
	helps.RecordAPIRequest(ctx, e.cfg, helps.UpstreamRequestLog{
		URL:       url,
		Method:    http.MethodPost,
		Headers:   httpReq.Header.Clone(),
		Body:      translated,
		Provider:  e.Identifier(),
		AuthID:    authID(auth),
		AuthLabel: authLabel(auth),
		AuthType:  authType(auth),
		AuthValue: authValue(auth),
	})

	httpClient := helps.NewProxyAwareHTTPClient(ctx, e.cfg, auth, 0)
	httpClient = reporter.TrackHTTPClient(httpClient)
	httpResp, err := httpClient.Do(httpReq)
	if err != nil {
		helps.RecordAPIResponseError(ctx, e.cfg, err)
		return nil, err
	}
	helps.RecordAPIResponseMetadata(ctx, e.cfg, httpResp.StatusCode, httpResp.Header.Clone())
	if httpResp.StatusCode < 200 || httpResp.StatusCode >= 300 {
		body, _ := io.ReadAll(httpResp.Body)
		helps.AppendAPIResponseChunk(ctx, e.cfg, body)
		if errClose := httpResp.Body.Close(); errClose != nil {
			log.Errorf("openai compat executor: close responses body error: %v", errClose)
		}
		err = newOpenAICompatStatusError(httpResp.StatusCode, httpResp.Header, body)
		return nil, err
	}

	out := make(chan cliproxyexecutor.StreamChunk)
	go func() {
		defer close(out)
		defer func() {
			if errClose := httpResp.Body.Close(); errClose != nil {
				log.Errorf("openai compat executor: close responses body error: %v", errClose)
			}
		}()
		scanner := bufio.NewScanner(httpResp.Body)
		scanner.Buffer(nil, 52_428_800)
		var param any
		var eventName string
		var dataLines [][]byte
		terminal := false
		fail := func(streamErr statusErr) bool {
			helps.RecordAPIResponseError(ctx, e.cfg, streamErr)
			reporter.PublishFailure(ctx, streamErr)
			select {
			case out <- cliproxyexecutor.StreamChunk{Err: streamErr}:
			case <-ctx.Done():
			}
			return true
		}
		process := func() bool {
			name := eventName
			eventName = ""
			lines := dataLines
			dataLines = nil
			if len(lines) == 0 {
				return false
			}
			payload := bytes.TrimSpace(bytes.Join(lines, []byte("\n")))
			if !json.Valid(payload) {
				return fail(statusErr{code: http.StatusBadGateway, msg: "upstream Responses stream ended with invalid JSON"})
			}
			eventType := gjson.GetBytes(payload, "type").String()
			if eventType == "" {
				eventType = name
			}
			if eventType == "error" || eventType == "response.failed" {
				message := gjson.GetBytes(payload, "error.message").String()
				if message == "" {
					message = gjson.GetBytes(payload, "response.error.message").String()
				}
				if message == "" {
					message = string(payload)
				}
				return fail(statusErr{code: http.StatusBadGateway, msg: message})
			}
			if usage := responsesUsageDetail(payload); usage != nil {
				reporter.Publish(ctx, *usage)
			}
			reporter.ObserveResponseModel(payload)
			block := []byte("event: " + firstNonEmpty(eventType, "message") + "\ndata: " + string(payload) + "\n\n")
			var chunks [][]byte
			if from == sdktranslator.FormatOpenAIResponse && responseFormat == sdktranslator.FormatOpenAIResponse {
				chunks = [][]byte{block}
			} else {
				chunks = sdktranslator.TranslateStream(ctx, to, responseFormat, req.Model, opts.OriginalRequest, translated, block, &param)
			}
			for i := range chunks {
				select {
				case out <- cliproxyexecutor.StreamChunk{Payload: chunks[i]}:
				case <-ctx.Done():
					return true
				}
			}
			if eventType == "response.completed" || eventType == "response.incomplete" {
				terminal = true
				return true
			}
			return false
		}

		for scanner.Scan() {
			line := scanner.Bytes()
			helps.AppendAPIResponseChunk(ctx, e.cfg, line)
			trimmed := bytes.TrimSpace(line)
			if len(trimmed) == 0 {
				if process() {
					break
				}
				continue
			}
			if bytes.HasPrefix(trimmed, []byte("event:")) {
				eventName = strings.TrimSpace(string(trimmed[len("event:"):]))
				continue
			}
			if bytes.HasPrefix(trimmed, []byte("data:")) {
				dataLines = append(dataLines, bytes.Clone(bytes.TrimSpace(trimmed[len("data:"):])))
				continue
			}
			if bytes.HasPrefix(trimmed, []byte(":")) || bytes.HasPrefix(trimmed, []byte("id:")) || bytes.HasPrefix(trimmed, []byte("retry:")) {
				continue
			}
			if bytes.HasPrefix(trimmed, []byte("{")) || bytes.HasPrefix(trimmed, []byte("[")) {
				dataLines = append(dataLines, bytes.Clone(trimmed))
			}
		}
		if errScan := scanner.Err(); errScan != nil {
			helps.RecordAPIResponseError(ctx, e.cfg, errScan)
			reporter.PublishFailure(ctx, errScan)
			select {
			case out <- cliproxyexecutor.StreamChunk{Err: errScan}:
			case <-ctx.Done():
			}
			return
		}
		if !terminal {
			fail(statusErr{code: http.StatusBadGateway, msg: "upstream Responses stream closed before a terminal event"})
			return
		}
		reporter.EnsurePublished(ctx)
	}()
	return &cliproxyexecutor.StreamResult{Headers: httpResp.Header.Clone(), Chunks: out}, nil
}

func (e *OpenAICompatExecutor) applyResponsesHeaders(httpReq *http.Request, auth *cliproxyauth.Auth, apiKey string, opts cliproxyexecutor.Options) {
	httpReq.Header.Set("Content-Type", "application/json")
	if apiKey != "" {
		httpReq.Header.Set("Authorization", "Bearer "+apiKey)
	}
	httpReq.Header.Set("User-Agent", "cli-proxy-openai-compat")
	var attrs map[string]string
	if auth != nil {
		attrs = auth.Attributes
	}
	util.ApplyCustomHeadersFromAttrs(httpReq, attrs, opts.Headers)
}

func authID(auth *cliproxyauth.Auth) string {
	if auth == nil {
		return ""
	}
	return auth.ID
}

func authLabel(auth *cliproxyauth.Auth) string {
	if auth == nil {
		return ""
	}
	return auth.Label
}

func authType(auth *cliproxyauth.Auth) string {
	if auth == nil {
		return ""
	}
	authType, _ := auth.AccountInfo()
	return authType
}

func authValue(auth *cliproxyauth.Auth) string {
	if auth == nil {
		return ""
	}
	_, authValue := auth.AccountInfo()
	return authValue
}

// responsesUsageDetail maps a Responses usage object onto the chat-shaped detail
// the reporter already understands.
func responsesUsageDetail(body []byte) *usage.Detail {
	node := gjson.GetBytes(body, "response.usage")
	if !node.Exists() {
		node = gjson.GetBytes(body, "usage")
	}
	if !node.Exists() {
		return nil
	}
	prompt := node.Get("input_tokens").Int()
	completion := node.Get("output_tokens").Int()
	total := node.Get("total_tokens").Int()
	if total == 0 {
		total = prompt + completion
	}
	detail := usage.Detail{
		InputTokens:  prompt,
		OutputTokens: completion,
		TotalTokens:  total,
	}
	if cached := node.Get("input_tokens_details.cached_tokens").Int(); cached > 0 {
		detail.CachedTokens = cached
	}
	if reasoning := node.Get("output_tokens_details.reasoning_tokens").Int(); reasoning > 0 {
		detail.ReasoningTokens = reasoning
	}
	return &detail
}
