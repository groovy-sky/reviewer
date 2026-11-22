
# Reviewer

`reviewer` is a Go CLI that writes evidence-grounded Markdown reviews of Microsoft
updates. It combines:

- the local Gemma 3 4B instruct model served by the official llama.cpp Docker image;
- Microsoft's remote Streamable HTTP MCP servers for Learn documentation and
  Release Communications;
- a bounded source-gathering workflow that feeds evidence to Gemma without
  exposing model tool calls or fetched pages as instructions.

The CLI accepts a Microsoft update URL, a KB or release identifier, pasted
update text, a file, or stdin.

## Requirements

- Docker with Docker Compose
- Go 1.24 or newer
- about 2.49 GB of free space for the quantized Gemma model
- internet access to Hugging Face and the configured Microsoft MCP endpoints

Gemma's usage terms apply to the downloaded model. The model is downloaded from
the `ggml-org` Gemma 3 GGUF repository at a pinned revision and verified before
the container starts.

## Quick start

```bash
make build
make up
```

`make up` downloads the checksummed
`gemma-3-4b-it-Q4_K_M.gguf` model, then starts the pinned
`ghcr.io/ggml-org/llama.cpp:server-b7728` image. The OpenAI-compatible llama.cpp
endpoint is bound only to `127.0.0.1:8080`.

Check the model and both MCP connections:

```bash
./bin/reviewer doctor
```

Write a review:

```bash
./bin/reviewer review --output review.md "KB5035853"
./bin/reviewer review "https://azure.microsoft.com/en-us/updates/..."
cat update.txt | ./bin/reviewer review -
./bin/reviewer review --file update.md --output review.md
```

The report goes to stdout unless `--output` is supplied. Stop the model with:

```bash
make down
```

## What the review contains

The generated report includes:

- an evidence-backed adoption recommendation;
- confirmed changes;
- operational and compatibility impact;
- rollout considerations and concrete actions;
- unknowns or missing details;
- a manifest of the MCP tools and source links used.

The model is instructed to distinguish facts from recommendations, cite source
labels, and say "unknown" rather than invent missing identifiers, dates, CVEs,
versions, or prerequisites.

## Microsoft MCPs

The compiled defaults use these public Microsoft endpoints:

| Server | Endpoint | Tools used |
| --- | --- | --- |
| Microsoft Learn | `https://learn.microsoft.com/api/mcp` | `microsoft_docs_search`, `microsoft_docs_fetch`, `microsoft_code_sample_search` |
| Microsoft Release Communications | `https://www.microsoft.com/releasecommunications/mcp` | Azure Update and Microsoft 365 Roadmap list/detail tools |

The client discovers each server's current tool schemas at runtime, allowlists
the configured tool names, and calls only the review-relevant search and detail
operations. It supports JSON and SSE responses from Streamable HTTP MCP.

By default, one unavailable MCP server produces a warning and the review
continues if another server returns usable evidence. Use `--strict-mcp` to fail
on any configured server or tool error.

To add or replace servers, copy `configs/mcp.example.json` and pass
`--mcp-config`:

```bash
cp configs/mcp.example.json mcp.json
./bin/reviewer doctor --mcp-config mcp.json
```

Custom configuration supports custom headers and environment references:

```json
{
  "servers": [{
    "name": "private-microsoft-mcp",
    "url": "https://mcp.example.microsoft.com/",
    "headers": {
      "Authorization": "Bearer ${MICROSOFT_MCP_TOKEN}"
    },
    "tools": ["search_updates"]
  }]
}
```

Remote MCP and update URLs must use HTTPS. Plain HTTP is accepted only for
loopback endpoints so tests and local MCP proxies can run safely. Direct update
URL fetching is restricted to Microsoft-owned domains and validates redirects.

## Configuration

| Flag / variable | Default | Purpose |
| --- | --- | --- |
| `--llm-url` / `REVIEWER_LLM_URL` | `http://127.0.0.1:8080` | llama.cpp base URL |
| `--model` / `REVIEWER_LLM_MODEL` | `gemma-3-4b-it` | llama-server model alias |
| `--mcp-config` / `REVIEWER_MCP_CONFIG` | built-in Microsoft servers | MCP JSON path |
| `--timeout` / `REVIEWER_TIMEOUT` | `5m` | overall timeout |
| `--max-tokens` / `REVIEWER_MAX_TOKENS` | `2048` | generated-token limit |
| `--temperature` / `REVIEWER_TEMPERATURE` | `0.15` | sampling temperature |
| `--max-evidence` | `6` | maximum evidence blocks in the prompt |
| `REVIEWER_LLM_API_KEY` | unset | optional Bearer token |

Copy `.env.example` to `.env` to customize Docker Compose. GPU users can set
`LLAMA_GPU_LAYERS`; the default image is CPU-only.

## Verification

```bash
make verify
```

This runs the Go tests and vet checks, validates Compose, and checks the model
download script. Tests use in-process fake MCP and llama.cpp servers; the large
model is not downloaded in CI.
