VERSION  := $(shell grep 'Version =' internal/buildinfo/version.go | cut -d'"' -f2)
COMMIT   := $(shell git rev-parse --short HEAD 2>/dev/null || echo unknown)
DATE     := $(shell date -u +%Y-%m-%dT%H:%M:%SZ)
GOVERSION := $(shell go version | awk '{print $$3}')
PREFIX   ?= $(HOME)/.local
# Pinned: an unpinned @latest would let an upstream release break CI without a
# repository change. Bump deliberately.
STATICCHECK_VERSION ?= 2025.1.1

LDFLAGS  := -s -w \
	-X github.com/toasterbook88/axis/internal/buildinfo.Commit=$(COMMIT) \
	-X github.com/toasterbook88/axis/internal/buildinfo.Date=$(DATE) \
	-X github.com/toasterbook88/axis/internal/buildinfo.GoVersion=$(GOVERSION) \
	-X github.com/toasterbook88/axis/internal/buildinfo.UpdateManagedBy=

.PHONY: build test test-race lint coverage clean install install-user install-system test-install

build:
	CGO_ENABLED=0 go build -trimpath -ldflags "$(LDFLAGS)" -o axis ./cmd/axis/

# Install to GOPATH/bin (legacy; often not on operator PATH).
install: build
	cp axis $(shell go env GOPATH)/bin/axis

# Regression tests for install.sh. Hermetic: serves a local release tree over
# file:// so no network and no real system paths are touched.
test-install:
	./hack/install-tests.sh

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

# Test isolation (audit finding C5). Unisolated tests resolve every AXIS store
# to ~/.axis, so the suite used to write real operator state on every run. The
# harness redirects HOME to a disposable directory.
#
# Two details are load-bearing:
#
#   - GOCACHE, GOPATH, and GOMODCACHE derive from HOME. They are captured from
#     the real home in the assignment prefix, which the shell expands before it
#     applies the HOME assignment, so the build cache survives.
#   - AXIS_HOME is deliberately NOT exported here. It outranks HOME in
#     persist.AxisDir, so a single suite-wide AXIS_HOME would override the ~133
#     tests that isolate themselves with t.Setenv("HOME", ...) and collapse them
#     onto one shared AXIS root.
test:
	@d=$$(mktemp -d "$${TMPDIR:-/tmp}/axis-test-home.XXXXXX"); \
	trap 'rm -rf "$$d"' EXIT; \
	GOCACHE=$$(go env GOCACHE) GOPATH=$$(go env GOPATH) GOMODCACHE=$$(go env GOMODCACHE) \
	HOME="$$d" AXIS_HOME= go test ./... -count=1 -timeout 180s
	python3 -B -m unittest discover -s docs/superpowers/tools -p '*_test.py'

test-race:
	@d=$$(mktemp -d "$${TMPDIR:-/tmp}/axis-test-home.XXXXXX"); \
	trap 'rm -rf "$$d"' EXIT; \
	GOCACHE=$$(go env GOCACHE) GOPATH=$$(go env GOPATH) GOMODCACHE=$$(go env GOMODCACHE) \
	HOME="$$d" AXIS_HOME= go test -race ./... -count=1 -timeout 180s

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
