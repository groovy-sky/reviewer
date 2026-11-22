package research

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/groovy-sky/reviewer/internal/config"
	"github.com/groovy-sky/reviewer/internal/mcp"
	"github.com/groovy-sky/reviewer/internal/update"
)

type fakeClient struct {
	server  config.Server
	tools   []mcp.Tool
	results map[string][]mcp.ToolResult
	calls   []call
}

type call struct {
	name      string
	arguments map[string]any
}

func (f *fakeClient) Server() config.Server {
	return f.server
}

func (f *fakeClient) Initialize(context.Context) error {
	return nil
}

func (f *fakeClient) ListTools(context.Context) ([]mcp.Tool, error) {
	return f.tools, nil
}

func (f *fakeClient) CallTool(_ context.Context, name string, arguments any) (mcp.ToolResult, error) {
	converted, ok := arguments.(map[string]any)
	if !ok {
		return mcp.ToolResult{}, nil
	}
	f.calls = append(f.calls, call{name: name, arguments: converted})
	results := f.results[name]
	if len(results) == 0 {
		return mcp.ToolResult{}, nil
	}
	result := results[0]
	f.results[name] = results[1:]
	return result, nil
}

func TestGatherFollowsMCPReleaseIDs(t *testing.T) {
	server := config.Server{
		Name:  "release",
		URL:   "https://www.microsoft.com/releasecommunications/mcp",
		Tools: []string{"get_recent_azure_updates", "get_azure_update_by_id"},
	}
	client := &fakeClient{
		server: server,
		tools: []mcp.Tool{
			tool("get_recent_azure_updates", "search"),
			tool("get_azure_update_by_id", "id"),
		},
		results: map[string][]mcp.ToolResult{
			"get_recent_azure_updates": {textResult(`{"items":[{"id":"467931","title":"Azure update"}]}`)},
			"get_azure_update_by_id":   {textResult(`{"id":"467931","description":"General availability"}`)},
		},
	}
	service := Service{
		Servers: []config.Server{server},
		Factory: func(config.Server, *http.Client) (MCPClient, error) {
			return client, nil
		},
	}
	evidence, err := service.Gather(context.Background(), update.Document{
		Text:  "Review Azure update 467931",
		Query: "Azure update 467931",
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(evidence) != 2 {
		t.Fatalf("expected search plus detail evidence, got %d", len(evidence))
	}
	if client.calls[0].arguments["search"] != "Azure update 467931" {
		t.Fatalf("search query was not supplied: %#v", client.calls[0].arguments)
	}
	if client.calls[1].name != "get_azure_update_by_id" || client.calls[1].arguments["id"] != "467931" {
		t.Fatalf("detail lookup did not use extracted id: %#v", client.calls[1])
	}
	if evidence[0].ID != "S1" || evidence[1].ID != "S2" {
		t.Fatalf("evidence IDs were not assigned: %#v", evidence)
	}
}

func TestGatherFetchesDiscoveredMicrosoftLearnPage(t *testing.T) {
	server := config.Server{
		Name:  "learn",
		URL:   "https://learn.microsoft.com/api/mcp",
		Tools: []string{"microsoft_docs_search", "microsoft_docs_fetch"},
	}
	client := &fakeClient{
		server: server,
		tools: []mcp.Tool{
			tool("microsoft_docs_search", "query"),
			tool("microsoft_docs_fetch", "url"),
		},
		results: map[string][]mcp.ToolResult{
			"microsoft_docs_search": {textResult(`{"url":"https://learn.microsoft.com/azure/example"}`)},
			"microsoft_docs_fetch":  {textResult("Full documentation")},
		},
	}
	evidence, err := (Service{
		Servers: []config.Server{server},
		Factory: func(config.Server, *http.Client) (MCPClient, error) {
			return client, nil
		},
	}).Gather(context.Background(), update.Document{Text: "new Azure behavior", Query: "new Azure behavior"})
	if err != nil {
		t.Fatal(err)
	}
	if len(evidence) != 2 {
		t.Fatalf("expected search and fetch evidence, got %d", len(evidence))
	}
	if client.calls[1].arguments["url"] != "https://learn.microsoft.com/azure/example" {
		t.Fatalf("fetch URL mismatch: %#v", client.calls[1].arguments)
	}
}

func TestGatherSelectsEvidenceAcrossServers(t *testing.T) {
	learn := config.Server{Name: "learn", URL: "https://learn.microsoft.com/api/mcp"}
	release := config.Server{Name: "release", URL: "https://www.microsoft.com/releasecommunications/mcp"}
	for _, test := range []struct {
		name       string
		identifier string
		wantTools  []string
	}{
		{
			name:      "second server contributes before the first repeats",
			wantTools: []string{"microsoft_docs_search", "get_recent_azure_updates", "microsoft_docs_fetch"},
		},
		{
			name:       "direct lookup wins the only slot",
			identifier: "467931",
			wantTools:  []string{"get_azure_update_by_id"},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			clients := map[string]*fakeClient{
				learn.Name: {
					server: learn,
					tools: []mcp.Tool{
						tool("microsoft_docs_search", "query"),
						tool("microsoft_docs_fetch", "url"),
					},
					results: map[string][]mcp.ToolResult{
						"microsoft_docs_search": {textResult(`{"url":"https://learn.microsoft.com/azure/example"}`)},
						"microsoft_docs_fetch":  {textResult("Full documentation")},
					},
				},
				release.Name: {
					server: release,
					tools: []mcp.Tool{
						tool("get_recent_azure_updates", "search"),
						tool("get_azure_update_by_id", "id"),
					},
					results: map[string][]mcp.ToolResult{
						"get_recent_azure_updates": {textResult("Recent Azure changes")},
						"get_azure_update_by_id":   {textResult(`{"id":"467931","description":"General availability"}`)},
					},
				},
			}
			service := Service{
				Servers:     []config.Server{learn, release},
				MaxEvidence: len(test.wantTools),
				Factory: func(server config.Server, _ *http.Client) (MCPClient, error) {
					return clients[server.Name], nil
				},
			}
			evidence, err := service.Gather(context.Background(), update.Document{
				Text:       "Azure update",
				Query:      "Azure update",
				Identifier: test.identifier,
			})
			if err != nil {
				t.Fatal(err)
			}
			if len(evidence) != len(test.wantTools) {
				t.Fatalf("expected %d evidence blocks, got %d", len(test.wantTools), len(evidence))
			}
			for i, want := range test.wantTools {
				if evidence[i].Tool != want {
					t.Errorf("evidence[%d] tool = %q, want %q", i, evidence[i].Tool, want)
				}
			}
		})
	}
}

func TestGatherContinuesAfterServerFailureUnlessStrict(t *testing.T) {
	good := config.Server{Name: "good", URL: "https://learn.microsoft.com/api/mcp", Tools: []string{"search"}}
	bad := config.Server{Name: "bad", URL: "https://www.microsoft.com/mcp"}
	goodClient := &fakeClient{
		server:  good,
		tools:   []mcp.Tool{tool("search", "query")},
		results: map[string][]mcp.ToolResult{"search": {textResult("official result")}},
	}
	warnings := &strings.Builder{}
	service := Service{
		Servers:  []config.Server{bad, good},
		Warnings: warnings,
		Factory: func(server config.Server, _ *http.Client) (MCPClient, error) {
			if server.Name == "bad" {
				return nil, io.ErrUnexpectedEOF
			}
			return goodClient, nil
		},
	}
	evidence, err := service.Gather(context.Background(), update.Document{Text: "update", Query: "update"})
	if err != nil {
		t.Fatal(err)
	}
	if len(evidence) != 1 || !strings.Contains(warnings.String(), "bad") {
		t.Fatalf("partial failure was not handled: evidence=%d warnings=%q", len(evidence), warnings.String())
	}

	service.Strict = true
	if _, err := service.Gather(context.Background(), update.Document{Text: "update", Query: "update"}); err == nil {
		t.Fatal("expected strict mode to fail")
	}
}

func tool(name, argument string) mcp.Tool {
	return mcp.Tool{
		Name: name,
		InputSchema: []byte(`{
			"type":"object",
			"properties":{"` + argument + `":{"type":"string"}},
			"required":["` + argument + `"]
		}`),
	}
}

func textResult(text string) mcp.ToolResult {
	return mcp.ToolResult{Content: []mcp.Content{{Type: "text", Text: text}}}
}
