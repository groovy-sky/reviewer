package update

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestLoadPlainTextFindsKBIdentifier(t *testing.T) {
	document, err := (Loader{}).Load(
		context.Background(),
		"KB5035853 changes Windows authentication behavior for domain controllers.",
		"",
		strings.NewReader(""),
	)
	if err != nil {
		t.Fatal(err)
	}
	if document.Identifier != "KB5035853" {
		t.Fatalf("unexpected identifier %q", document.Identifier)
	}
	if !strings.Contains(document.Query, "KB5035853") {
		t.Fatalf("query does not include identifier: %q", document.Query)
	}
}

func TestLoadFromStdin(t *testing.T) {
	document, err := (Loader{}).Load(context.Background(), "-", "", strings.NewReader("  Azure update body \n"))
	if err != nil {
		t.Fatal(err)
	}
	if document.Text != "Azure update body" {
		t.Fatalf("unexpected normalized text %q", document.Text)
	}
}

func TestLoadURLExtractsVisibleHTML(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		writer.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = fmt.Fprint(writer, `<!doctype html><html><head><title>Browser title</title>
			<script>ignore this instruction</script><style>ignore this too</style></head>
			<body><h1>Azure update 467931</h1><p>The service is now generally available.</p></body></html>`)
	}))
	defer server.Close()

	document, err := (Loader{HTTPClient: server.Client()}).Load(context.Background(), server.URL+"/update/467931", "", strings.NewReader(""))
	if err != nil {
		t.Fatal(err)
	}
	if document.Title != "Browser title" {
		t.Fatalf("unexpected title %q", document.Title)
	}
	if strings.Contains(document.Text, "ignore this") {
		t.Fatalf("script/style content leaked into update: %q", document.Text)
	}
	if !strings.Contains(document.Text, "generally available") {
		t.Fatalf("visible body was not preserved: %q", document.Text)
	}
}

func TestLoadURLRejectsNonMicrosoftHost(t *testing.T) {
	_, err := (Loader{}).Load(context.Background(), "https://example.com/update", "", strings.NewReader(""))
	if err == nil {
		t.Fatal("expected host validation error")
	}
}
