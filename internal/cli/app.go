package cli

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/groovy-sky/reviewer/internal/config"
	"github.com/groovy-sky/reviewer/internal/llm"
	"github.com/groovy-sky/reviewer/internal/mcp"
	"github.com/groovy-sky/reviewer/internal/research"
	"github.com/groovy-sky/reviewer/internal/review"
	"github.com/groovy-sky/reviewer/internal/update"
)

const (
	defaultLLMURL  = "http://127.0.0.1:8080"
	defaultModel   = "gemma-3-4b-it"
	defaultTimeout = 5 * time.Minute
)

var Version = "dev"

func Run(args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		printUsage(stderr)
		return 2
	}
	switch args[0] {
	case "help", "-h", "--help":
		printUsage(stdout)
		return 0
	case "version", "-v", "--version":
		_, _ = fmt.Fprintln(stdout, Version)
		return 0
	case "review":
		return runReview(args[1:], stdin, stdout, stderr)
	case "doctor":
		return runDoctor(args[1:], stdout, stderr)
	default:
		return runReview(args, stdin, stdout, stderr)
	}
}

type commonOptions struct {
	LLMURL      string
	Model       string
	APIKey      string
	MCPConfig   string
	Timeout     time.Duration
	MaxTokens   int
	Temperature float64
}

func addCommonFlags(flags *flag.FlagSet, options *commonOptions) {
	flags.StringVar(&options.LLMURL, "llm-url", options.LLMURL, "llama.cpp OpenAI-compatible base URL")
	flags.StringVar(&options.Model, "model", options.Model, "model alias exposed by llama.cpp")
	flags.StringVar(&options.APIKey, "llm-api-key", options.APIKey, "Bearer token for the LLM endpoint")
	flags.StringVar(&options.MCPConfig, "mcp-config", options.MCPConfig, "optional MCP JSON configuration file")
	flags.DurationVar(&options.Timeout, "timeout", options.Timeout, "overall command timeout")
	flags.IntVar(&options.MaxTokens, "max-tokens", options.MaxTokens, "maximum tokens generated")
	flags.Float64Var(&options.Temperature, "temperature", options.Temperature, "generation temperature")
}

func runReview(args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	flags := flag.NewFlagSet("review", flag.ContinueOnError)
	flags.SetOutput(stderr)
	common, err := commonDefaults()
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "error: %v\n", err)
		return 1
	}
	addCommonFlags(flags, &common)
	inputFile := ""
	outputFile := ""
	strictMCP := false
	maxEvidence := 6
	flags.StringVar(&inputFile, "file", "", "read the update from a file instead of an argument")
	flags.StringVar(&outputFile, "output", "", "write Markdown to this file instead of stdout")
	flags.StringVar(&outputFile, "o", "", "short alias for --output")
	flags.BoolVar(&strictMCP, "strict-mcp", false, "fail if any configured MCP server or tool call fails")
	flags.IntVar(&maxEvidence, "max-evidence", 6, "maximum MCP evidence blocks included in the prompt")
	flags.Usage = func() {
		_, _ = fmt.Fprintln(stderr, "Usage: reviewer review [flags] <Microsoft update URL|KB|text|->")
		flags.PrintDefaults()
	}
	if err := flags.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 2
	}
	input := strings.TrimSpace(strings.Join(flags.Args(), " "))
	if input == "" && inputFile == "" {
		_, _ = fmt.Fprintln(stderr, "error: provide a Microsoft update argument, --file, or '-' for stdin")
		flags.Usage()
		return 2
	}
	if maxEvidence <= 0 {
		_, _ = fmt.Fprintln(stderr, "error: --max-evidence must be greater than zero")
		return 2
	}
	if err := validateCommon(common); err != nil {
		_, _ = fmt.Fprintf(stderr, "error: %v\n", err)
		return 2
	}

	ctx, cancel := context.WithTimeout(context.Background(), common.Timeout)
	defer cancel()
	httpClient := &http.Client{Timeout: common.Timeout}

	servers, err := config.LoadServers(common.MCPConfig)
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "error: %v\n", err)
		return 1
	}
	document, err := (update.Loader{HTTPClient: httpClient}).Load(ctx, input, inputFile, stdin)
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "error: %v\n", err)
		return 1
	}
	if document.Text == "" {
		_, _ = fmt.Fprintln(stderr, "error: update input is empty")
		return 1
	}

	model, err := llm.New(llm.Options{
		BaseURL:     common.LLMURL,
		Model:       common.Model,
		APIKey:      common.APIKey,
		Temperature: common.Temperature,
		MaxTokens:   common.MaxTokens,
		HTTPClient:  httpClient,
	})
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "error: %v\n", err)
		return 1
	}
	researcher := research.Service{
		Servers:     servers,
		HTTPClient:  httpClient,
		Strict:      strictMCP,
		MaxEvidence: maxEvidence,
		Warnings:    stderr,
	}
	report, err := (review.Service{
		Research: researcher,
		LLM:      model,
	}).Generate(ctx, document)
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "error: %v\n", err)
		return 1
	}

	if outputFile == "" {
		if _, err := fmt.Fprint(stdout, report); err != nil {
			_, _ = fmt.Fprintf(stderr, "error: write review: %v\n", err)
			return 1
		}
		return 0
	}
	if err := writeFileAtomic(outputFile, []byte(report)); err != nil {
		_, _ = fmt.Fprintf(stderr, "error: %v\n", err)
		return 1
	}
	_, _ = fmt.Fprintf(stdout, "wrote %s\n", outputFile)
	return 0
}

func runDoctor(args []string, stdout, stderr io.Writer) int {
	flags := flag.NewFlagSet("doctor", flag.ContinueOnError)
	flags.SetOutput(stderr)
	common, err := commonDefaults()
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "error: %v\n", err)
		return 1
	}
	addCommonFlags(flags, &common)
	flags.Usage = func() {
		_, _ = fmt.Fprintln(stderr, "Usage: reviewer doctor [flags]")
		flags.PrintDefaults()
	}
	if err := flags.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 2
	}
	if flags.NArg() != 0 {
		_, _ = fmt.Fprintln(stderr, "error: doctor does not accept positional arguments")
		return 2
	}
	if err := validateCommon(common); err != nil {
		_, _ = fmt.Fprintf(stderr, "error: %v\n", err)
		return 2
	}

	ctx, cancel := context.WithTimeout(context.Background(), common.Timeout)
	defer cancel()
	httpClient := &http.Client{Timeout: common.Timeout}
	servers, err := config.LoadServers(common.MCPConfig)
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "error: %v\n", err)
		return 1
	}
	model, err := llm.New(llm.Options{
		BaseURL:     common.LLMURL,
		Model:       common.Model,
		APIKey:      common.APIKey,
		Temperature: common.Temperature,
		MaxTokens:   common.MaxTokens,
		HTTPClient:  httpClient,
	})
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "error: %v\n", err)
		return 1
	}

	ok := true
	if err := model.Health(ctx); err != nil {
		_, _ = fmt.Fprintf(stdout, "[ERROR] llama.cpp: %v\n", err)
		ok = false
	} else {
		_, _ = fmt.Fprintf(stdout, "[OK] llama.cpp: %s\n", common.LLMURL)
	}

	connected := 0
	for _, server := range servers {
		if !server.IsEnabled() {
			_, _ = fmt.Fprintf(stdout, "[SKIP] %s\n", server.Name)
			continue
		}
		client, err := mcp.NewClient(server, httpClient)
		if err != nil {
			_, _ = fmt.Fprintf(stdout, "[ERROR] %s: %v\n", server.Name, err)
			ok = false
			continue
		}
		if err := client.Initialize(ctx); err != nil {
			_, _ = fmt.Fprintf(stdout, "[ERROR] %s: %v\n", server.Name, err)
			ok = false
			continue
		}
		tools, err := client.ListTools(ctx)
		if err != nil {
			_, _ = fmt.Fprintf(stdout, "[ERROR] %s: %v\n", server.Name, err)
			ok = false
			continue
		}
		usableTools := countUsableTools(tools, server.Tools)
		if usableTools == 0 {
			_, _ = fmt.Fprintf(stdout, "[ERROR] %s: no configured tools were discovered\n", server.Name)
			ok = false
			continue
		}
		connected++
		_, _ = fmt.Fprintf(stdout, "[OK] %s: %d usable tools\n", server.Name, usableTools)
	}
	if connected == 0 {
		_, _ = fmt.Fprintln(stdout, "[ERROR] no Microsoft MCP server connected")
		ok = false
	}
	if !ok {
		return 1
	}
	return 0
}

func countUsableTools(tools []mcp.Tool, allowlist []string) int {
	if len(allowlist) == 0 {
		return len(tools)
	}
	allowed := make(map[string]struct{}, len(allowlist))
	for _, name := range allowlist {
		allowed[strings.ToLower(name)] = struct{}{}
	}
	count := 0
	for _, tool := range tools {
		if _, ok := allowed[strings.ToLower(tool.Name)]; ok {
			count++
		}
	}
	return count
}

func writeFileAtomic(path string, data []byte) error {
	clean := filepath.Clean(path)
	directory := filepath.Dir(clean)
	if err := os.MkdirAll(directory, 0o755); err != nil {
		return fmt.Errorf("create output directory %q: %w", directory, err)
	}
	temporary, err := os.CreateTemp(directory, ".reviewer-*")
	if err != nil {
		return fmt.Errorf("create temporary output in %q: %w", directory, err)
	}
	temporaryName := temporary.Name()
	removeTemporary := true
	defer func() {
		_ = temporary.Close()
		if removeTemporary {
			_ = os.Remove(temporaryName)
		}
	}()

	if err := temporary.Chmod(0o600); err != nil {
		return fmt.Errorf("set output permissions: %w", err)
	}
	if _, err := temporary.Write(data); err != nil {
		return fmt.Errorf("write temporary output: %w", err)
	}
	if err := temporary.Sync(); err != nil {
		return fmt.Errorf("sync temporary output: %w", err)
	}
	if err := temporary.Close(); err != nil {
		return fmt.Errorf("close temporary output: %w", err)
	}
	if err := os.Rename(temporaryName, clean); err != nil {
		return fmt.Errorf("replace output %q: %w", path, err)
	}
	removeTemporary = false
	return nil
}

func env(name, fallback string) string {
	value := strings.TrimSpace(os.Getenv(name))
	if value == "" {
		return fallback
	}
	return value
}

func parseEnvDuration(name string, fallback time.Duration) (time.Duration, error) {
	value := strings.TrimSpace(os.Getenv(name))
	if value == "" {
		return fallback, nil
	}
	parsed, err := time.ParseDuration(value)
	if err != nil {
		return 0, fmt.Errorf("%s must be a duration: %w", name, err)
	}
	return parsed, nil
}

func parseEnvInt(name string, fallback int) (int, error) {
	value := strings.TrimSpace(os.Getenv(name))
	if value == "" {
		return fallback, nil
	}
	parsed, err := strconv.Atoi(value)
	if err != nil {
		return 0, fmt.Errorf("%s must be an integer: %w", name, err)
	}
	return parsed, nil
}

func parseEnvFloat(name string, fallback float64) (float64, error) {
	value := strings.TrimSpace(os.Getenv(name))
	if value == "" {
		return fallback, nil
	}
	parsed, err := strconv.ParseFloat(value, 64)
	if err != nil {
		return 0, fmt.Errorf("%s must be a number: %w", name, err)
	}
	return parsed, nil
}

func commonDefaults() (commonOptions, error) {
	timeout, err := parseEnvDuration("REVIEWER_TIMEOUT", defaultTimeout)
	if err != nil {
		return commonOptions{}, err
	}
	maxTokens, err := parseEnvInt("REVIEWER_MAX_TOKENS", 2048)
	if err != nil {
		return commonOptions{}, err
	}
	temperature, err := parseEnvFloat("REVIEWER_TEMPERATURE", 0.15)
	if err != nil {
		return commonOptions{}, err
	}
	return commonOptions{
		LLMURL:      env("REVIEWER_LLM_URL", defaultLLMURL),
		Model:       env("REVIEWER_LLM_MODEL", defaultModel),
		APIKey:      os.Getenv("REVIEWER_LLM_API_KEY"),
		MCPConfig:   os.Getenv("REVIEWER_MCP_CONFIG"),
		Timeout:     timeout,
		MaxTokens:   maxTokens,
		Temperature: temperature,
	}, nil
}

func validateCommon(options commonOptions) error {
	if options.Timeout <= 0 {
		return fmt.Errorf("--timeout must be greater than zero")
	}
	if options.MaxTokens <= 0 {
		return fmt.Errorf("--max-tokens must be greater than zero")
	}
	if options.Temperature < 0 || options.Temperature > 2 {
		return fmt.Errorf("--temperature must be between 0 and 2")
	}
	return nil
}

func printUsage(writer io.Writer) {
	_, _ = fmt.Fprint(writer, `reviewer generates evidence-grounded Markdown reviews of Microsoft updates.

Usage:
  reviewer review [flags] <Microsoft update URL|KB|text|->
  reviewer doctor [flags]
  reviewer version

Examples:
  reviewer review https://azure.microsoft.com/updates/...
  reviewer review "KB5035853" -o review.md
  cat update.txt | reviewer review -
  reviewer doctor
`)
}
