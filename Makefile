VERSION  := $(shell grep 'Version =' internal/buildinfo/version.go | cut -d'"' -f2)
COMMIT   := $(shell git rev-parse --short HEAD 2>/dev/null || echo unknown)
DATE     := $(shell date -u +%Y-%m-%dT%H:%M:%SZ)
GOVERSION := $(shell go version | awk '{print $$3}')

LDFLAGS  := -s -w \
	-X github.com/toasterbook88/axis/internal/buildinfo.Commit=$(COMMIT) \
	-X github.com/toasterbook88/axis/internal/buildinfo.Date=$(DATE) \
	-X github.com/toasterbook88/axis/internal/buildinfo.GoVersion=$(GOVERSION) \
	-X github.com/toasterbook88/axis/internal/buildinfo.UpdateManagedBy=

.PHONY: build test test-race lint coverage clean install vuln

build:
	CGO_ENABLED=0 go build -trimpath -ldflags "$(LDFLAGS)" -o axis ./cmd/axis/

install: build
	cp axis $(shell go env GOPATH)/bin/axis

test:
	go test ./... -count=1 -timeout 180s

test-race:
	go test ./... -count=1 -timeout 180s -race

lint:
	gofmt -l . | grep -q . && { echo "gofmt needed"; exit 1; } || true
	go vet ./...

coverage:
	./hack/coverage-check.sh

vuln:
	go run golang.org/x/vulncheck/cmd/govulncheck@latest ./...

clean:
	rm -f axis
