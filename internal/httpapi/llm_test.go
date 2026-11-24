package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/ph0nkj/mailcontroller/internal/gmail"
	"github.com/ph0nkj/mailcontroller/internal/llm"
)

type fakeLLM struct {
	status       llm.Status
	saveErr      error
	testErr      error
	summarizeErr error
	draftErr     error
	clearCalls   int
	testCalls    int
	saved        *llm.Settings
	summarizeIDs []string
	draftID      string
	draftNotes   string
	summary      llm.SummaryResult
	draft        llm.Draft
}

func (f *fakeLLM) Status(context.Context) (llm.Status, error) { return f.status, nil }

func (f *fakeLLM) Save(_ context.Context, settings llm.Settings) (llm.Status, error) {
	f.saved = &settings
	if f.saveErr != nil {
		return llm.Status{}, f.saveErr
	}
	return f.status, nil
}

func (f *fakeLLM) Clear(context.Context) error {
	f.clearCalls++
	return nil
}

func (f *fakeLLM) Test(context.Context) error {
	f.testCalls++
	return f.testErr
}

func (f *fakeLLM) Summarize(_ context.Context, ids []string) (llm.SummaryResult, error) {
	f.summarizeIDs = append([]string(nil), ids...)
	if f.summarizeErr != nil {
		return llm.SummaryResult{}, f.summarizeErr
	}
	return f.summary, nil
}

func (f *fakeLLM) DraftReply(_ context.Context, messageID, instructions string) (llm.Draft, error) {
	f.draftID = messageID
	f.draftNotes = instructions
	if f.draftErr != nil {
		return llm.Draft{}, f.draftErr
	}
	return f.draft, nil
}

func newLLMTestAPI(t *testing.T, service *fakeLLM) (http.Handler, *http.Cookie, string) {
	t.Helper()
	_, handler := newTestAPIWithDeps(t, Deps{LLM: service})
	cookie, csrf := loginSession(t, handler)
	return handler, cookie, csrf
}

func TestLLMStatusRequiresAuthAndHidesKey(t *testing.T) {
	service := &fakeLLM{status: llm.Status{
		Configured: true,
		Provider:   llm.ProviderOpenAI,
		BaseURL:    llm.DefaultOpenAIBaseURL,
		Model:      "gpt-test",
		HasAPIKey:  true,
	}}
	_, handler := newTestAPIWithDeps(t, Deps{LLM: service})

	rec := doJSON(t, handler, nil, "", http.MethodGet, "/api/v1/llm/status", "")
	require.Equal(t, http.StatusUnauthorized, rec.Code)

	cookie, _ := loginSession(t, handler)
	rec = doJSON(t, handler, cookie, "", http.MethodGet, "/api/v1/llm/status", "")
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	require.NotContains(t, rec.Body.String(), `"api_key":`)
	require.NotContains(t, rec.Body.String(), "sk-")
	var status llm.Status
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &status))
	require.Equal(t, service.status, status)
}

func TestLLMSettingsSaveAndClear(t *testing.T) {
	service := &fakeLLM{status: llm.Status{Configured: true, Provider: llm.ProviderAnthropic}}
	handler, cookie, csrf := newLLMTestAPI(t, service)

	payload := `{"provider":"anthropic","base_url":"https://api.anthropic.com/v1","model":"claude-test","api_key":"sk-secret"}`
	rec := doJSON(t, handler, cookie, "", http.MethodPut, "/api/v1/llm/settings", payload)
	require.Equal(t, http.StatusForbidden, rec.Code)

	rec = doJSON(t, handler, cookie, csrf, http.MethodPut, "/api/v1/llm/settings", payload)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	require.NotNil(t, service.saved)
	require.Equal(t, "sk-secret", service.saved.APIKey)
	require.Equal(t, "claude-test", service.saved.Model)
	require.NotContains(t, rec.Body.String(), "sk-secret")

	rec = doJSON(t, handler, cookie, csrf, http.MethodDelete, "/api/v1/llm/settings", "")
	require.Equal(t, http.StatusNoContent, rec.Code)
	require.Equal(t, 1, service.clearCalls)

	rec = doJSON(t, handler, cookie, csrf, http.MethodPut, "/api/v1/llm/settings", `not-json`)
	require.Equal(t, http.StatusBadRequest, rec.Code)
}

func TestLLMTestEndpoint(t *testing.T) {
	service := &fakeLLM{status: llm.Status{Configured: true, Provider: llm.ProviderOpenAI, Model: "gpt"}}
	handler, cookie, csrf := newLLMTestAPI(t, service)

	rec := doJSON(t, handler, cookie, csrf, http.MethodPost, "/api/v1/llm/test", "")
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	require.Equal(t, 1, service.testCalls)
	require.Contains(t, rec.Body.String(), `"configured":true`)

	service.testErr = llm.ErrProvider
	rec = doJSON(t, handler, cookie, csrf, http.MethodPost, "/api/v1/llm/test", "")
	require.Equal(t, http.StatusBadGateway, rec.Code)
	require.Contains(t, rec.Body.String(), "llm_error")
}

func TestAssistantSummarize(t *testing.T) {
	service := &fakeLLM{summary: llm.SummaryResult{Summary: "- one", Messages: 2}}
	handler, cookie, csrf := newLLMTestAPI(t, service)

	rec := doJSON(t, handler, cookie, "", http.MethodPost, "/api/v1/assistant/summarize", `{"message_ids":["a"]}`)
	require.Equal(t, http.StatusForbidden, rec.Code)

	rec = doJSON(t, handler, cookie, csrf, http.MethodPost, "/api/v1/assistant/summarize", `{"message_ids":["a","b"]}`)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	require.Equal(t, []string{"a", "b"}, service.summarizeIDs)
	require.JSONEq(t, `{"summary":"- one","messages":2}`, rec.Body.String())

	service.summarizeErr = llm.ErrNotConfigured
	rec = doJSON(t, handler, cookie, csrf, http.MethodPost, "/api/v1/assistant/summarize", `{"message_ids":["a"]}`)
	require.Equal(t, http.StatusConflict, rec.Code)
	require.Contains(t, rec.Body.String(), "llm_not_configured")

	service.summarizeErr = nil
	rec = doJSON(t, handler, cookie, csrf, http.MethodPost, "/api/v1/assistant/summarize", `{"message_ids":["a"],"extra":true}`)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
}

func TestAssistantSummarizeRejectsOversizedRequest(t *testing.T) {
	service := &fakeLLM{}
	handler, cookie, csrf := newLLMTestAPI(t, service)

	ids := make([]string, assistantMaxRequestIDs+1)
	for i := range ids {
		ids[i] = "id"
	}
	payload, err := json.Marshal(assistantSummarizeRequest{MessageIDs: ids})
	require.NoError(t, err)
	rec := doJSON(t, handler, cookie, csrf, http.MethodPost, "/api/v1/assistant/summarize", string(payload))
	require.Equal(t, http.StatusBadRequest, rec.Code)
	require.Nil(t, service.summarizeIDs)
}

func TestAssistantDraftReply(t *testing.T) {
	service := &fakeLLM{draft: llm.Draft{Subject: "Re: Hi", Body: "Hello", To: "alice@example.com"}}
	handler, cookie, csrf := newLLMTestAPI(t, service)

	rec := doJSON(t, handler, cookie, csrf, http.MethodPost, "/api/v1/assistant/draft-reply",
		`{"message_id":"m1","instructions":"Be brief"}`)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	require.Equal(t, "m1", service.draftID)
	require.Equal(t, "Be brief", service.draftNotes)
	require.JSONEq(t, `{"subject":"Re: Hi","body":"Hello","to":"alice@example.com"}`, rec.Body.String())
}

func TestLLMErrorMapping(t *testing.T) {
	cases := []struct {
		name   string
		err    error
		status int
		code   string
	}{
		{"invalid request", errors.Join(llm.ErrInvalidRequest, errors.New("bad id")), http.StatusBadRequest, "invalid_request"},
		{"invalid settings", errors.Join(llm.ErrInvalidSettings, errors.New("bad model")), http.StatusBadRequest, "invalid_request"},
		{"not configured", llm.ErrNotConfigured, http.StatusConflict, "llm_not_configured"},
		{"google not configured", gmail.ErrNotConfigured, http.StatusConflict, "google_not_configured"},
		{"google not connected", gmail.ErrNotConnected, http.StatusConflict, "google_not_connected"},
		{"cancelled", context.Canceled, http.StatusRequestTimeout, "request_cancelled"},
		{"provider", llm.ErrProvider, http.StatusBadGateway, "llm_error"},
		{"unexpected", errors.New("boom"), http.StatusInternalServerError, "internal_error"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			service := &fakeLLM{draftErr: tc.err}
			handler, cookie, csrf := newLLMTestAPI(t, service)
			rec := doJSON(t, handler, cookie, csrf, http.MethodPost, "/api/v1/assistant/draft-reply",
				`{"message_id":"m1"}`)
			require.Equal(t, tc.status, rec.Code, rec.Body.String())
			require.Contains(t, rec.Body.String(), tc.code)
		})
	}
}

func TestLLMRoutesAbsentWithoutService(t *testing.T) {
	_, handler := newTestAPIWithDeps(t, Deps{})
	cookie, _ := loginSession(t, handler)
	rec := doJSON(t, handler, cookie, "", http.MethodGet, "/api/v1/llm/status", "")
	require.Equal(t, http.StatusNotFound, rec.Code)
}

func TestLLMMessageStripsInternalPrefix(t *testing.T) {
	err := fmt.Errorf("%w: model is required", llm.ErrInvalidSettings)
	require.Equal(t, "model is required", llmMessage(err))
}
