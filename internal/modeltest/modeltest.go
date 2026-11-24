package modeltest

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
)

const maxResponseBytes = 4 << 20

type Options struct {
	Model      string
	Article    string
	Output     string
	URL        string
	HTTPClient *http.Client
}

func Run(options Options) error {
	article, err := os.ReadFile(options.Article)
	if err != nil {
		return fmt.Errorf("read article: %w", err)
	}
	prompt := fmt.Sprintf(`Read the article below and produce exactly these three sections:

## Short summary
A concise factual summary.

## Why the announcement matters
Explain the practical importance without marketing hype.

## Sarcastic announcement
Rewrite the announcement as a short, clearly sarcastic version.

ARTICLE:
%s
`, article)
	payload, err := json.Marshal(struct {
		Model    string `json:"model"`
		Messages []struct {
			Role    string `json:"role"`
			Content string `json:"content"`
		} `json:"messages"`
		Temperature float64 `json:"temperature"`
		MaxTokens   int     `json:"max_tokens"`
		Stream      bool    `json:"stream"`
	}{
		Model: options.Model,
		Messages: []struct {
			Role    string `json:"role"`
			Content string `json:"content"`
		}{{Role: "user", Content: prompt}},
		Temperature: 0.35,
		MaxTokens:   900,
		Stream:      false,
	})
	if err != nil {
		return fmt.Errorf("encode model request: %w", err)
	}
	request, err := http.NewRequest(http.MethodPost, options.URL, bytes.NewReader(payload))
	if err != nil {
		return fmt.Errorf("create model request: %w", err)
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Accept", "application/json")
	client := options.HTTPClient
	if client == nil {
		client = &http.Client{Timeout: 300 * time.Second}
	}
	response, err := client.Do(request)
	if err != nil {
		return fmt.Errorf("model request failed: %w", err)
	}
	defer response.Body.Close()
	data, err := io.ReadAll(io.LimitReader(response.Body, maxResponseBytes+1))
	if err != nil {
		return fmt.Errorf("read model response: %w", err)
	}
	if len(data) > maxResponseBytes {
		return fmt.Errorf("model response exceeds %d bytes", maxResponseBytes)
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return fmt.Errorf("model request returned %s: %s", response.Status, strings.TrimSpace(string(data)))
	}
	var result struct {
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
	}
	if err := json.Unmarshal(data, &result); err != nil {
		return fmt.Errorf("decode model response: %w", err)
	}
	if len(result.Choices) == 0 || strings.TrimSpace(result.Choices[0].Message.Content) == "" {
		return fmt.Errorf("model returned no text: %s", strings.TrimSpace(string(data)))
	}
	if err := os.MkdirAll(filepath.Dir(options.Output), 0o755); err != nil {
		return fmt.Errorf("create output directory: %w", err)
	}
	content := fmt.Sprintf("# %s\n\n%s\n", options.Model, strings.TrimSpace(result.Choices[0].Message.Content))
	if err := os.WriteFile(options.Output, []byte(content), 0o644); err != nil {
		return fmt.Errorf("write output: %w", err)
	}
	return nil
}
