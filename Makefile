VERSION  := $(shell grep 'Version =' internal/buildinfo/version.go | cut -d'"' -f2)
COMMIT   := $(shell git rev-parse --short HEAD 2>/dev/null || echo unknown)
DATE     := $(shell date -u +%Y-%m-%dT%H:%M:%SZ)
GOVERSION := $(shell go version | awk '{print $$3}')
PREFIX   ?= $(HOME)/.local
# Pinned: an unpinned @latest would let an upstream release break CI without a
# repository change. Bump deliberately.
STATICCHECK_VERSION ?= 2025.1.1

# Go's implicit VCS probe can fail in valid linked worktrees. Axis release
# metadata is injected explicitly below, so Makefile gates and builds do not
# depend on that probe.
export GOFLAGS := $(strip $(GOFLAGS) -buildvcs=false)

LDFLAGS  := -s -w \
	-X github.com/toasterbook88/axis/internal/buildinfo.Commit=$(COMMIT) \
	-X github.com/toasterbook88/axis/internal/buildinfo.Date=$(DATE) \
	-X github.com/toasterbook88/axis/internal/buildinfo.GoVersion=$(GOVERSION) \
	-X github.com/toasterbook88/axis/internal/buildinfo.UpdateManagedBy=

.PHONY: build test test-race lint coverage clean install install-user install-system test-install test-fleet ci-preflight release-preflight

build:
	CGO_ENABLED=0 go build -trimpath -ldflags "$(LDFLAGS)" -o axis ./cmd/axis/

# Install to GOPATH/bin (legacy; often not on operator PATH).
install: build
	cp axis $(shell go env GOPATH)/bin/axis

# Regression tests for install.sh. Hermetic: serves a local release tree over
# file:// so no network and no real system paths are touched.
test-install:
	./hack/install-tests.sh

# Full local mirror of required PR gates.
ci-preflight:
	./hack/ci-preflight.sh

# CI preflight plus the release vulnerability scan. Use --require-clean
# directly on the script for the exact post-merge commit that will be tagged.
release-preflight:
	./hack/release-preflight.sh

# Install to /usr/local/bin — the same absolute path on every node, and what
# install.sh writes by default. Preferred for any host in a cluster: per-user
# paths differ per account, so two nodes can sit on different releases while
# each one's `axis version` looks correct.
SYSTEM_PREFIX ?= /usr/local
install-system: build
	sudo mkdir -p $(SYSTEM_PREFIX)/bin
	sudo install -m 0755 axis $(SYSTEM_PREFIX)/bin/axis
	@echo "installed $(SYSTEM_PREFIX)/bin/axis (version $(VERSION) commit $(COMMIT))"
	@echo "verify: $(SYSTEM_PREFIX)/bin/axis version"
	@echo "check for duplicate installs: $(SYSTEM_PREFIX)/bin/axis doctor"

# Install to ~/.local/bin. Useful for development on a workstation; prefer
# install-system on cluster nodes so every host resolves one path.
install-user: build
	mkdir -p $(PREFIX)/bin
	install -m 0755 axis $(PREFIX)/bin/axis
	@echo "installed $(PREFIX)/bin/axis (version $(VERSION) commit $(COMMIT))"
	@echo "verify: $(PREFIX)/bin/axis version"
	@echo "daemon: $(PREFIX)/bin/axis daemon restart && $(PREFIX)/bin/axis daemon status"

# Test isolation lives in one executable abstraction so workflows that do not
# provide make can preserve the same operator-state boundary.
test:
	@./hack/hermetic-go-test.sh ./... -count=1 -timeout 180s
	./hack/hermetic-go-test-tests.sh
	python3 -B -m unittest discover -s docs/superpowers/tools -p '*_test.py'

test-race:
	@./hack/hermetic-go-test.sh -race ./... -count=1 -timeout 180s

# Fleet test: two-node read-only facts smoke. Gated behind //go:build fleet so
# it never runs in normal CI or make test. Set AXIS_FLEET_TARGET to the remote
# node ([user@]host[:port]) and ensure SSH key access to it; the test skips when
# that variable is unset.
test-fleet:
	@./hack/hermetic-go-test.sh -tags=fleet -count=1 -timeout 120s -v ./internal/fleettest/

lint:
	@unformatted=$$(gofmt -l .) || exit $$?; \
	if [ -n "$$unformatted" ]; then \
		echo "Files need gofmt:"; \
		echo "$$unformatted"; \
		exit 1; \
	fi
	go vet ./...
	go run honnef.co/go/tools/cmd/staticcheck@$(STATICCHECK_VERSION) ./...

coverage:
	./hack/coverage-check.sh

clean:
	rm -f axis
