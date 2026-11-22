.PHONY: build model up down doctor test verify

VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)

build:
	go build -trimpath -ldflags "-X github.com/groovy-sky/reviewer/internal/cli.Version=$(VERSION)" -o bin/reviewer ./cmd/reviewer

model:
	./scripts/download-model.sh

up: model
	docker compose up -d --wait gemma

down:
	docker compose down

doctor: build
	./bin/reviewer doctor

test:
	go test ./...

verify: test
	go vet ./...
	docker compose config --quiet
	bash -n scripts/download-model.sh
