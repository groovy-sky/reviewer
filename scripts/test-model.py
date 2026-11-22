#!/usr/bin/env python3
import argparse
import json
import pathlib
import sys
import urllib.error
import urllib.request


def main() -> int:
    parser = argparse.ArgumentParser()
    parser.add_argument("--model", required=True)
    parser.add_argument("--article", required=True)
    parser.add_argument("--output", required=True)
    parser.add_argument("--url", default="http://127.0.0.1:8080/v1/chat/completions")
    args = parser.parse_args()

    article = pathlib.Path(args.article).read_text(encoding="utf-8")
    prompt = f"""Read the article below and produce exactly these three sections:

## Short summary
A concise factual summary.

## Why the announcement matters
Explain the practical importance without marketing hype.

## Sarcastic announcement
Rewrite the announcement as a short, clearly sarcastic version.

ARTICLE:
{article}
"""
    payload = json.dumps(
        {
            "model": args.model,
            "messages": [{"role": "user", "content": prompt}],
            "temperature": 0.35,
            "max_tokens": 900,
            "stream": False,
        }
    ).encode()
    request = urllib.request.Request(
        args.url,
        data=payload,
        headers={"Content-Type": "application/json", "Accept": "application/json"},
    )
    try:
        with urllib.request.urlopen(request, timeout=300) as response:
            result = json.load(response)
    except urllib.error.URLError as error:
        print(f"model request failed: {error}", file=sys.stderr)
        return 1

    choices = result.get("choices") or []
    content = choices[0].get("message", {}).get("content") if choices else None
    if not isinstance(content, str) or not content.strip():
        print(f"model returned no text: {result}", file=sys.stderr)
        return 1

    output = pathlib.Path(args.output)
    output.parent.mkdir(parents=True, exist_ok=True)
    output.write_text(f"# {args.model}\n\n{content.strip()}\n", encoding="utf-8")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
