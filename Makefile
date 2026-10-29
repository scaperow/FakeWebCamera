APP        := FakeWebCamera
VERSION    ?= 1.0.0
BIN        := build/fakecam
CONFIG     ?= config.yaml
GOFLAGS    ?=
LDFLAGS    := -s -w -X main.version=$(VERSION)

.PHONY: all build run app test vet fmt tidy doctor clean help

all: build

## build: compile the CLI binary into build/fakecam
build:
	@mkdir -p build
	CGO_ENABLED=1 go build $(GOFLAGS) -trimpath -ldflags "$(LDFLAGS)" -o $(BIN) ./cmd/fakecam
	@echo "built $(BIN)"

## run: start the service in the foreground
run:
	go run ./cmd/fakecam run --config $(CONFIG)

## app: build the macOS menu bar bundle (build/FakeWebCamera.app)
app:
	./scripts/build-app.sh --version $(VERSION)

## test: run unit tests
test:
	go test ./...

## vet: run go vet
vet:
	go vet ./...

## fmt: format all Go sources
fmt:
	gofmt -w $$(find . -name '*.go' -not -path './build/*')

## tidy: refresh go.mod/go.sum
tidy:
	GOTOOLCHAIN=local GOSUMDB=off go mod tidy

## doctor: check ffmpeg, interfaces and ports
doctor:
	go run ./cmd/fakecam doctor --config $(CONFIG)

## clean: remove build artifacts
clean:
	rm -rf build

## help: list targets
help:
	@grep -E '^## ' $(MAKEFILE_LIST) | sed 's/## //'
