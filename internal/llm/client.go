package llm

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
)

const (
	openAIChatPath   = "/chat/completions"
	anthropicPath    = "/messages"
	anthropicVersion = "2023-06-01"
)

// Client speaks either the OpenAI chat-completions protocol or the Anthropic
// messages protocol. It never logs prompts, responses or credentials.
type Client struct {
	provider string
	baseURL  string
	model    string
	apiKey   string
	http     *http.Client
}

func newClient(cfg Settings, httpClient *http.Client) *Client {
	if httpClient == nil {
		httpClient = &http.Client{Timeout: defaultHTTPTimeout}
	}
	return &Client{
		provider: cfg.Provider,
		baseURL:  strings.TrimRight(cfg.BaseURL, "/"),
		model:    cfg.Model,
		apiKey:   cfg.APIKey,
		http:     httpClient,
	}
}

type openAIRequest struct {
	Model       string          `json:"model"`
	Messages    []openAIMessage `json:"messages"`
	MaxTokens   int             `json:"max_tokens"`
	Temperature float64         `json:"temperature"`
}

type openAIMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type openAIResponse struct {
	Choices []struct {
		Message struct {
			Content string `json:"content"`
		} `json:"message"`
	} `json:"choices"`
}

type anthropicRequest struct {
	Model       string             `json:"model"`
	System      string             `json:"system"`
	Messages    []anthropicMessage `json:"messages"`
	MaxTokens   int                `json:"max_tokens"`
	Temperature float64            `json:"temperature"`
}

type anthropicMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type anthropicResponse struct {
	Content []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	} `json:"content"`
}

// complete runs one system+user completion and returns the assistant text.
func (c *Client) complete(ctx context.Context, system, user string, maxTokens int) (string, error) {
	if c == nil || c.http == nil {
		return "", fmt.Errorf("%w: client is not initialized", ErrProvider)
	}
	if maxTokens <= 0 {
		maxTokens = defaultMaxOutputTokens
	}
	if c.provider == ProviderAnthropic {
		return c.completeAnthropic(ctx, system, user, maxTokens)
	}
	return c.completeOpenAI(ctx, system, user, maxTokens)
}

func (c *Client) completeOpenAI(ctx context.Context, system, user string, maxTokens int) (string, error) {
	payload := openAIRequest{
		Model: c.model,
		Messages: []openAIMessage{
			{Role: "system", Content: system},
			{Role: "user", Content: user},
		},
		MaxTokens:   maxTokens,
		Temperature: 0.2,
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return "", fmt.Errorf("%w: encode request", ErrProvider)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+openAIChatPath, bytes.NewReader(body))
	if err != nil {
		return "", fmt.Errorf("%w: build request", ErrProvider)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Authorization", "Bearer "+c.apiKey)

	var response openAIResponse
	if err := c.do(req, &response); err != nil {
		return "", err
	}
	if len(response.Choices) == 0 {
		return "", fmt.Errorf("%w: empty response", ErrProvider)
	}
	return response.Choices[0].Message.Content, nil
}

func (c *Client) completeAnthropic(ctx context.Context, system, user string, maxTokens int) (string, error) {
	payload := anthropicRequest{
		Model:  c.model,
		System: system,
		Messages: []anthropicMessage{
			{Role: "user", Content: user},
		},
		MaxTokens:   maxTokens,
		Temperature: 0.2,
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return "", fmt.Errorf("%w: encode request", ErrProvider)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+anthropicPath, bytes.NewReader(body))
	if err != nil {
		return "", fmt.Errorf("%w: build request", ErrProvider)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	req.Header.Set("x-api-key", c.apiKey)
	req.Header.Set("anthropic-version", anthropicVersion)

	var response anthropicResponse
	if err := c.do(req, &response); err != nil {
		return "", err
	}
	var text strings.Builder
	for _, block := range response.Content {
		if block.Type == "text" {
			text.WriteString(block.Text)
		}
	}
	return text.String(), nil
}

func (c *Client) do(req *http.Request, out any) error {
	response, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("%w: request failed", ErrProvider)
	}
	defer response.Body.Close()
	limited := io.LimitReader(response.Body, maxResponseBytes)
	if response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices {
		// Drain a bounded amount so the connection can be reused, but never
		// surface provider bodies: they may contain prompt fragments.
		_, _ = io.Copy(io.Discard, limited)
		return fmt.Errorf("%w: %s returned HTTP %d", ErrProvider, c.provider, response.StatusCode)
	}
	if err := json.NewDecoder(limited).Decode(out); err != nil {
		return fmt.Errorf("%w: decode response", ErrProvider)
	}
	return nil
}
