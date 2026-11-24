package modeltest

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRunWritesModelResponse(t *testing.T) {
	var requestBody struct {
		Model    string `json:"model"`
		Messages []struct {
			Role    string `json:"role"`
			Content string `json:"content"`
		} `json:"messages"`
	}
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if err := json.NewDecoder(request.Body).Decode(&requestBody); err != nil {
			t.Error(err)
		}
		_, _ = writer.Write([]byte(`{"choices":[{"message":{"content":"  model output  "}}]}`))
	}))
	defer server.Close()

	root := t.TempDir()
	article := filepath.Join(root, "article.txt")
	output := filepath.Join(root, "nested", "result.md")
	if err := os.WriteFile(article, []byte("Important source article."), 0o644); err != nil {
		t.Fatal(err)
	}
	err := Run(Options{
		Model: "test-model", Article: article, Output: output, URL: server.URL,
		HTTPClient: server.Client(),
	})
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(output)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "# test-model\n\nmodel output\n" {
		t.Fatalf("unexpected output %q", data)
	}
	if requestBody.Model != "test-model" || len(requestBody.Messages) != 1 {
		t.Fatalf("unexpected request: %+v", requestBody)
	}
	if !strings.Contains(requestBody.Messages[0].Content, "Important source article.") {
		t.Fatalf("article missing from prompt: %q", requestBody.Messages[0].Content)
	}
}

func TestRunRejectsResponseWithoutText(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		_, _ = writer.Write([]byte(`{"choices":[]}`))
	}))
	defer server.Close()
	article := filepath.Join(t.TempDir(), "article.txt")
	if err := os.WriteFile(article, []byte("article"), 0o644); err != nil {
		t.Fatal(err)
	}
	err := Run(Options{
		Model: "test", Article: article, Output: filepath.Join(t.TempDir(), "output.md"),
		URL: server.URL, HTTPClient: server.Client(),
	})
	if err == nil || !strings.Contains(err.Error(), "model returned no text") {
		t.Fatalf("unexpected error %v", err)
	}
}
