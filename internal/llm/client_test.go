package llm

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestCompleteUsesChatCompletions(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.URL.Path != "/v1/chat/completions" {
			t.Errorf("unexpected path %s", request.URL.Path)
		}
		if got := request.Header.Get("Authorization"); got != "Bearer test-token" {
			t.Errorf("unexpected authorization %q", got)
		}
		var body struct {
			Model       string    `json:"model"`
			Messages    []Message `json:"messages"`
			MaxTokens   int       `json:"max_tokens"`
			Temperature float64   `json:"temperature"`
		}
		if err := json.NewDecoder(request.Body).Decode(&body); err != nil {
			t.Error(err)
		}
		if body.Model != "gemma-test" || len(body.Messages) != 1 || body.MaxTokens != 512 {
			t.Errorf("unexpected request body: %+v", body)
		}
		if len(body.Messages) == 1 && !strings.Contains(body.Messages[0].Content, "system\n\nuser") {
			t.Errorf("system and user prompts were not combined: %q", body.Messages[0].Content)
		}
		writer.Header().Set("Content-Type", "application/json")
		_, _ = writer.Write([]byte(`{"choices":[{"message":{"content":"review complete"}}]}`))
	}))
	defer server.Close()

	client, err := New(Options{
		BaseURL:     server.URL,
		Model:       "gemma-test",
		APIKey:      "test-token",
		Temperature: 0.2,
		MaxTokens:   512,
		HTTPClient:  server.Client(),
	})
	if err != nil {
		t.Fatal(err)
	}
	answer, err := client.Complete(context.Background(), "system", "user")
	if err != nil {
		t.Fatal(err)
	}
	if answer != "review complete" {
		t.Fatalf("unexpected answer %q", answer)
	}
}

func TestHealthUsesHealthEndpoint(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.URL.Path != "/health" {
			t.Errorf("unexpected path %s", request.URL.Path)
		}
		writer.WriteHeader(http.StatusOK)
	}))
	defer server.Close()
	client, err := New(Options{BaseURL: server.URL + "/v1", Model: "gemma", HTTPClient: server.Client()})
	if err != nil {
		t.Fatal(err)
	}
	if err := client.Health(context.Background()); err != nil {
		t.Fatal(err)
	}
}
