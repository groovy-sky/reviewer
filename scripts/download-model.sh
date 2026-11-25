#!/usr/bin/env bash
set -euo pipefail

model="${REVIEWER_MODEL:-nemotron}"
model_dir="${REVIEWER_MODEL_DIR:-models}"

case "${model}" in
  nemotron)
    model_repo="nvidia/NVIDIA-Nemotron-3-Nano-4B-GGUF"
    model_file="NVIDIA-Nemotron3-Nano-4B-Q4_K_M.gguf"
    ;;
  lfm25)
    model_repo="LiquidAI/LFM2.5-2.6B-GGUF"
    model_file="LFM2.5-2.6B-Q8_0.gguf"
    ;;
  *)
    printf 'REVIEWER_MODEL must be nemotron or lfm25\n' >&2
    exit 1
    ;;
esac

revision="${REVIEWER_MODEL_REVISION:-main}"
model_file="${REVIEWER_MODEL_FILENAME:-${model_file}}"
model_sha256="${REVIEWER_MODEL_SHA256:-}"
target="${model_dir}/${model_file}"
partial="${target}.part"

mkdir -p "${model_dir}"

if [[ ! -f "${target}" ]]; then
  url="${REVIEWER_MODEL_URL:-https://huggingface.co/${model_repo}/resolve/${revision}/${model_file}?download=true}"
  printf 'Downloading %s from %s...\n' "${model_file}" "${model_repo}"
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

printf 'Model ready: %s\n' "${target}"
