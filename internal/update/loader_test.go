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

func TestLoadURLPrefersExplicitUpdateIdentifier(t *testing.T) {
	for _, tt := range []struct {
		name       string
		path       string
		redirectTo string
		wantID     string
	}{
		{name: "Azure query", path: "/updates/?id=467931", wantID: "467931"},
		{name: "Azure path", path: "/updates/467931", wantID: "467931"},
		{name: "feature ID query", path: "/roadmap?featureId=4679%33%31", wantID: "467931"},
		{name: "KB slug", path: "/help/latest-update-kb5067036", wantID: "KB5067036"},
		{name: "KB query", path: "/help?id=kb5067036", wantID: "KB5067036"},
		{name: "redirect strips ID", path: "/updates/?id=467931", redirectTo: "/details", wantID: "467931"},
		{name: "redirect replaces ID", path: "/updates/?id=467931", redirectTo: "/updates/?id=467932", wantID: "467932"},
		{name: "unrelated URL parameters", path: "/details?tenantid=467931&ref=KB5067036", wantID: "KB5035853"},
		{name: "invalid URL ID", path: "/updates/?id=467931beta", wantID: "KB5035853"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
				if tt.redirectTo != "" && request.URL.RequestURI() == tt.path {
					http.Redirect(writer, request, tt.redirectTo, http.StatusFound)
					return
				}
				writer.Header().Set("Content-Type", "text/html; charset=utf-8")
				_, _ = fmt.Fprint(writer, `<title>Current update</title><p>See also the related KB5035853.</p>`)
			}))
			defer server.Close()

			document, err := (Loader{HTTPClient: server.Client()}).Load(context.Background(), server.URL+tt.path, "", nil)
			if err != nil {
				t.Fatal(err)
			}
			if document.Identifier != tt.wantID || document.Query != "Current update "+tt.wantID {
				t.Fatalf("got identifier %q and query %q; want identifier %q in the search query",
					document.Identifier, document.Query, tt.wantID)
			}
			if tt.redirectTo != "" && document.URL != server.URL+tt.redirectTo {
				t.Fatalf("got source URL %q; want final URL %q", document.URL, server.URL+tt.redirectTo)
			}
		})
	}
}

func TestLoadURLRejectsNonMicrosoftHost(t *testing.T) {
	_, err := (Loader{}).Load(context.Background(), "https://example.com/update", "", strings.NewReader(""))
	if err == nil {
		t.Fatal("expected host validation error")
	}
}
