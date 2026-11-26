package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/groovy-sky/reviewer/internal/weeklyrss"
)

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

func run(args []string, stdout, stderr io.Writer) int {
	flags := flag.NewFlagSet("generate-weekly-rss", flag.ContinueOnError)
	flags.SetOutput(stderr)
	sourceDirectory := ""
	week := ""
	output := ""
	llmURL := env("REVIEWER_LLM_URL", "http://127.0.0.1:8080")
	model := env("REVIEWER_LLM_MODEL", "nemotron-3-nano-4b")
	apiKey := os.Getenv("REVIEWER_LLM_API_KEY")
	temperature := 0.15
	maxTokens := 320
	maxSourceChars := 16_000
	timeoutSeconds := 300.0
	flags.StringVar(&sourceDirectory, "source-directory", "", "checked-out rss/* archive branch")
	flags.StringVar(&week, "week", "", "ISO week in YYYY-Www format")
	flags.StringVar(&output, "output", "", "Markdown output file")
	flags.StringVar(&llmURL, "llm-url", llmURL, "llama.cpp OpenAI-compatible base URL")
	flags.StringVar(&model, "model", model, "model alias exposed by llama.cpp")
	flags.StringVar(&apiKey, "llm-api-key", apiKey, "Bearer token for the LLM endpoint")
	flags.Float64Var(&temperature, "temperature", temperature, "generation temperature")
	flags.IntVar(&maxTokens, "max-tokens", maxTokens, "maximum tokens generated")
	flags.IntVar(&maxSourceChars, "max-source-chars", maxSourceChars, "maximum aggregate introduction source characters sent to the model")
	flags.Float64Var(&timeoutSeconds, "timeout", timeoutSeconds, "per-request timeout in seconds")
	flags.Usage = func() {
		_, _ = fmt.Fprintln(stderr, "Usage: generate-weekly-rss [flags]")
		flags.PrintDefaults()
	}
	if err := flags.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 2
	}
	if flags.NArg() != 0 || sourceDirectory == "" || week == "" || output == "" {
		_, _ = fmt.Fprintln(stderr, "error: --source-directory, --week, and --output are required")
		flags.Usage()
		return 2
	}
	if temperature < 0 || temperature > 2 {
		_, _ = fmt.Fprintln(stderr, "error: --temperature must be between 0 and 2")
		return 2
	}
	if maxTokens <= 0 {
		_, _ = fmt.Fprintln(stderr, "error: --max-tokens must be greater than zero")
		return 2
	}
	if maxSourceChars <= 0 {
		_, _ = fmt.Fprintln(stderr, "error: --max-source-chars must be greater than zero")
		return 2
	}
	if timeoutSeconds <= 0 {
		_, _ = fmt.Fprintln(stderr, "error: --timeout must be greater than zero")
		return 2
	}

	feedID, announcements, err := weeklyrss.CollectAnnouncements(sourceDirectory, week)
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "error: %v\n", err)
		return 1
	}
	summarizer, err := weeklyrss.NewLLMSummarizer(weeklyrss.SummarizerOptions{
		BaseURL:        llmURL,
		Model:          model,
		APIKey:         apiKey,
		Temperature:    temperature,
		MaxTokens:      maxTokens,
		MaxSourceChars: maxSourceChars,
		Timeout:        time.Duration(timeoutSeconds * float64(time.Second)),
	})
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "error: %v\n", err)
		return 1
	}
	report, err := weeklyrss.RenderReport(
		feedID,
		week,
		announcements,
		summarizer.Summarize,
		summarizer.Introduce,
		stderr,
	)
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "error: %v\n", err)
		return 1
	}
	if err := os.MkdirAll(filepath.Dir(output), 0o755); err != nil {
		_, _ = fmt.Fprintf(stderr, "error: create output directory: %v\n", err)
		return 1
	}
	if err := os.WriteFile(output, []byte(report), 0o644); err != nil {
		_, _ = fmt.Fprintf(stderr, "error: write %s: %v\n", output, err)
		return 1
	}
	_, _ = fmt.Fprintf(stdout, "wrote %s\n", output)
	return 0
}

func env(name, fallback string) string {
	if value := strings.TrimSpace(os.Getenv(name)); value != "" {
		return value
	}
	return fallback
}
