# Everything here needs only Docker. Local Go or Node are used when present.

IMAGE   ?= dockpanel:dev
VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)

.PHONY: test test-go test-web test-integration web build run

## test: run every test that needs no Docker daemon
test: test-go test-web

test-go:
	./scripts/go vet ./...
	./scripts/go test -race ./...

test-web:
	@if [ -f web/package.json ]; then ./scripts/npm --prefix web test; else echo "web/ not present yet"; fi

## test-integration: tests against the local Docker daemon (creates and removes throwaway containers)
test-integration:
	DOCKPANEL_GO_IN_DOCKER=1 DOCKPANEL_DOCKER_SOCK=$${DOCKER_SOCK:-/var/run/docker.sock} \
		./scripts/go test -tags integration -count=1 ./internal/integration/...

## web: build the frontend straight into the folder the Go program embeds
web:
	./scripts/npm --prefix web ci
	./scripts/npm --prefix web run build -- --outDir ../internal/webui/dist --emptyOutDir
	touch internal/webui/dist/.gitkeep

## build: build the Docker image
build:
	docker build --build-arg VERSION=$(VERSION) -t $(IMAGE) .

## run: run the image against the local Docker daemon on http://localhost:8080
run: build
	docker run --rm -p 127.0.0.1:8080:8080 \
		-v dockpanel-data:/data \
		-v /var/run/docker.sock:/var/run/docker.sock \
		$(IMAGE)
