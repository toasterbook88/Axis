VERSION  := $(shell grep 'Version =' internal/buildinfo/version.go | cut -d'"' -f2)
COMMIT   := $(shell git rev-parse --short HEAD 2>/dev/null || echo unknown)
DATE     := $(shell date -u +%Y-%m-%dT%H:%M:%SZ)
GOVERSION := $(shell go version | awk '{print $$3}')
PREFIX   ?= $(HOME)/.local

LDFLAGS  := -s -w \
	-X github.com/toasterbook88/axis/internal/buildinfo.Commit=$(COMMIT) \
	-X github.com/toasterbook88/axis/internal/buildinfo.Date=$(DATE) \
	-X github.com/toasterbook88/axis/internal/buildinfo.GoVersion=$(GOVERSION) \
	-X github.com/toasterbook88/axis/internal/buildinfo.UpdateManagedBy=

.PHONY: build test test-race lint coverage clean install install-user

build:
	CGO_ENABLED=0 go build -trimpath -ldflags "$(LDFLAGS)" -o axis ./cmd/axis/

# Install to GOPATH/bin (legacy; often not on operator PATH).
install: build
	cp axis $(shell go env GOPATH)/bin/axis

# Install to ~/.local/bin — matches axis update / install.sh on Cranium.
install-user: build
	mkdir -p $(PREFIX)/bin
	install -m 0755 axis $(PREFIX)/bin/axis
	@echo "installed $(PREFIX)/bin/axis (version $(VERSION) commit $(COMMIT))"
	@echo "verify: $(PREFIX)/bin/axis version"
	@echo "daemon: $(PREFIX)/bin/axis daemon restart && $(PREFIX)/bin/axis daemon status"

test:
	go test ./... -count=1 -timeout 180s

test-race:
	go test ./... -count=1 -timeout 180s -race

lint:
	@unformatted=$$(gofmt -l .) || exit $$?; \
	if [ -n "$$unformatted" ]; then \
		echo "Files need gofmt:"; \
		echo "$$unformatted"; \
		exit 1; \
	fi
	go vet ./...

coverage:
	./hack/coverage-check.sh

clean:
	rm -f axis
