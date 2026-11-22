package research

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/groovy-sky/reviewer/internal/config"
	"github.com/groovy-sky/reviewer/internal/mcp"
	"github.com/groovy-sky/reviewer/internal/update"
)

const (
	defaultMaxEvidence     = 6
	defaultMaxTextBytes    = 16 << 10
	defaultMaxCallsPerHost = 8
)

var (
	resultID   = regexp.MustCompile(`(?i)"(?:id|baseId)"\s*:\s*"?([0-9]{4,12})"?`)
	resultURL  = regexp.MustCompile(`https?://[^\s"'<>]+`)
	toolSuffix = regexp.MustCompile(`(?i)[?.,)\]}]+$`)
)

type MCPClient interface {
	Server() config.Server
	Initialize(context.Context) error
	ListTools(context.Context) ([]mcp.Tool, error)
	CallTool(context.Context, string, any) (mcp.ToolResult, error)
}

type ClientFactory func(config.Server, *http.Client) (MCPClient, error)

type Evidence struct {
	ID       string
	Server   string
	Endpoint string
	Tool     string
	Text     string
}

type Service struct {
	Servers           []config.Server
	HTTPClient        *http.Client
	Factory           ClientFactory
	Strict            bool
	MaxEvidence       int
	MaxTextBytes      int
	MaxCallsPerServer int
	Warnings          io.Writer
}

type plannedCall struct {
	Tool       mcp.Tool
	Arguments  map[string]any
	Score      int
	CallKey    string
	IsIDLookup bool
}

func (s Service) Gather(ctx context.Context, document update.Document) ([]Evidence, error) {
	servers := s.Servers
	maxEvidence := s.MaxEvidence
	if maxEvidence <= 0 {
		maxEvidence = defaultMaxEvidence
	}
	maxTextBytes := s.MaxTextBytes
	if maxTextBytes <= 0 {
		maxTextBytes = defaultMaxTextBytes
	}
	maxCalls := s.MaxCallsPerServer
	if maxCalls <= 0 {
		maxCalls = defaultMaxCallsPerHost
	}
	factory := s.Factory
	if factory == nil {
		factory = func(server config.Server, client *http.Client) (MCPClient, error) {
			return mcp.NewClient(server, client)
		}
	}

	var evidence []Evidence
	var failures []error
	for _, server := range servers {
		if !server.IsEnabled() {
			continue
		}
		client, err := factory(server, s.HTTPClient)
		if err != nil {
			err = fmt.Errorf("create MCP client: %w", err)
			if handleFailure(s.Strict, s.Warnings, server.Name, err) {
				return nil, err
			}
			failures = append(failures, err)
			continue
		}
		if err := client.Initialize(ctx); err != nil {
			err = fmt.Errorf("initialize: %w", err)
			if handleFailure(s.Strict, s.Warnings, server.Name, err) {
				return nil, err
			}
			failures = append(failures, err)
			continue
		}
		tools, err := client.ListTools(ctx)
		if err != nil {
			err = fmt.Errorf("list tools: %w", err)
			if handleFailure(s.Strict, s.Warnings, server.Name, err) {
				return nil, err
			}
			failures = append(failures, err)
			continue
		}

		tools = filterTools(tools, server.Tools)
		calls := planSearchCalls(tools, document, maxCalls)
		called := make(map[string]struct{}, len(calls))
		callCount := 0
		resolvedIdentifier := false
		byID := make(map[string]mcp.Tool)
		for _, tool := range tools {
			byName := strings.ToLower(tool.Name)
			if strings.Contains(byName, "by_id") || strings.HasSuffix(byName, "_get") {
				byID[byName] = tool
			}
		}

		for _, call := range calls {
			if len(evidence) >= maxEvidence || callCount >= maxCalls {
				break
			}
			if resolvedIdentifier && !call.IsIDLookup {
				continue
			}
			result, err := client.CallTool(ctx, call.Tool.Name, call.Arguments)
			callCount++
			called[call.CallKey] = struct{}{}
			if err != nil {
				err = fmt.Errorf("call %s: %w", call.Tool.Name, err)
				if handleFailure(s.Strict, s.Warnings, server.Name, err) {
					return nil, err
				}
				failures = append(failures, err)
				continue
			}
			text := truncate(result.Text(), maxTextBytes)
			if text == "" {
				continue
			}
			if call.IsIDLookup {
				resolvedIdentifier = true
			}
			evidence = append(evidence, makeEvidence(server, call.Tool.Name, text))

			followups := followupCalls(call.Tool, text, tools, byID, document, maxCalls-callCount)
			for _, followup := range followups {
				if len(evidence) >= maxEvidence || callCount >= maxCalls {
					break
				}
				if _, duplicate := called[followup.CallKey]; duplicate {
					continue
				}
				result, err := client.CallTool(ctx, followup.Tool.Name, followup.Arguments)
				callCount++
				called[followup.CallKey] = struct{}{}
				if err != nil {
					err = fmt.Errorf("call %s: %w", followup.Tool.Name, err)
					if handleFailure(s.Strict, s.Warnings, server.Name, err) {
						return nil, err
					}
					failures = append(failures, err)
					continue
				}
				text := truncate(result.Text(), maxTextBytes)
				if text != "" {
					evidence = append(evidence, makeEvidence(server, followup.Tool.Name, text))
				}
			}
		}
	}

	for index := range evidence {
		evidence[index].ID = "S" + strconv.Itoa(index+1)
	}
	if len(evidence) == 0 {
		if len(failures) == 0 {
			return nil, fmt.Errorf("Microsoft MCP servers returned no review evidence")
		}
		return nil, fmt.Errorf("Microsoft MCP research failed: %w", errors.Join(failures...))
	}
	return evidence, nil
}

func handleFailure(strict bool, warnings io.Writer, server string, err error) bool {
	wrapped := fmt.Errorf("MCP %s: %w", server, err)
	if strict {
		return true
	}
	if warnings != nil {
		_, _ = fmt.Fprintf(warnings, "warning: %v\n", wrapped)
	}
	return false
}

func filterTools(tools []mcp.Tool, allowlist []string) []mcp.Tool {
	if len(allowlist) == 0 {
		return tools
	}
	allowed := make(map[string]struct{}, len(allowlist))
	for _, name := range allowlist {
		allowed[strings.ToLower(name)] = struct{}{}
	}
	filtered := tools[:0]
	for _, tool := range tools {
		if _, ok := allowed[strings.ToLower(tool.Name)]; ok {
			filtered = append(filtered, tool)
		}
	}
	return filtered
}

func planSearchCalls(tools []mcp.Tool, document update.Document, maximum int) []plannedCall {
	if maximum <= 0 {
		return nil
	}
	calls := make([]plannedCall, 0, maximum)
	for _, tool := range tools {
		name := strings.ToLower(tool.Name)
		isIDLookup := strings.Contains(name, "by_id")
		if isIDLookup && (document.Identifier == "" || strings.HasPrefix(strings.ToUpper(document.Identifier), "KB")) {
			continue
		}
		if !isIDLookup && !isSearchTool(name) {
			continue
		}
		arguments, err := buildArguments(tool, document, isIDLookup)
		if err != nil {
			continue
		}
		score := searchToolScore(name, isIDLookup)
		callKey := tool.Name + ":" + stableArguments(arguments)
		calls = append(calls, plannedCall{
			Tool:       tool,
			Arguments:  arguments,
			Score:      score,
			CallKey:    callKey,
			IsIDLookup: isIDLookup,
		})
	}
	sort.SliceStable(calls, func(i, j int) bool {
		if calls[i].Score == calls[j].Score {
			return calls[i].Tool.Name < calls[j].Tool.Name
		}
		return calls[i].Score > calls[j].Score
	})
	limit := maximum
	if isLikelyIdentifier(document.Identifier) {
		limit = min(limit, 3)
	} else {
		limit = min(limit, 4)
	}
	return calls[:min(limit, len(calls))]
}

func followupCalls(
	primary mcp.Tool,
	text string,
	tools []mcp.Tool,
	byID map[string]mcp.Tool,
	document update.Document,
	remaining int,
) []plannedCall {
	if remaining <= 0 {
		return nil
	}
	primaryName := strings.ToLower(primary.Name)
	var followups []plannedCall

	if tool, ok := followupLookupTool(primaryName, byID); ok {
		ids := extractIDs(text)
		for _, id := range ids {
			arguments, err := buildArguments(tool, update.Document{Identifier: id}, true)
			if err != nil {
				continue
			}
			followups = append(followups, plannedCall{
				Tool:       tool,
				Arguments:  arguments,
				CallKey:    tool.Name + ":" + stableArguments(arguments),
				IsIDLookup: true,
			})
			if len(followups) >= 2 {
				break
			}
		}
	}

	if strings.Contains(primaryName, "docs_search") {
		for _, tool := range tools {
			if !strings.Contains(strings.ToLower(tool.Name), "docs_fetch") {
				continue
			}
			for _, rawURL := range extractURLs(text) {
				arguments, ok := buildURLArguments(tool, rawURL)
				if !ok {
					continue
				}
				followups = append(followups, plannedCall{
					Tool:      tool,
					Arguments: arguments,
					CallKey:   tool.Name + ":" + stableArguments(arguments),
				})
				if len(followups) >= 2 {
					return followups
				}
			}
		}
	}
	return followups
}

func followupLookupTool(primaryName string, tools map[string]mcp.Tool) (mcp.Tool, bool) {
	if strings.Contains(primaryName, "azure") {
		for name, tool := range tools {
			if strings.Contains(name, "azure") && strings.Contains(name, "by_id") {
				return tool, true
			}
		}
	}
	if strings.Contains(primaryName, "roadmap") || strings.Contains(primaryName, "m365") {
		for name, tool := range tools {
			if strings.Contains(name, "roadmap") && strings.Contains(name, "by_id") {
				return tool, true
			}
		}
	}
	return mcp.Tool{}, false
}

func buildArguments(tool mcp.Tool, document update.Document, byID bool) (map[string]any, error) {
	var schema struct {
		Properties map[string]json.RawMessage `json:"properties"`
		Required   []string                   `json:"required"`
	}
	if len(tool.InputSchema) == 0 {
		return nil, fmt.Errorf("tool %s has no input schema", tool.Name)
	}
	if err := json.Unmarshal(tool.InputSchema, &schema); err != nil {
		return nil, fmt.Errorf("decode schema for tool %s: %w", tool.Name, err)
	}

	arguments := make(map[string]any)
	names := make(map[string]string, len(schema.Properties))
	for original := range schema.Properties {
		names[strings.ToLower(original)] = original
	}
	if byID {
		for _, candidate := range []string{"id", "updateid", "roadmapid"} {
			if original, ok := names[candidate]; ok {
				arguments[original] = document.Identifier
				break
			}
		}
	} else {
		for _, candidate := range []string{"query", "search", "q", "term", "keywords", "question"} {
			if original, ok := names[candidate]; ok {
				arguments[original] = document.Query
				break
			}
		}
	}

	for _, required := range schema.Required {
		if _, ok := arguments[required]; !ok {
			return nil, fmt.Errorf("tool %s requires unsupported argument %s", tool.Name, required)
		}
	}
	if len(arguments) == 0 {
		return nil, fmt.Errorf("tool %s does not expose a supported query argument", tool.Name)
	}
	return arguments, nil
}

func buildURLArguments(tool mcp.Tool, rawURL string) (map[string]any, bool) {
	parsed, err := url.Parse(rawURL)
	if err != nil || parsed.Scheme != "https" {
		return nil, false
	}
	host := strings.ToLower(parsed.Hostname())
	if host != "learn.microsoft.com" && !strings.HasSuffix(host, ".microsoft.com") {
		return nil, false
	}
	var schema struct {
		Properties map[string]json.RawMessage `json:"properties"`
		Required   []string                   `json:"required"`
	}
	if err := json.Unmarshal(tool.InputSchema, &schema); err != nil {
		return nil, false
	}
	arguments := make(map[string]any)
	for name := range schema.Properties {
		switch strings.ToLower(name) {
		case "url", "uri", "href":
			arguments[name] = rawURL
		}
	}
	for _, required := range schema.Required {
		if _, ok := arguments[required]; !ok {
			return nil, false
		}
	}
	return arguments, len(arguments) != 0
}

func isSearchTool(name string) bool {
	return strings.Contains(name, "search") ||
		strings.Contains(name, "recent") ||
		strings.Contains(name, "list_updates") ||
		strings.Contains(name, "query")
}

func searchToolScore(name string, idLookup bool) int {
	if idLookup {
		return 100
	}
	switch {
	case strings.Contains(name, "docs_search"):
		return 95
	case strings.Contains(name, "recent_azure_updates"):
		return 92
	case strings.Contains(name, "recent_m365_roadmaps"):
		return 90
	case strings.Contains(name, "update") && strings.Contains(name, "search"):
		return 85
	case strings.Contains(name, "search"):
		return 80
	default:
		return 70
	}
}

func isLikelyIdentifier(id string) bool {
	_, err := strconv.ParseUint(id, 10, 64)
	return err == nil
}

func extractIDs(text string) []string {
	matches := resultID.FindAllStringSubmatch(text, 8)
	ids := make([]string, 0, len(matches))
	seen := make(map[string]struct{}, len(matches))
	for _, match := range matches {
		if len(match) != 2 {
			continue
		}
		if _, ok := seen[match[1]]; ok {
			continue
		}
		seen[match[1]] = struct{}{}
		ids = append(ids, match[1])
	}
	return ids
}

func extractURLs(text string) []string {
	matches := resultURL.FindAllString(text, 8)
	urls := make([]string, 0, len(matches))
	seen := make(map[string]struct{}, len(matches))
	for _, match := range matches {
		match = toolSuffix.ReplaceAllString(match, "")
		parsed, err := url.Parse(match)
		if err != nil || parsed.Scheme != "https" {
			continue
		}
		host := strings.ToLower(parsed.Hostname())
		if host != "learn.microsoft.com" && !strings.HasSuffix(host, ".microsoft.com") {
			continue
		}
		if _, ok := seen[match]; ok {
			continue
		}
		seen[match] = struct{}{}
		urls = append(urls, match)
	}
	return urls
}

func stableArguments(arguments map[string]any) string {
	data, _ := json.Marshal(arguments)
	return string(data)
}

func makeEvidence(server config.Server, tool, text string) Evidence {
	return Evidence{
		Server:   server.Name,
		Endpoint: server.URL,
		Tool:     tool,
		Text:     strings.TrimSpace(text),
	}
}

func truncate(value string, limit int) string {
	runes := []rune(value)
	if len(runes) <= limit {
		return value
	}
	return strings.TrimSpace(string(runes[:limit])) + "\n\n[Evidence truncated]"
}
