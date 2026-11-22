package config

import (
	"encoding/json"
	"fmt"
	"net"
	"net/url"
	"os"
	"regexp"
	"strings"
)

const DefaultProtocolVersion = "2025-06-18"

var environmentReference = regexp.MustCompile(`\$\{([A-Za-z_][A-Za-z0-9_]*)\}`)

type File struct {
	ProtocolVersion string   `json:"protocolVersion,omitempty"`
	Servers         []Server `json:"servers"`
}

type Server struct {
	Name            string            `json:"name"`
	URL             string            `json:"url"`
	Headers         map[string]string `json:"headers,omitempty"`
	Tools           []string          `json:"tools,omitempty"`
	ProtocolVersion string            `json:"protocolVersion,omitempty"`
	Enabled         *bool             `json:"enabled,omitempty"`
}

func (s Server) IsEnabled() bool {
	return s.Enabled == nil || *s.Enabled
}

func LoadServers(path string) ([]Server, error) {
	configuration, err := DefaultFile()
	if err != nil {
		return nil, err
	}

	if path != "" {
		data, err := os.ReadFile(path)
		if err != nil {
			return nil, fmt.Errorf("read MCP config %q: %w", path, err)
		}
		if err := json.Unmarshal(data, &configuration); err != nil {
			return nil, fmt.Errorf("parse MCP config %q: %w", path, err)
		}
	}

	if configuration.ProtocolVersion == "" {
		configuration.ProtocolVersion = DefaultProtocolVersion
	}

	servers := configuration.Servers
	seen := make(map[string]struct{}, len(servers))
	enabled := 0
	for index := range servers {
		server := &servers[index]
		if strings.TrimSpace(server.Name) == "" {
			return nil, fmt.Errorf("MCP server %d has no name", index+1)
		}
		server.Name = strings.TrimSpace(server.Name)
		if _, duplicate := seen[server.Name]; duplicate {
			return nil, fmt.Errorf("duplicate MCP server name %q", server.Name)
		}
		seen[server.Name] = struct{}{}

		if !server.IsEnabled() {
			continue
		}
		enabled++
		if err := validateEndpoint(server.URL); err != nil {
			return nil, fmt.Errorf("MCP server %q: %w", server.Name, err)
		}
		if server.ProtocolVersion == "" {
			server.ProtocolVersion = configuration.ProtocolVersion
		}
		expanded, err := expandHeaders(server.Headers)
		if err != nil {
			return nil, fmt.Errorf("MCP server %q: %w", server.Name, err)
		}
		server.Headers = expanded
	}

	if enabled == 0 {
		return nil, fmt.Errorf("MCP config has no enabled servers")
	}
	return servers, nil
}

func DefaultFile() (File, error) {
	servers := []Server{
		{
			Name: "microsoft-learn",
			URL:  "https://learn.microsoft.com/api/mcp",
			Tools: []string{
				"microsoft_docs_search",
				"microsoft_docs_fetch",
				"microsoft_code_sample_search",
			},
		},
		{
			Name: "microsoft-release-communications",
			URL:  "https://www.microsoft.com/releasecommunications/mcp",
			Tools: []string{
				"get_azure_update_by_id",
				"get_recent_azure_updates",
				"get_m365_roadmap_by_id",
				"get_recent_m365_roadmaps",
			},
		},
	}

	return File{
		ProtocolVersion: DefaultProtocolVersion,
		Servers:         servers,
	}, nil
}

func expandHeaders(headers map[string]string) (map[string]string, error) {
	if headers == nil {
		return nil, nil
	}

	expanded := make(map[string]string, len(headers))
	for name, value := range headers {
		result := value
		for _, match := range environmentReference.FindAllStringSubmatch(value, -1) {
			replacement, ok := os.LookupEnv(match[1])
			if !ok {
				return nil, fmt.Errorf("header %q references unset environment variable %s", name, match[1])
			}
			result = strings.ReplaceAll(result, match[0], replacement)
		}
		expanded[name] = result
	}
	return expanded, nil
}

func validateEndpoint(raw string) error {
	parsed, err := url.Parse(raw)
	if err != nil {
		return fmt.Errorf("invalid URL: %w", err)
	}
	if parsed.Scheme == "" || parsed.Hostname() == "" {
		return fmt.Errorf("URL must include a scheme and host")
	}
	if parsed.User != nil {
		return fmt.Errorf("URL must not include user information")
	}
	if parsed.Scheme == "https" {
		return nil
	}
	if parsed.Scheme != "http" {
		return fmt.Errorf("URL scheme must be https (http is only allowed for loopback)")
	}
	if !isLoopback(parsed.Hostname()) {
		return fmt.Errorf("URL scheme must be https (http is only allowed for loopback)")
	}
	return nil
}

func isLoopback(host string) bool {
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}
