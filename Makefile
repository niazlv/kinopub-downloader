VERSION := $(shell git describe --tags --always --dirty 2>/dev/null || echo "dev")
LDFLAGS := -s -w -X main.version=$(VERSION)
BUILD   := go build -ldflags "$(LDFLAGS)" -trimpath

.PHONY: build test clean release

build:
	$(BUILD) -o kinopub ./cmd/kinopub

test:
	go test ./... -count=1

clean:
	rm -f kinopub kinopub-*

release: clean
	GOOS=darwin  GOARCH=arm64 $(BUILD) -o kinopub-darwin-arm64  ./cmd/kinopub
	GOOS=darwin  GOARCH=amd64 $(BUILD) -o kinopub-darwin-amd64  ./cmd/kinopub
	GOOS=linux   GOARCH=amd64 $(BUILD) -o kinopub-linux-amd64   ./cmd/kinopub
	GOOS=linux   GOARCH=arm64 $(BUILD) -o kinopub-linux-arm64   ./cmd/kinopub
	@echo "Built release binaries:"
	@ls -la kinopub-*
