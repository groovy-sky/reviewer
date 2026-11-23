
# Reviewer

`reviewer` is a Go CLI that writes evidence-grounded Markdown reviews of Microsoft
updates. It combines:

- a local NVIDIA Nemotron 3 Nano 4B or LiquidAI LFM2.5 2.6B model served by
  llama.cpp;
- Microsoft's remote Streamable HTTP MCP servers for Learn documentation and
  Release Communications;
- a bounded source-gathering workflow that feeds evidence to the selected model without
  exposing model tool calls or fetched pages as instructions.

The CLI accepts a Microsoft update URL, a KB or release identifier, pasted
update text, a file, or stdin.

## Requirements

- Docker with Docker Compose
- Go 1.24 or newer
- enough Docker storage for a model image
- internet access to GHCR and the configured Microsoft MCP endpoints

The selected model's usage terms apply. The model-backed images are published to
GHCR by the `Publish model images` workflow. They contain one of:

| Model | GHCR tag |
| --- | --- |
| NVIDIA Nemotron 3 Nano 4B, Q4_K_M | `ghcr.io/groovy-sky/reviewer:nemotron-3-nano-4b` |
| LiquidAI LFM2.5 2.6B, Q8_0 | `ghcr.io/groovy-sky/reviewer:lfm25-2-6b` |

## Quick start

```bash
make build
make up
```

`make up` pulls the published Nemotron image and waits for it to be ready. Use
LFM2.5 instead with:

```bash
make up MODEL=lfm25
```

The published images currently target Linux `amd64`. The OpenAI-compatible
endpoint is bound only to `127.0.0.1:8080`.

Check the model and both MCP connections:

```bash
./bin/reviewer doctor
```

Pass the matching `REVIEWER_LLM_MODEL` value when invoking the CLI manually. The
Make targets select it automatically.

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

To build a model image locally, download the selected model and build it:

```bash
make build-model MODEL=nemotron
make build-model MODEL=lfm25
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

Each enabled server gets its own call budget. To fill the `--max-evidence`
slots in the prompt, the reviewer takes one result per server per pass.
Servers that returned direct ID lookup results go first; ties use
configuration order.

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

For URL inputs, an explicit update ID in the final URL's path or query
parameters takes precedence over one in the originally supplied URL. The
reviewer only falls back to the page text if neither URL identifies the update.

## Configuration

| Flag / variable | Default | Purpose |
| --- | --- | --- |
| `--llm-url` / `REVIEWER_LLM_URL` | `http://127.0.0.1:8080` | llama.cpp base URL |
| `--model` / `REVIEWER_LLM_MODEL` | `nemotron-3-nano-4b` | llama-server model alias |
| `--mcp-config` / `REVIEWER_MCP_CONFIG` | built-in Microsoft servers | MCP JSON path |
| `--timeout` / `REVIEWER_TIMEOUT` | `5m` | overall timeout |
| `--max-tokens` / `REVIEWER_MAX_TOKENS` | `2048` | generated-token limit |
| `--temperature` / `REVIEWER_TEMPERATURE` | `0.15` | sampling temperature |
| `--max-evidence` | `6` | maximum evidence blocks in the prompt |
| `REVIEWER_LLM_API_KEY` | unset | optional Bearer token |

Copy `.env.example` to `.env` to customize Docker Compose.

## Verification

```bash
make verify
```

This runs the Go tests and vet checks, validates Compose, and checks the model
download script. Tests use in-process fake MCP and llama.cpp servers. The
`Publish model images` workflow downloads and packages the large models
separately.

## Weekly RSS summaries

The `Weekly RSS summaries` workflow runs at 23:00 UTC every Sunday. It uses the
current ISO year and week, checks each `rss*` archive branch in
`groovy-sky/rsscollector`, and selects only entries whose current revision has
a numeric `published_at` epoch timestamp in that week. Matching provider
enrichments take precedence over source-truncated feed text.

Each selected announcement is summarized independently by the published
Nemotron model using the neutral-source prompt in
`scripts/generate-weekly-rss.py`. The workflow writes one
`weekly/<YYYY-Www>.md` report to the `reviewer` branch with the same name as the
corresponding source archive branch, preserving reports from earlier weeks.
The workflow can also be started manually with an optional `week_number` from 1
to 53 and `iso_year`. Omitted values default to the current UTC ISO week and
year.
