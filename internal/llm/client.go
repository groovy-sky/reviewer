package llm

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
)

const maxResponseBytes = 4 << 20

type Client struct {
	baseURL     *url.URL
	model       string
	apiKey      string
	temperature float64
	maxTokens   int
	http        *http.Client
}

type Options struct {
	BaseURL     string
	Model       string
	APIKey      string
	Temperature float64
	MaxTokens   int
	HTTPClient  *http.Client
}

type Message struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

func New(options Options) (*Client, error) {
	if strings.TrimSpace(options.BaseURL) == "" {
		return nil, fmt.Errorf("LLM base URL is required")
	}
	parsed, err := url.Parse(options.BaseURL)
	if err != nil {
		return nil, fmt.Errorf("parse LLM base URL: %w", err)
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return nil, fmt.Errorf("LLM base URL must use http or https")
	}
	if parsed.Host == "" {
		return nil, fmt.Errorf("LLM base URL must include a host")
	}
	if options.Model == "" {
		return nil, fmt.Errorf("LLM model is required")
	}
	if options.MaxTokens <= 0 {
		options.MaxTokens = 2048
	}
	if options.HTTPClient == nil {
		options.HTTPClient = http.DefaultClient
	}
	return &Client{
		baseURL:     parsed,
		model:       options.Model,
		apiKey:      options.APIKey,
		temperature: options.Temperature,
		maxTokens:   options.MaxTokens,
		http:        options.HTTPClient,
	}, nil
}

func (c *Client) Complete(ctx context.Context, system, user string) (string, error) {
	requestBody := struct {
		Model       string    `json:"model"`
		Messages    []Message `json:"messages"`
		Temperature float64   `json:"temperature"`
		MaxTokens   int       `json:"max_tokens"`
		Stream      bool      `json:"stream"`
	}{
		Model: c.model,
		Messages: []Message{
			{Role: "user", Content: strings.TrimSpace(system) + "\n\n" + strings.TrimSpace(user)},
		},
		Temperature: c.temperature,
		MaxTokens:   c.maxTokens,
		Stream:      false,
	}
	payload, err := json.Marshal(requestBody)
	if err != nil {
		return "", fmt.Errorf("encode LLM request: %w", err)
	}

	request, err := http.NewRequestWithContext(ctx, http.MethodPost, c.chatURL(), bytes.NewReader(payload))
	if err != nil {
		return "", fmt.Errorf("create LLM request: %w", err)
	}
	c.setHeaders(request)

	response, err := c.http.Do(request)
	if err != nil {
		return "", fmt.Errorf("POST LLM chat completion: %w", err)
	}
	defer response.Body.Close()
	data, err := io.ReadAll(io.LimitReader(response.Body, maxResponseBytes+1))
	if err != nil {
		return "", fmt.Errorf("read LLM response: %w", err)
	}
	if len(data) > maxResponseBytes {
		return "", fmt.Errorf("LLM response exceeds %d bytes", maxResponseBytes)
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return "", fmt.Errorf("LLM returned %s: %s", response.Status, strings.TrimSpace(string(data)))
	}

	var completion struct {
		Choices []struct {
			Message struct {
				Content json.RawMessage `json:"content"`
			} `json:"message"`
		} `json:"choices"`
		Error *struct {
			Message string `json:"message"`
		} `json:"error,omitempty"`
	}
	if err := json.Unmarshal(data, &completion); err != nil {
		return "", fmt.Errorf("decode LLM response: %w", err)
	}
	if completion.Error != nil {
		return "", fmt.Errorf("LLM response: %s", completion.Error.Message)
	}
	if len(completion.Choices) == 0 {
		return "", fmt.Errorf("LLM response contains no choices")
	}
	content, err := decodeContent(completion.Choices[0].Message.Content)
	if err != nil {
		return "", err
	}
	if strings.TrimSpace(content) == "" {
		return "", fmt.Errorf("LLM response contains empty content")
	}
	return strings.TrimSpace(content), nil
}

func (c *Client) Health(ctx context.Context) error {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, c.healthURL(), nil)
	if err != nil {
		return fmt.Errorf("create LLM health request: %w", err)
	}
	c.setHeaders(request)
	response, err := c.http.Do(request)
	if err != nil {
		return fmt.Errorf("GET LLM health: %w", err)
	}
	defer response.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, 8<<10))
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return fmt.Errorf("LLM health returned %s", response.Status)
	}
	return nil
}

func (c *Client) chatURL() string {
	return c.endpoint("/v1/chat/completions")
}

func (c *Client) healthURL() string {
	return c.endpoint("/health")
}

func (c *Client) endpoint(defaultPath string) string {
	base := *c.baseURL
	path := strings.TrimSuffix(base.Path, "/")
	switch {
	case path == "":
		base.Path = defaultPath
	case defaultPath != "/health" && strings.HasSuffix(path, "/v1"):
		base.Path = path + strings.TrimPrefix(defaultPath, "/v1")
	case defaultPath != "/health" && strings.HasSuffix(path, "/v1/chat/completions"):
		// Keep a fully specified completion endpoint.
	case defaultPath == "/health":
		root := strings.TrimSuffix(path, "/v1/chat/completions")
		root = strings.TrimSuffix(root, "/v1")
		base.Path = root + "/health"
	default:
		base.Path = path + defaultPath
	}
	return base.String()
}

func (c *Client) setHeaders(request *http.Request) {
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Accept", "application/json")
	request.Header.Set("User-Agent", "groovy-sky-reviewer/0.1.0")
	if c.apiKey != "" {
		request.Header.Set("Authorization", "Bearer "+c.apiKey)
	}
}

func decodeContent(raw json.RawMessage) (string, error) {
	if len(raw) == 0 {
		return "", nil
	}
	var text string
	if err := json.Unmarshal(raw, &text); err == nil {
		return text, nil
	}
	var parts []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	}
	if err := json.Unmarshal(raw, &parts); err == nil {
		values := make([]string, 0, len(parts))
		for _, part := range parts {
			if part.Type == "text" && part.Text != "" {
				values = append(values, part.Text)
			}
		}
		return strings.Join(values, "\n"), nil
	}
	return "", fmt.Errorf("LLM response has unsupported message content")
}
