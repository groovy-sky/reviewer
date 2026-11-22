#!/usr/bin/env bash
set -euo pipefail

model_dir="${REVIEWER_MODEL_DIR:-models}"
model_repo="${GEMMA_MODEL_REPO:-ggml-org/gemma-3-4b-it-GGUF}"
model_revision="${GEMMA_MODEL_REVISION:-ab31416aceb30cd095cb34cc27eea120940964e4}"
model_file="${GEMMA_MODEL_FILENAME:-gemma-3-4b-it-Q4_K_M.gguf}"
model_sha256="${GEMMA_MODEL_SHA256:-882e8d2db44dc554fb0ea5077cb7e4bc49e7342a1f0da57901c0802ea21a0863}"
target="${model_dir}/${model_file}"
partial="${target}.part"

mkdir -p "${model_dir}"

if [[ -f "${target}" ]]; then
  printf 'Model already exists at %s\n' "${target}"
else
  url="${GEMMA_MODEL_URL:-https://huggingface.co/${model_repo}/resolve/${model_revision}/${model_file}?download=true}"
  printf 'Downloading %s (about 2.49 GB)...\n' "${model_file}"
  curl_args=(
    --fail
    --location
    --retry 3
    --retry-all-errors
    --continue-at -
    --output "${partial}"
  )
  if [[ -n "${HF_TOKEN:-}" ]]; then
    curl_args+=(--header "Authorization: Bearer ${HF_TOKEN}")
  fi
  curl "${curl_args[@]}" "${url}"
  mv "${partial}" "${target}"
fi

if [[ -n "${model_sha256}" ]]; then
  if command -v sha256sum >/dev/null 2>&1; then
    actual_sha256="$(sha256sum "${target}" | awk '{print $1}')"
  elif command -v shasum >/dev/null 2>&1; then
    actual_sha256="$(shasum -a 256 "${target}" | awk '{print $1}')"
  else
    printf 'No sha256 tool is available; unable to verify %s\n' "${target}" >&2
    exit 1
  fi
  if [[ "${actual_sha256}" != "${model_sha256}" ]]; then
    rm -f "${target}"
    printf 'Checksum mismatch for %s\nExpected: %s\nActual:   %s\n' \
      "${target}" "${model_sha256}" "${actual_sha256}" >&2
    exit 1
  fi
fi

printf 'Verified model: %s\n' "${target}"
