package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"os"

	"github.com/groovy-sky/reviewer/internal/modeltest"
)

func main() {
	os.Exit(run(os.Args[1:], os.Stderr))
}

func run(args []string, stderr io.Writer) int {
	flags := flag.NewFlagSet("test-model", flag.ContinueOnError)
	flags.SetOutput(stderr)
	model := ""
	article := ""
	output := ""
	url := "http://127.0.0.1:8080/v1/chat/completions"
	flags.StringVar(&model, "model", "", "model alias exposed by llama.cpp")
	flags.StringVar(&article, "article", "", "source article file")
	flags.StringVar(&output, "output", "", "Markdown output file")
	flags.StringVar(&url, "url", url, "OpenAI-compatible chat completions URL")
	flags.Usage = func() {
		_, _ = fmt.Fprintln(stderr, "Usage: test-model [flags]")
		flags.PrintDefaults()
	}
	if err := flags.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 2
	}
	if flags.NArg() != 0 || model == "" || article == "" || output == "" {
		_, _ = fmt.Fprintln(stderr, "error: --model, --article, and --output are required")
		flags.Usage()
		return 2
	}
	if err := (modeltest.Run(modeltest.Options{
		Model: model, Article: article, Output: output, URL: url,
	})); err != nil {
		_, _ = fmt.Fprintf(stderr, "model request failed: %v\n", err)
		return 1
	}
	return 0
}
