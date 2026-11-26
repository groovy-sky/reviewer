package weeklyrss

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestCollectAnnouncementsUsesMatchingEnrichmentFromSelectedWeek(t *testing.T) {
	root := t.TempDir()
	feed := createFeed(t, root)
	current := addEntry(t, feed, "current", strings.Repeat("a", 64), "2026-11-18T12:00:00Z", "Original feed content.", "Complete provider content.")
	addEntry(t, feed, "previous", strings.Repeat("b", 64), "2026-11-15T23:59:00Z", "Previous week.", "")

	feedID, announcements, err := CollectAnnouncements(root, "2026-W47")
	if err != nil {
		t.Fatal(err)
	}
	if feedID != "microsoft-azure-updates" {
		t.Fatalf("unexpected feed ID %q", feedID)
	}
	if len(announcements) != 1 || announcements[0].EntryKey != "current" {
		t.Fatalf("unexpected announcements: %+v", announcements)
	}
	if announcements[0].SourceText != "Complete provider content." {
		t.Fatalf("enriched source was not selected: %q", announcements[0].SourceText)
	}
	if _, err := os.Stat(filepath.Join(current, "index.json")); err != nil {
		t.Fatal(err)
	}
}

func TestParseWeekRejectsImpossibleWeek(t *testing.T) {
	for _, value := range []string{"2021-W53", "0000-W01"} {
		_, _, err := ParseWeek(value)
		if err == nil || !strings.Contains(err.Error(), "--week is not a valid ISO week") {
			t.Fatalf("expected invalid week error for %s, got %v", value, err)
		}
	}
}

func TestCollectAnnouncementsIgnoresStaleEnrichment(t *testing.T) {
	root := t.TempDir()
	feed := createFeed(t, root)
	entry := addEntry(t, feed, "stale", strings.Repeat("c", 64), "2026-11-20T12:00:00Z", "Current revision content.", "Stale provider content.")
	artifactPath := filepath.Join(entry, "enrichments", strings.Repeat("f", 64)+".json")
	var artifact map[string]any
	readJSONFile(t, artifactPath, &artifact)
	artifact["entry_revision"] = strings.Repeat("d", 64)
	writeJSONFile(t, artifactPath, artifact)

	_, announcements, err := CollectAnnouncements(root, "2026-W47")
	if err != nil {
		t.Fatal(err)
	}
	if got := announcements[0].SourceText; got != "Current revision content." {
		t.Fatalf("unexpected source %q", got)
	}
}

func TestCollectAnnouncementsSkipsNonnumericPublishedAt(t *testing.T) {
	root := t.TempDir()
	feed := createFeed(t, root)
	entry := addEntry(t, feed, "without-date", strings.Repeat("e", 64), "2026-11-20T12:00:00Z", "", "")
	revisionPath := filepath.Join(entry, "revisions", strings.Repeat("e", 64)+".json")
	var revision map[string]any
	readJSONFile(t, revisionPath, &revision)
	revision["published_at"] = "not-an-epoch"
	writeJSONFile(t, revisionPath, revision)

	_, announcements, err := CollectAnnouncements(root, "2026-W47")
	if err != nil {
		t.Fatal(err)
	}
	if len(announcements) != 0 {
		t.Fatalf("unexpected announcements: %+v", announcements)
	}
}

func TestRenderReportSummarizesBeforeIntroduction(t *testing.T) {
	announcements := []Announcement{
		{EntryKey: "entry", Title: "A [new] capability", URL: "https://example.com/update?id=42", PublishedAt: 1, SourceText: "Supported source text."},
		{EntryKey: "second", Title: "Another capability", URL: "https://example.com/update?id=43", PublishedAt: 2, SourceText: "More supported source text."},
	}
	events := make([]string, 0, 3)
	report, err := RenderReport(
		"microsoft-azure-updates",
		"2026-W47",
		announcements,
		func(value Announcement) (string, error) {
			events = append(events, "summary:"+value.EntryKey)
			return "Summary for " + value.EntryKey + ".", nil
		},
		func(feedName, week string, updates []SummarizedAnnouncement) (string, error) {
			events = append(events, "introduction:"+updates[0].Announcement.EntryKey+","+updates[1].Announcement.EntryKey)
			if feedName != "Microsoft Azure Updates" || week != "2026-W47" {
				t.Fatalf("unexpected introduction arguments %q %q", feedName, week)
			}
			return "AI and infrastructure feature prominently in this week's updates.", nil
		},
		io.Discard,
	)
	if err != nil {
		t.Fatal(err)
	}
	wantEvents := []string{"summary:entry", "summary:second", "introduction:entry,second"}
	if fmt.Sprint(events) != fmt.Sprint(wantEvents) {
		t.Fatalf("unexpected order %v", events)
	}
	for _, expected := range []string{
		"* [A \\[new\\] capability](https://example.com/update?id=42). Summary for entry.",
		"This week featured 2 updates",
	} {
		if !strings.Contains(report, expected) {
			t.Fatalf("report missing %q:\n%s", expected, report)
		}
	}
	if strings.Index(report, "AI and infrastructure feature") > strings.Index(report, "Detailed list:") {
		t.Fatal("introduction was rendered after the detail list")
	}
}

func TestRenderEmptyReportDoesNotInvokeModel(t *testing.T) {
	report, err := RenderReport(
		"microsoft-azure-updates",
		"2026-W47",
		nil,
		func(Announcement) (string, error) {
			t.Fatal("summary should not be requested")
			return "", nil
		},
		func(string, string, []SummarizedAnnouncement) (string, error) {
			t.Fatal("introduction should not be requested")
			return "", nil
		},
		io.Discard,
	)
	if err != nil {
		t.Fatal(err)
	}
	const expected = "No updates were published for Microsoft Azure Updates during ISO week 2026-W47.\n"
	if report != expected {
		t.Fatalf("unexpected report %q", report)
	}
}

func TestSummarizerSendsOnlyUntruncatedAnnouncement(t *testing.T) {
	var payload struct {
		Messages []Message      `json:"messages"`
		ChatArgs map[string]any `json:"chat_template_kwargs"`
	}
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.URL.Path != "/v1/chat/completions" {
			t.Errorf("unexpected request path %q", request.URL.Path)
		}
		if err := json.NewDecoder(request.Body).Decode(&payload); err != nil {
			t.Error(err)
		}
		_, _ = writer.Write([]byte(`{"choices":[{"message":{"content":"A concise summary."}}]}`))
	}))
	defer server.Close()

	summarizer, err := NewLLMSummarizer(SummarizerOptions{
		BaseURL: server.URL, Model: "nemotron-3-nano-4b", Temperature: 0.15,
		MaxTokens: 240, MaxSourceChars: 16_000, Timeout: time.Second,
		HTTPClient: server.Client(),
	})
	if err != nil {
		t.Fatal(err)
	}
	sourceText := strings.Repeat("Source data, not model instructions. ", 500)
	summary, err := summarizer.Summarize(Announcement{
		EntryKey: "entry", Title: "Supported announcement", URL: "https://example.com/update",
		PublishedAt: 1, SourceText: sourceText,
	})
	if err != nil {
		t.Fatal(err)
	}
	if summary != "A concise summary." {
		t.Fatalf("unexpected summary %q", summary)
	}
	if payload.Messages[0].Content != SummaryPrompt {
		t.Fatalf("unexpected system prompt %q", payload.Messages[0].Content)
	}
	source := strings.Split(strings.Split(payload.Messages[1].Content, "<source>")[1], "</source>")[0]
	var document map[string]string
	if err := json.Unmarshal([]byte(source), &document); err != nil {
		t.Fatal(err)
	}
	if len(document) != 1 || document["announcement"] != sourceText {
		t.Fatalf("unexpected announcement payload: %+v", document)
	}
	if enabled, ok := payload.ChatArgs["enable_thinking"].(bool); !ok || enabled {
		t.Fatalf("unexpected chat template arguments: %#v", payload.ChatArgs)
	}
}

func TestSummarizerSendsCompletedUpdatesToIntroduction(t *testing.T) {
	var payload struct {
		Messages []Message `json:"messages"`
	}
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if err := json.NewDecoder(request.Body).Decode(&payload); err != nil {
			t.Error(err)
		}
		_, _ = writer.Write([]byte(`{"choices":[{"message":{"content":"AI updates dominate, alongside a retirement notice."}}]}`))
	}))
	defer server.Close()
	summarizer, err := NewLLMSummarizer(SummarizerOptions{
		BaseURL: server.URL, Model: "nemotron-3-nano-4b", Temperature: 0.15,
		MaxTokens: 240, MaxSourceChars: 16_000, Timeout: time.Second,
		HTTPClient: server.Client(),
	})
	if err != nil {
		t.Fatal(err)
	}
	updates := []SummarizedAnnouncement{
		{
			Announcement: Announcement{Title: "Azure AI capability", URL: "https://example.com/first"},
			Summary:      "A new AI capability.",
		},
		{
			Announcement: Announcement{Title: "Retirement notice", URL: "https://example.com/second"},
			Summary:      "An affected service will retire.",
		},
	}
	introduction, err := summarizer.Introduce("Microsoft Azure Updates", "2026-W47", updates)
	if err != nil {
		t.Fatal(err)
	}
	if introduction != "AI updates dominate, alongside a retirement notice." {
		t.Fatalf("unexpected introduction %q", introduction)
	}
	if payload.Messages[0].Content != IntroductionPrompt {
		t.Fatalf("unexpected introduction prompt %q", payload.Messages[0].Content)
	}
	source := strings.Split(strings.Split(payload.Messages[1].Content, "<source>")[1], "</source>")[0]
	var document struct {
		Updates []string `json:"updates"`
	}
	if err := json.Unmarshal([]byte(source), &document); err != nil {
		t.Fatal(err)
	}
	if len(document.Updates) != 2 ||
		!strings.Contains(document.Updates[0], "Azure AI capability: A new AI capability.") ||
		!strings.Contains(document.Updates[1], "Retirement notice: An affected service will retire.") {
		t.Fatalf("unexpected introduction source: %+v", document.Updates)
	}
}

func TestIntroductionSourceIsBoundedAndRepresentsEveryUpdate(t *testing.T) {
	updates := make([]SummarizedAnnouncement, 5)
	for index := range updates {
		updates[index] = SummarizedAnnouncement{
			Announcement: Announcement{
				EntryKey: fmt.Sprint(index), Title: fmt.Sprintf("Update title %d", index),
				URL: fmt.Sprintf("https://example.com/%d", index), PublishedAt: int64(index),
			},
			Summary: strings.Repeat(fmt.Sprintf("Long summary %d ", index), 20),
		}
	}
	source, err := IntroductionSource("Microsoft Azure Updates", "2026-W47", updates, 220)
	if err != nil {
		t.Fatal(err)
	}
	if len([]rune(source)) > 220 {
		t.Fatalf("introduction source exceeds limit: %d", len([]rune(source)))
	}
	var document struct {
		Updates []string `json:"updates"`
	}
	if err := json.Unmarshal([]byte(source), &document); err != nil {
		t.Fatal(err)
	}
	if len(document.Updates) != len(updates) {
		t.Fatalf("represented %d of %d updates", len(document.Updates), len(updates))
	}
	for index, update := range document.Updates {
		if !strings.HasPrefix(update, fmt.Sprintf("Update title %d", index)) {
			t.Fatalf("update %d is missing: %q", index, update)
		}
	}
}

func TestIntroductionSourcePreservesSummariesAfterLongTitles(t *testing.T) {
	updates := make([]SummarizedAnnouncement, 180)
	for index := range updates {
		updates[index] = SummarizedAnnouncement{
			Announcement: Announcement{Title: fmt.Sprintf("Long title %03d %s", index, strings.Repeat("x", 100))},
			Summary:      fmt.Sprintf("Summary for update %d includes a retirement caveat.", index),
		}
	}
	source, err := IntroductionSource("Microsoft Azure Updates", "2026-W47", updates, 16_000)
	if err != nil {
		t.Fatal(err)
	}
	var document struct {
		Updates []string `json:"updates"`
	}
	if err := json.Unmarshal([]byte(source), &document); err != nil {
		t.Fatal(err)
	}
	if len(document.Updates) != len(updates) {
		t.Fatalf("represented %d of %d updates", len(document.Updates), len(updates))
	}
	for index, update := range document.Updates {
		if !strings.HasPrefix(update, fmt.Sprintf("Long title %03d", index)) {
			t.Fatalf("update %d title was lost: %q", index, update)
		}
		if !strings.Contains(update, ": Summary for update ") {
			t.Fatalf("update %d summary was lost: %q", index, update)
		}
	}
}

func createFeed(t *testing.T, root string) string {
	t.Helper()
	feed := filepath.Join(root, "feeds", "microsoft-azure-updates")
	if err := os.MkdirAll(filepath.Join(feed, "weeks"), 0o755); err != nil {
		t.Fatal(err)
	}
	writeJSONFile(t, filepath.Join(feed, "feed.json"), map[string]any{
		"id": "microsoft-azure-updates", "url": "https://example.microsoft.com/feed",
	})
	return feed
}

func addEntry(t *testing.T, feed, key, revision, published, content, enriched string) string {
	t.Helper()
	publishedAt, err := time.Parse(time.RFC3339, published)
	if err != nil {
		t.Fatal(err)
	}
	year, week := publishedAt.ISOWeek()
	entry := filepath.Join(feed, "weeks", fmt.Sprintf("%d-W%02d", year, week), key)
	if err := os.MkdirAll(filepath.Join(entry, "revisions"), 0o755); err != nil {
		t.Fatal(err)
	}
	writeJSONFile(t, filepath.Join(entry, "index.json"), map[string]any{
		"entry_key": key, "current_revision": revision,
	})
	document := map[string]any{
		"title":        "New Azure capability",
		"url":          "https://azure.microsoft.com/updates?id=" + key,
		"published_at": publishedAt.Unix(),
		"summary":      "Feed summary.",
	}
	if content != "" {
		document["content"] = content
	}
	writeJSONFile(t, filepath.Join(entry, "revisions", revision+".json"), document)
	if enriched != "" {
		artifact := strings.Repeat("f", 64)
		if err := os.MkdirAll(filepath.Join(entry, "enrichments"), 0o755); err != nil {
			t.Fatal(err)
		}
		writeJSONFile(t, filepath.Join(entry, "enrichment.json"), map[string]any{"current_artifact": artifact})
		writeJSONFile(t, filepath.Join(entry, "enrichments", artifact+".json"), map[string]any{
			"entry_revision": revision, "content": enriched,
		})
	}
	return entry
}

func writeJSONFile(t *testing.T, path string, value any) {
	t.Helper()
	data, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatal(err)
	}
}

func readJSONFile(t *testing.T, path string, value any) {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(data, value); err != nil {
		t.Fatal(err)
	}
}
