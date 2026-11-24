.PHONY: build model build-model up down doctor test verify

VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
MODEL ?= nemotron
LLAMA_CPP_IMAGE ?= ghcr.io/ggml-org/llama.cpp:server-b12627

ifeq ($(MODEL),nemotron)
MODEL_TAG=nemotron-3-nano-4b
MODEL_FILENAME=NVIDIA-Nemotron3-Nano-4B-Q4_K_M.gguf
MODEL_NAME=nemotron-3-nano-4b
else ifeq ($(MODEL),lfm25)
MODEL_TAG=lfm25-2-6b
MODEL_FILENAME=LFM2.5-2.6B-Q8_0.gguf
MODEL_NAME=lfm25-2-6b
else
$(error MODEL must be nemotron or lfm25)
endif

MODEL_IMAGE ?= ghcr.io/groovy-sky/reviewer:$(MODEL_TAG)

build:
	go build -trimpath -ldflags "-X github.com/groovy-sky/reviewer/internal/cli.Version=$(VERSION)" -o bin/reviewer ./cmd/reviewer

model:
	REVIEWER_MODEL=$(MODEL) ./scripts/download-model.sh

build-model: model
	docker build -f Dockerfile.model \
		--build-arg LLAMA_CPP_IMAGE=$(LLAMA_CPP_IMAGE) \
		--build-arg MODEL_FILENAME=$(MODEL_FILENAME) \
		-t reviewer-model:$(MODEL_TAG) .

up:
	REVIEWER_MODEL_IMAGE=$(MODEL_IMAGE) \
	REVIEWER_MODEL_FILENAME=$(MODEL_FILENAME) \
	REVIEWER_MODEL_NAME=$(MODEL_NAME) \
	REVIEWER_LLM_MODEL=$(MODEL_NAME) \
		docker compose up -d --wait model

down:
	docker compose down

doctor: build
	REVIEWER_LLM_MODEL=$(MODEL_NAME) ./bin/reviewer doctor

test:
	go test ./...

verify: test
	go vet ./...
	docker compose config --quiet
	bash -n scripts/download-model.sh
