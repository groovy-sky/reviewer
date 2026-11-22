package update

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"strings"
	"unicode"

	"golang.org/x/net/html"
)

const maxDocumentBytes = 2 << 20

var (
	kbIdentifier = regexp.MustCompile(`(?i)\bKB[0-9]{4,8}\b`)
	identifier   = regexp.MustCompile(`(?i)(?:feature(?:id)?|roadmap|update|id)[/_=-](\d{4,10})`)
)

type Document struct {
	Title      string
	Text       string
	URL        string
	Identifier string
	Query      string
}

type Loader struct {
	HTTPClient *http.Client
}

func (l Loader) Load(ctx context.Context, input, file string, stdin io.Reader) (Document, error) {
	if input != "" && file != "" {
		return Document{}, fmt.Errorf("provide either an update argument or --file, not both")
	}

	value := strings.TrimSpace(input)
	switch {
	case file != "":
		data, err := readFile(file)
		if err != nil {
			return Document{}, err
		}
		return newDocument(string(data), ""), nil
	case value == "-":
		data, err := readLimited(stdin, maxDocumentBytes)
		if err != nil {
			return Document{}, fmt.Errorf("read update from stdin: %w", err)
		}
		return newDocument(string(data), ""), nil
	case strings.HasPrefix(value, "@"):
		data, err := readFile(strings.TrimSpace(strings.TrimPrefix(value, "@")))
		if err != nil {
			return Document{}, err
		}
		return newDocument(string(data), ""), nil
	case isURL(value):
		return l.loadURL(ctx, value)
	default:
		return newDocument(value, ""), nil
	}
}

func (l Loader) loadURL(ctx context.Context, rawURL string) (Document, error) {
	parsed, err := url.Parse(rawURL)
	if err != nil {
		return Document{}, fmt.Errorf("parse update URL: %w", err)
	}
	if err := validateMicrosoftURL(parsed); err != nil {
		return Document{}, err
	}

	client := http.DefaultClient
	if l.HTTPClient != nil {
		copy := *l.HTTPClient
		client = &copy
	}
	originalRedirect := client.CheckRedirect
	client.CheckRedirect = func(request *http.Request, via []*http.Request) error {
		if err := validateMicrosoftURL(request.URL); err != nil {
			return err
		}
		if len(via) >= 10 {
			return fmt.Errorf("too many redirects")
		}
		if originalRedirect != nil {
			return originalRedirect(request, via)
		}
		return nil
	}

	request, err := http.NewRequestWithContext(ctx, http.MethodGet, parsed.String(), nil)
	if err != nil {
		return Document{}, fmt.Errorf("create update request: %w", err)
	}
	request.Header.Set("Accept", "text/html, text/plain;q=0.9")
	request.Header.Set("User-Agent", "groovy-sky-reviewer/0.1.0")

	response, err := client.Do(request)
	if err != nil {
		return Document{}, fmt.Errorf("fetch update: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return Document{}, fmt.Errorf("fetch update: HTTP %s", response.Status)
	}
	data, err := readLimited(response.Body, maxDocumentBytes)
	if err != nil {
		return Document{}, fmt.Errorf("read update response: %w", err)
	}

	contentType := response.Header.Get("Content-Type")
	body := string(data)
	title := ""
	if strings.Contains(strings.ToLower(contentType), "html") {
		title, body, err = extractHTML(body)
		if err != nil {
			return Document{}, fmt.Errorf("parse update HTML: %w", err)
		}
	} else if strings.Contains(strings.ToLower(contentType), "text/") || contentType == "" {
		body = normalize(body)
	} else {
		return Document{}, fmt.Errorf("fetch update: unsupported content type %q", contentType)
	}
	return newDocumentWithTitle(body, response.Request.URL.String(), title), nil
}

func newDocument(raw, rawURL string) Document {
	return newDocumentWithTitle(raw, rawURL, "")
}

func newDocumentWithTitle(raw, rawURL, title string) Document {
	text := normalize(raw)
	id := findIdentifier(text + " " + rawURL)
	query := deriveQuery(title, text, rawURL, id)
	return Document{
		Title:      strings.TrimSpace(title),
		Text:       text,
		URL:        rawURL,
		Identifier: id,
		Query:      query,
	}
}

func deriveQuery(title, text, rawURL, id string) string {
	parts := make([]string, 0, 3)
	title = normalize(title)
	lowerTitle := strings.ToLower(title)
	if title != "" &&
		!strings.EqualFold(title, "Microsoft support") &&
		!strings.HasSuffix(lowerTitle, "microsoft learn") {
		parts = append(parts, title)
	}
	if id != "" {
		parts = append(parts, id)
	}
	if len(parts) == 0 {
		candidate := firstMeaningfulText(text)
		if candidate != "" {
			parts = append(parts, candidate)
		} else if rawURL != "" {
			parts = append(parts, rawURL)
		}
	}
	return truncate(strings.Join(parts, " "), 320)
}

func firstMeaningfulText(text string) string {
	for _, line := range strings.Split(text, "\n") {
		line = strings.TrimSpace(line)
		if len([]rune(line)) >= 12 && !strings.HasPrefix(line, "http://") && !strings.HasPrefix(line, "https://") {
			return truncate(line, 240)
		}
	}
	return truncate(text, 240)
}

func findIdentifier(text string) string {
	if value := kbIdentifier.FindString(text); value != "" {
		return strings.ToUpper(value)
	}
	matches := identifier.FindStringSubmatch(text)
	if len(matches) == 2 {
		return matches[1]
	}
	trimmed := strings.TrimSpace(text)
	if len(trimmed) >= 4 && len(trimmed) <= 10 && isDigits(trimmed) {
		return trimmed
	}
	return ""
}

func isDigits(value string) bool {
	for _, r := range value {
		if r < '0' || r > '9' {
			return false
		}
	}
	return value != ""
}

func validateMicrosoftURL(parsed *url.URL) error {
	if parsed.Scheme == "https" {
		// Continue with host validation.
	} else if parsed.Scheme == "http" && isLoopback(parsed.Hostname()) {
		// Loopback is allowed for deterministic tests and local proxies.
	} else {
		return fmt.Errorf("update URL must use https")
	}
	host := strings.ToLower(strings.TrimSuffix(parsed.Hostname(), "."))
	allowed := []string{
		"microsoft.com",
		"microsoftonline.com",
		"office.com",
		"azure.com",
		"windows.com",
		"visualstudio.com",
	}
	for _, suffix := range allowed {
		if host == suffix || strings.HasSuffix(host, "."+suffix) {
			return nil
		}
	}
	if isLoopback(parsed.Hostname()) {
		return nil
	}
	return fmt.Errorf("update URL host %q is not an allowed Microsoft domain", parsed.Hostname())
}

func isLoopback(host string) bool {
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

func isURL(value string) bool {
	parsed, err := url.Parse(value)
	return err == nil && parsed.Scheme != "" && parsed.Host != ""
}

func readFile(path string) ([]byte, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("open update file %q: %w", path, err)
	}
	defer file.Close()
	data, err := readLimited(file, maxDocumentBytes)
	if err != nil {
		return nil, fmt.Errorf("read update file %q: %w", path, err)
	}
	return data, nil
}

func readLimited(reader io.Reader, limit int64) ([]byte, error) {
	data, err := io.ReadAll(io.LimitReader(reader, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > limit {
		return nil, fmt.Errorf("input exceeds %d bytes", limit)
	}
	return data, nil
}

func extractHTML(source string) (string, string, error) {
	root, err := html.Parse(strings.NewReader(source))
	if err != nil {
		return "", "", err
	}
	var title, heading string
	var text strings.Builder
	var visit func(*html.Node)
	visit = func(node *html.Node) {
		if node.Type == html.ElementNode {
			switch strings.ToLower(node.Data) {
			case "script", "style", "noscript", "template", "svg":
				return
			}
		}
		if node.Type == html.TextNode {
			value := strings.TrimSpace(node.Data)
			if value != "" {
				text.WriteString(value)
				text.WriteByte('\n')
			}
		}
		switch {
		case node.Type == html.ElementNode && node.Data == "title" && title == "":
			title = nodeText(node)
		case node.Type == html.ElementNode && node.Data == "h1" && heading == "":
			heading = nodeText(node)
		}
		for child := node.FirstChild; child != nil; child = child.NextSibling {
			visit(child)
		}
	}
	visit(root)
	if title == "" {
		title = heading
	}
	return normalize(title), normalize(text.String()), nil
}

func nodeText(node *html.Node) string {
	var value strings.Builder
	var visit func(*html.Node)
	visit = func(current *html.Node) {
		if current.Type == html.TextNode {
			value.WriteString(current.Data)
			value.WriteByte(' ')
		}
		for child := current.FirstChild; child != nil; child = child.NextSibling {
			visit(child)
		}
	}
	visit(node)
	return normalize(value.String())
}

func normalize(value string) string {
	fields := strings.FieldsFunc(value, func(r rune) bool {
		return unicode.IsSpace(r) || r == '\u0000'
	})
	return strings.TrimSpace(strings.Join(fields, " "))
}

func truncate(value string, limit int) string {
	runes := []rune(value)
	if len(runes) <= limit {
		return value
	}
	return strings.TrimSpace(string(runes[:limit])) + "…"
}
