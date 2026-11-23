package review

import (
	"context"
	"strings"
	"testing"

	"github.com/groovy-sky/reviewer/internal/research"
	"github.com/groovy-sky/reviewer/internal/update"
)

type fakeResearcher struct {
	evidence []research.Evidence
}

func (f fakeResearcher) Gather(context.Context, update.Document) ([]research.Evidence, error) {
	return f.evidence, nil
}

type fakeCompleter struct {
	system string
	user   string
}

func (f *fakeCompleter) Complete(_ context.Context, system, user string) (string, error) {
	f.system = system
	f.user = user
	return "## Executive summary\nPilot first. [S1]\n\n## What changes\nConfirmed change.",
		nil
}

func TestGenerateGroundsAndLabelsReport(t *testing.T) {
	completer := &fakeCompleter{}
	service := Service{
		Research: fakeResearcher{evidence: []research.Evidence{{
			Server:   "microsoft-release-communications",
			Endpoint: "https://www.microsoft.com/releasecommunications/mcp",
			Tool:     "get_azure_update_by_id",
			Text:     `{"id":"467931","url":"https://learn.microsoft.com/azure/update-467931"}`,
		}}},
		LLM: completer,
	}
	report, err := service.Generate(context.Background(), update.Document{
		Title:      "New Azure update",
		Text:       "Untrusted source text",
		Identifier: "467931",
	})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(report, "# Microsoft update review: New Azure update") {
		t.Fatalf("report title missing: %s", report)
	}
	if !strings.Contains(report, "[S1]") || !strings.Contains(report, "get_azure_update_by_id") {
		t.Fatalf("evidence manifest missing: %s", report)
	}
	if !strings.Contains(report, "https://learn.microsoft.com/azure/update-467931") {
		t.Fatalf("evidence link missing: %s", report)
	}
	if !strings.Contains(completer.system, "Preserve every announcement as a separate bullet, including duplicates, in its original order with its original link") {
		t.Fatalf("system prompt does not preserve announcements and their order and links: %s", completer.system)
	}
	if !strings.Contains(completer.user, "### [S1]") {
		t.Fatalf("evidence was not labeled for the model: %s", completer.user)
	}
}
