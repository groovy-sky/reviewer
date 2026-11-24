package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/groovy-sky/reviewer/internal/config"
)

func TestClientHandlesSSEAndSessionHeaders(t *testing.T) {
	t.Helper()
	const sessionID = "session-123"
	var sawSession, sawProtocol bool

	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, httpRequest *http.Request) {
		if httpRequest.Header.Get("Mcp-Session-Id") == sessionID {
			sawSession = true
		}
		if httpRequest.Header.Get("MCP-Protocol-Version") == config.DefaultProtocolVersion {
			sawProtocol = true
		}
		var envelope request
		if err := json.NewDecoder(httpRequest.Body).Decode(&envelope); err != nil {
			t.Errorf("decode request: %v", err)
			return
		}
		switch envelope.Method {
		case "initialize":
			writer.Header().Set("Mcp-Session-Id", sessionID)
			writeSSE(writer, envelope.ID, map[string]any{
				"protocolVersion": config.DefaultProtocolVersion,
				"capabilities":    map[string]any{"tools": map[string]any{}},
				"serverInfo":      map[string]any{"name": "test", "version": "1"},
			})
		case "notifications/initialized":
			writer.WriteHeader(http.StatusAccepted)
		case "tools/list":
			if httpRequest.Header.Get("Mcp-Session-Id") != sessionID {
				t.Errorf("missing session header on tools/list")
			}
			writeJSON(writer, envelope.ID, map[string]any{
				"tools": []map[string]any{{
					"name":        "search_updates",
					"description": "Search updates",
					"inputSchema": map[string]any{
						"type":       "object",
						"properties": map[string]any{"query": map[string]any{"type": "string"}},
						"required":   []string{"query"},
					},
				}},
			})
		case "tools/call":
			writeJSON(writer, envelope.ID, map[string]any{
				"content": []map[string]any{{"type": "text", "text": "confirmed"}},
			})
		default:
			t.Errorf("unexpected method %q", envelope.Method)
		}
	}))
	defer server.Close()

	client, err := NewClient(config.Server{
		Name:            "test",
		URL:             server.URL,
		ProtocolVersion: config.DefaultProtocolVersion,
	}, server.Client())
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if err := client.Initialize(ctx); err != nil {
		t.Fatal(err)
	}
	tools, err := client.ListTools(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(tools) != 1 {
		t.Fatalf("expected one tool, got %d", len(tools))
	}
	result, err := client.CallTool(ctx, "search_updates", map[string]any{"query": "KB123456"})
	if err != nil {
		t.Fatal(err)
	}
	if result.Text() != "confirmed" {
		t.Fatalf("unexpected tool text %q", result.Text())
	}
	if !sawSession || !sawProtocol {
		t.Fatalf("subsequent requests did not preserve session/protocol headers: session=%v protocol=%v", sawSession, sawProtocol)
	}
}

func TestToolResultUsesStructuredContent(t *testing.T) {
	result := ToolResult{StructuredContent: json.RawMessage(`{"value":"confirmed"}`)}
	if result.Text() != "{\n  \"value\": \"confirmed\"\n}" {
		t.Fatalf("unexpected structured text: %q", result.Text())
	}
}

func writeSSE(writer http.ResponseWriter, id *int64, result any) {
	writer.Header().Set("Content-Type", "text/event-stream")
	_, _ = fmt.Fprintf(writer, "event: message\ndata: ")
	_ = json.NewEncoder(writer).Encode(map[string]any{"jsonrpc": "2.0", "id": id, "result": result})
	_, _ = fmt.Fprint(writer, "\n")
}

func writeJSON(writer http.ResponseWriter, id *int64, result any) {
	writer.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(writer).Encode(map[string]any{"jsonrpc": "2.0", "id": id, "result": result})
}
