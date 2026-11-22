package cli

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestReviewCommandEndToEnd(t *testing.T) {
	var toolQuery string
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		switch request.URL.Path {
		case "/health":
			writer.WriteHeader(http.StatusOK)
		case "/v1/chat/completions":
			writer.Header().Set("Content-Type", "application/json")
			_, _ = writer.Write([]byte(`{
				"choices":[{"message":{"content":"## Executive summary\nPilot first. [S1]\n\n## What changes\nConfirmed by official MCP evidence.\n"}}]
			}`))
		case "/mcp":
			var envelope struct {
				JSONRPC string          `json:"jsonrpc"`
				ID      json.RawMessage `json:"id"`
				Method  string          `json:"method"`
				Params  struct {
					Arguments map[string]any `json:"arguments"`
				} `json:"params"`
			}
			if err := json.NewDecoder(request.Body).Decode(&envelope); err != nil {
				t.Errorf("decode MCP request: %v", err)
				return
			}
			writer.Header().Set("Content-Type", "application/json")
			switch envelope.Method {
			case "initialize":
				_, _ = fmt.Fprintf(writer, `{"jsonrpc":"2.0","id":%s,"result":{"protocolVersion":"2025-06-18","serverInfo":{"name":"fake","version":"1"}}}`,
					envelope.ID)
			case "notifications/initialized":
				writer.WriteHeader(http.StatusAccepted)
			case "tools/list":
				_, _ = fmt.Fprintf(writer, `{"jsonrpc":"2.0","id":%s,"result":{"tools":[{
					"name":"search_updates",
					"description":"Search official updates",
					"inputSchema":{"type":"object","properties":{"query":{"type":"string"}},"required":["query"]}
				}]}}`, envelope.ID)
			case "tools/call":
				toolQuery, _ = envelope.Params.Arguments["query"].(string)
				_, _ = fmt.Fprintf(writer, `{"jsonrpc":"2.0","id":%s,"result":{"content":[{"type":"text","text":"Official update details"}]}}`,
					envelope.ID)
			default:
				t.Errorf("unexpected MCP method %q", envelope.Method)
			}
		default:
			http.NotFound(writer, request)
		}
	}))
	defer server.Close()

	configPath := filepath.Join(t.TempDir(), "mcp.json")
	configData := fmt.Sprintf(`{
		"servers":[{
			"name":"fake-microsoft",
			"url":%q,
			"tools":["search_updates"]
		}]
	}`, server.URL+"/mcp")
	if err := os.WriteFile(configPath, []byte(configData), 0o600); err != nil {
		t.Fatal(err)
	}
	outputPath := filepath.Join(t.TempDir(), "review.md")
	stdout := &bytes.Buffer{}
	stderr := &bytes.Buffer{}
	exitCode := Run([]string{
		"review",
		"--mcp-config", configPath,
		"--llm-url", server.URL,
		"--model", "gemma-test",
		"--timeout", "10s",
		"--output", outputPath,
		"KB5035853",
	}, strings.NewReader(""), stdout, stderr)
	if exitCode != 0 {
		t.Fatalf("review exited %d: %s", exitCode, stderr.String())
	}
	if toolQuery != "KB5035853" {
		t.Fatalf("unexpected MCP query %q", toolQuery)
	}
	report, err := os.ReadFile(outputPath)
	if err != nil {
		t.Fatal(err)
	}
	for _, expected := range []string{
		"# Microsoft update review",
		"Pilot first. [S1]",
		"## Evidence consulted",
		"search_updates",
		"fake-microsoft",
	} {
		if !bytes.Contains(report, []byte(expected)) {
			t.Fatalf("report is missing %q:\n%s", expected, report)
		}
	}
}

func TestDoctorCommandChecksModelAndMCP(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		switch request.URL.Path {
		case "/health":
			writer.WriteHeader(http.StatusOK)
		case "/mcp":
			var envelope struct {
				Method string          `json:"method"`
				ID     json.RawMessage `json:"id"`
			}
			_ = json.NewDecoder(request.Body).Decode(&envelope)
			writer.Header().Set("Content-Type", "application/json")
			switch envelope.Method {
			case "initialize":
				_, _ = fmt.Fprintf(writer, `{"jsonrpc":"2.0","id":%s,"result":{"protocolVersion":"2025-06-18","serverInfo":{"name":"fake","version":"1"}}}`, envelope.ID)
			case "notifications/initialized":
				writer.WriteHeader(http.StatusAccepted)
			case "tools/list":
				_, _ = fmt.Fprintf(writer, `{"jsonrpc":"2.0","id":%s,"result":{"tools":[{"name":"search_updates","inputSchema":{"type":"object","properties":{"query":{"type":"string"}},"required":["query"]}}]}}`, envelope.ID)
			}
		default:
			http.NotFound(writer, request)
		}
	}))
	defer server.Close()
	configPath := filepath.Join(t.TempDir(), "mcp.json")
	data := fmt.Sprintf(`{"servers":[{"name":"fake","url":%q,"tools":["search_updates"]}]}`, server.URL+"/mcp")
	if err := os.WriteFile(configPath, []byte(data), 0o600); err != nil {
		t.Fatal(err)
	}
	stdout := &bytes.Buffer{}
	stderr := &bytes.Buffer{}
	exitCode := Run([]string{
		"doctor",
		"--mcp-config", configPath,
		"--llm-url", server.URL,
		"--timeout", "10s",
	}, strings.NewReader(""), stdout, stderr)
	if exitCode != 0 {
		t.Fatalf("doctor exited %d: stdout=%s stderr=%s", exitCode, stdout.String(), stderr.String())
	}
	for _, expected := range []string{"[OK] llama.cpp", "[OK] fake: 1 usable tools"} {
		if !strings.Contains(stdout.String(), expected) {
			t.Fatalf("doctor output missing %q: %s", expected, stdout.String())
		}
	}
}

func TestRunReportsInvalidEnvironment(t *testing.T) {
	t.Setenv("REVIEWER_TIMEOUT", "eventually")
	stdout := &bytes.Buffer{}
	stderr := &bytes.Buffer{}
	exitCode := Run([]string{"review", "KB123456"}, strings.NewReader(""), stdout, stderr)
	if exitCode != 1 || !strings.Contains(stderr.String(), "REVIEWER_TIMEOUT must be a duration") {
		t.Fatalf("invalid environment did not fail clearly: code=%d stderr=%q", exitCode, stderr.String())
	}
}
