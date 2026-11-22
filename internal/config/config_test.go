package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestDefaultServers(t *testing.T) {
	configuration, err := DefaultFile()
	if err != nil {
		t.Fatal(err)
	}
	if len(configuration.Servers) != 2 {
		t.Fatalf("expected 2 default servers, got %d", len(configuration.Servers))
	}
	if configuration.Servers[0].URL != "https://learn.microsoft.com/api/mcp" {
		t.Fatalf("unexpected Learn endpoint: %s", configuration.Servers[0].URL)
	}
	if configuration.Servers[1].URL != "https://www.microsoft.com/releasecommunications/mcp" {
		t.Fatalf("unexpected release endpoint: %s", configuration.Servers[1].URL)
	}
	for _, server := range configuration.Servers {
		if len(server.Tools) == 0 {
			t.Fatalf("server %s has no allowlisted tools", server.Name)
		}
	}
}

func TestLoadServersExpandsHeaders(t *testing.T) {
	t.Setenv("REVIEWER_TEST_TOKEN", "secret-value")
	path := filepath.Join(t.TempDir(), "mcp.json")
	data := `{
		"protocolVersion": "2025-06-18",
		"servers": [{
			"name": "local",
			"url": "http://127.0.0.1:8080/mcp",
			"headers": {"Authorization": "Bearer ${REVIEWER_TEST_TOKEN}"}
		}]
	}`
	if err := os.WriteFile(path, []byte(data), 0o600); err != nil {
		t.Fatal(err)
	}
	servers, err := LoadServers(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := servers[0].Headers["Authorization"]; got != "Bearer secret-value" {
		t.Fatalf("unexpected expanded header: %q", got)
	}
	if servers[0].ProtocolVersion != "2025-06-18" {
		t.Fatalf("protocol version was not inherited: %q", servers[0].ProtocolVersion)
	}
}

func TestLoadServersRejectsMissingEnvironment(t *testing.T) {
	path := filepath.Join(t.TempDir(), "mcp.json")
	data := `{
		"servers": [{
			"name": "local",
			"url": "http://localhost:8080/mcp",
			"headers": {"Authorization": "Bearer ${REVIEWER_MISSING_TEST_TOKEN}"}
		}]
	}`
	if err := os.WriteFile(path, []byte(data), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadServers(path); err == nil {
		t.Fatal("expected missing environment error")
	}
}

func TestLoadServersRejectsInsecureRemoteURL(t *testing.T) {
	path := filepath.Join(t.TempDir(), "mcp.json")
	data := `{"servers":[{"name":"remote","url":"http://example.com/mcp"}]}`
	if err := os.WriteFile(path, []byte(data), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadServers(path); err == nil {
		t.Fatal("expected insecure remote URL error")
	}
}
