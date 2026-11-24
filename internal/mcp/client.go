package mcp

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"mime"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"

	"github.com/groovy-sky/reviewer/internal/config"
)

const (
	maxResponseBytes = 8 << 20
	maxErrorBytes    = 8 << 10
)

type Client struct {
	server    config.Server
	endpoint  *url.URL
	http      *http.Client
	sessionID string
	protocol  string
	nextID    int64
	mu        sync.Mutex
}

type Tool struct {
	Name        string          `json:"name"`
	Description string          `json:"description,omitempty"`
	InputSchema json.RawMessage `json:"inputSchema"`
}

type ToolResult struct {
	Content           []Content       `json:"content"`
	StructuredContent json.RawMessage `json:"structuredContent,omitempty"`
	IsError           bool            `json:"isError,omitempty"`
}

type Content struct {
	Type     string            `json:"type"`
	Text     string            `json:"text,omitempty"`
	Data     string            `json:"data,omitempty"`
	MIMEType string            `json:"mimeType,omitempty"`
	URI      string            `json:"uri,omitempty"`
	Resource *EmbeddedResource `json:"resource,omitempty"`
}

type EmbeddedResource struct {
	URI      string `json:"uri"`
	MIMEType string `json:"mimeType,omitempty"`
	Text     string `json:"text,omitempty"`
	Blob     string `json:"blob,omitempty"`
}

type InitializeResult struct {
	ProtocolVersion string `json:"protocolVersion"`
	ServerInfo      struct {
		Name    string `json:"name"`
		Version string `json:"version"`
	} `json:"serverInfo"`
}

type rpcError struct {
	Code    int             `json:"code"`
	Message string          `json:"message"`
	Data    json.RawMessage `json:"data,omitempty"`
}

func (e *rpcError) Error() string {
	return fmt.Sprintf("MCP error %d: %s", e.Code, e.Message)
}

type request struct {
	JSONRPC string `json:"jsonrpc"`
	ID      *int64 `json:"id,omitempty"`
	Method  string `json:"method"`
	Params  any    `json:"params,omitempty"`
}

type responseEnvelope struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Result  json.RawMessage `json:"result,omitempty"`
	Error   *rpcError       `json:"error,omitempty"`
}

func NewClient(server config.Server, httpClient *http.Client) (*Client, error) {
	endpoint, err := url.Parse(server.URL)
	if err != nil {
		return nil, fmt.Errorf("parse MCP endpoint: %w", err)
	}
	if endpoint.Scheme == "" || endpoint.Host == "" {
		return nil, fmt.Errorf("parse MCP endpoint: URL must include scheme and host")
	}
	if httpClient == nil {
		httpClient = http.DefaultClient
	}
	return &Client{
		server:   server,
		endpoint: endpoint,
		http:     httpClient,
		protocol: server.ProtocolVersion,
	}, nil
}

func (c *Client) Server() config.Server {
	return c.server
}

func (c *Client) Initialize(ctx context.Context) error {
	result := InitializeResult{}
	params := map[string]any{
		"protocolVersion": c.protocol,
		"capabilities":    map[string]any{},
		"clientInfo": map[string]any{
			"name":    "groovy-sky-reviewer",
			"version": "0.1.0",
		},
	}
	if err := c.request(ctx, "initialize", params, &result); err != nil {
		return err
	}
	if result.ProtocolVersion == "" {
		return fmt.Errorf("initialize %s: response omitted protocol version", c.server.Name)
	}

	c.mu.Lock()
	c.protocol = result.ProtocolVersion
	c.mu.Unlock()

	return c.notify(ctx, "notifications/initialized", map[string]any{})
}

func (c *Client) ListTools(ctx context.Context) ([]Tool, error) {
	var all []Tool
	cursor := ""
	for {
		params := map[string]any{}
		if cursor != "" {
			params["cursor"] = cursor
		}
		result := struct {
			Tools      []Tool `json:"tools"`
			NextCursor string `json:"nextCursor,omitempty"`
		}{}
		if err := c.request(ctx, "tools/list", params, &result); err != nil {
			return nil, err
		}
		all = append(all, result.Tools...)
		if result.NextCursor == "" {
			break
		}
		cursor = result.NextCursor
	}
	return all, nil
}

func (c *Client) CallTool(ctx context.Context, name string, arguments any) (ToolResult, error) {
	params := map[string]any{
		"name":      name,
		"arguments": arguments,
	}
	result := ToolResult{}
	if err := c.request(ctx, "tools/call", params, &result); err != nil {
		return ToolResult{}, err
	}
	if result.IsError {
		text := result.Text()
		if text == "" {
			text = "tool returned an unspecified error"
		}
		return ToolResult{}, fmt.Errorf("tool %s: %s", name, text)
	}
	return result, nil
}

func (r ToolResult) Text() string {
	parts := make([]string, 0, len(r.Content)+1)
	for _, content := range r.Content {
		switch content.Type {
		case "text":
			parts = append(parts, content.Text)
		case "resource_link":
			parts = append(parts, content.URI)
		case "resource":
			if content.Resource == nil {
				continue
			}
			if content.Resource.Text != "" {
				parts = append(parts, content.Resource.Text)
			} else if content.Resource.URI != "" {
				parts = append(parts, content.Resource.URI)
			}
		}
	}
	if len(parts) == 0 && len(r.StructuredContent) != 0 && !bytes.Equal(r.StructuredContent, []byte("null")) {
		var structured bytes.Buffer
		if err := json.Indent(&structured, r.StructuredContent, "", "  "); err == nil {
			parts = append(parts, structured.String())
		} else {
			parts = append(parts, string(r.StructuredContent))
		}
	}
	return strings.TrimSpace(strings.Join(parts, "\n\n"))
}

func (c *Client) request(ctx context.Context, method string, params any, result any) error {
	payload, id, err := c.payload(method, params, true)
	if err != nil {
		return err
	}
	parsed, err := c.send(ctx, payload, id)
	if err != nil {
		return fmt.Errorf("%s via %s: %w", method, c.server.Name, err)
	}
	if len(parsed.Result) == 0 {
		return fmt.Errorf("%s via %s: MCP response omitted a result", method, c.server.Name)
	}
	if err := json.Unmarshal(parsed.Result, result); err != nil {
		return fmt.Errorf("%s via %s: decode result: %w", method, c.server.Name, err)
	}
	return nil
}

func (c *Client) notify(ctx context.Context, method string, params any) error {
	payload, _, err := c.payload(method, params, false)
	if err != nil {
		return err
	}
	_, err = c.send(ctx, payload, nil)
	if err != nil {
		return fmt.Errorf("%s via %s: %w", method, c.server.Name, err)
	}
	return nil
}

func (c *Client) payload(method string, params any, withID bool) ([]byte, *int64, error) {
	envelope := request{
		JSONRPC: "2.0",
		Method:  method,
		Params:  params,
	}
	var id *int64
	if withID {
		c.mu.Lock()
		c.nextID++
		requestID := c.nextID
		c.mu.Unlock()
		id = &requestID
		envelope.ID = id
	}
	payload, err := json.Marshal(envelope)
	if err != nil {
		return nil, nil, fmt.Errorf("encode MCP %s request: %w", method, err)
	}
	return payload, id, nil
}

func (c *Client) send(ctx context.Context, payload []byte, expectedID *int64) (responseEnvelope, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, c.endpoint.String(), bytes.NewReader(payload))
	if err != nil {
		return responseEnvelope{}, fmt.Errorf("create HTTP request: %w", err)
	}
	request.Header.Set("Accept", "application/json, text/event-stream")
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("User-Agent", "groovy-sky-reviewer/0.1.0")
	for name, value := range c.server.Headers {
		request.Header.Set(name, value)
	}

	c.mu.Lock()
	sessionID := c.sessionID
	protocol := c.protocol
	c.mu.Unlock()
	if sessionID != "" {
		request.Header.Set("Mcp-Session-Id", sessionID)
	}
	if protocol != "" {
		request.Header.Set("MCP-Protocol-Version", protocol)
	}

	response, err := c.http.Do(request)
	if err != nil {
		return responseEnvelope{}, fmt.Errorf("POST %s: %w", c.endpoint.Redacted(), err)
	}
	defer response.Body.Close()

	if sessionID := response.Header.Get("Mcp-Session-Id"); sessionID != "" {
		c.mu.Lock()
		c.sessionID = sessionID
		c.mu.Unlock()
	}

	if response.StatusCode < 200 || response.StatusCode >= 300 {
		body, _ := io.ReadAll(io.LimitReader(response.Body, maxErrorBytes))
		return responseEnvelope{}, fmt.Errorf("POST %s: HTTP %s: %s", c.endpoint.Redacted(), response.Status, strings.TrimSpace(string(body)))
	}
	if expectedID == nil {
		_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, maxErrorBytes))
		return responseEnvelope{}, nil
	}

	body, err := io.ReadAll(io.LimitReader(response.Body, maxResponseBytes+1))
	if err != nil {
		return responseEnvelope{}, fmt.Errorf("read HTTP response: %w", err)
	}
	if len(body) > maxResponseBytes {
		return responseEnvelope{}, fmt.Errorf("HTTP response exceeds %d bytes", maxResponseBytes)
	}
	mediaType, _, _ := mime.ParseMediaType(response.Header.Get("Content-Type"))
	parsed, err := decodeResponse(body, mediaType)
	if err != nil {
		return responseEnvelope{}, err
	}
	if parsed.Error != nil {
		return responseEnvelope{}, parsed.Error
	}

	if len(parsed.ID) != 0 {
		received, err := strconv.ParseInt(string(parsed.ID), 10, 64)
		if err == nil && received != *expectedID {
			return responseEnvelope{}, fmt.Errorf("MCP response id %d does not match request id %d", received, *expectedID)
		}
	}
	return parsed, nil
}

func decodeResponse(body []byte, mediaType string) (responseEnvelope, error) {
	if strings.EqualFold(mediaType, "text/event-stream") {
		return decodeEventStream(body)
	}
	var envelope responseEnvelope
	if err := json.Unmarshal(body, &envelope); err != nil {
		return responseEnvelope{}, fmt.Errorf("decode MCP JSON response: %w", err)
	}
	return envelope, nil
}

func decodeEventStream(body []byte) (responseEnvelope, error) {
	scanner := bufio.NewScanner(bytes.NewReader(body))
	scanner.Buffer(make([]byte, 64<<10), maxResponseBytes)
	var data []string
	flush := func() (responseEnvelope, bool, error) {
		if len(data) == 0 {
			return responseEnvelope{}, false, nil
		}
		raw := strings.Join(data, "\n")
		data = data[:0]
		var envelope responseEnvelope
		if err := json.Unmarshal([]byte(raw), &envelope); err != nil {
			return responseEnvelope{}, false, fmt.Errorf("decode MCP event data: %w", err)
		}
		if len(envelope.ID) != 0 || envelope.Error != nil {
			return envelope, true, nil
		}
		return responseEnvelope{}, false, nil
	}

	var found responseEnvelope
	for scanner.Scan() {
		line := strings.TrimSuffix(scanner.Text(), "\r")
		if line == "" {
			envelope, ok, err := flush()
			if err != nil {
				return responseEnvelope{}, err
			}
			if ok {
				found = envelope
			}
			continue
		}
		if strings.HasPrefix(line, "data:") {
			value := strings.TrimPrefix(line, "data:")
			data = append(data, strings.TrimPrefix(value, " "))
		}
	}
	if err := scanner.Err(); err != nil {
		return responseEnvelope{}, fmt.Errorf("scan MCP event stream: %w", err)
	}
	envelope, ok, err := flush()
	if err != nil {
		return responseEnvelope{}, err
	}
	if ok {
		found = envelope
	}
	if len(found.ID) == 0 && found.Error == nil {
		return responseEnvelope{}, fmt.Errorf("MCP event stream did not contain a matching JSON-RPC response")
	}
	return found, nil
}
