BIN         := satelle
SERVE_BIN   := satelled
PREFIX      ?= $(HOME)/.local
INSTALL_DIR := $(PREFIX)/bin

# Build identity from .version — CLI and serve carry independent version numbers
# (sty_19ff03f4); commit SHA and build stamp are shared (one commit → both artifacts).
# scripts/build-version.sh appends +<sha>[-dirty] when HEAD is not the tagged
# release commit or the tree is dirty, so isDevVersion treats make-built
# unreleased binaries as dev builds (sty_022929ef). CI release.yml stamps its
# own ldflags and never calls this script — published assets stay plain.
PKG         := github.com/bobmcallan/satelle/internal/buildinfo
BASE_VERSION := $(shell awk '$$1=="satelle.version:" {print $$2}' .version)
BASE_SERVE_VERSION := $(shell awk '$$1=="satelled.version:" {print $$2}' .version)
VERSION     := $(shell bash scripts/build-version.sh $(BASE_VERSION))
SERVE_VERSION := $(shell bash scripts/build-version.sh $(BASE_SERVE_VERSION) serve-v)
COMMIT      := $(shell git rev-parse --short=12 HEAD 2>/dev/null || echo none)
BUILD_TIME  := $(shell awk '$$1=="satelle.build:" {print $$2}' .version)
LDFLAGS     := -X $(PKG).Name=satelle -X $(PKG).Version=$(VERSION) -X $(PKG).Commit=$(COMMIT) -X $(PKG).BuildTime=$(BUILD_TIME)
SERVE_LDFLAGS := -X $(PKG).Name=satelled -X $(PKG).Version=$(SERVE_VERSION) -X $(PKG).Commit=$(COMMIT) -X $(PKG).BuildTime=$(BUILD_TIME)

.PHONY: build install uninstall test integration judgment check-serve-version

build:
	go build -ldflags "$(LDFLAGS)" -o $(BIN) ./cmd/satelle
	go build -ldflags "$(SERVE_LDFLAGS)" -o $(SERVE_BIN) ./cmd/satelled

# Fail closed when serve-path sources changed since the last serve-v* tag but
# satelled.version was not advanced (sty_4a5c6924). Run on EVERY release,
# not only slices the author reads as serve-path (sty_a8853e85). The watch set is
# derived from `go list -deps ./cmd/satelled`; print it with
# `bash scripts/check-serve-version.sh --paths`.
check-serve-version:
	@bash scripts/check-serve-version.sh

# install places both binaries on PATH (~/.local/bin by default — machine-wide).
# SCOPE DECISION (sty_022929ef AC4): keep the fleet-wide default. Named failure
# mode: every repo on the host resolves this binary; mitigated (not removed) by
# the honest +sha/-dirty stamp so isDevVersion demotes unreleased installs out
# of gating. PREFIX=… overrides for a repo-local install. Refusing dirty trees
# and defaulting to repo-local were rejected: both push people to unstamped
# `go build -o ~/.local/bin/satelle` with the same fleet blast radius.
# Afterwards, run `satelle service install` inside a repo to start the always-on web service.
install: build
	mkdir -p $(INSTALL_DIR)
	install -m 0755 $(BIN) $(INSTALL_DIR)/$(BIN)
	install -m 0755 $(SERVE_BIN) $(INSTALL_DIR)/$(SERVE_BIN)
	@echo "installed $(INSTALL_DIR)/$(BIN) and $(INSTALL_DIR)/$(SERVE_BIN) (version $(VERSION) / serve $(SERVE_VERSION))"
	@echo "next: cd <repo> && satelle init && satelle service install"

uninstall:
	rm -f $(INSTALL_DIR)/$(BIN) $(INSTALL_DIR)/$(SERVE_BIN)
	@echo "removed $(INSTALL_DIR)/$(BIN) and $(INSTALL_DIR)/$(SERVE_BIN) (run 'satelle service uninstall' first if the service is installed)"

# Every go test target below runs under scripts/credguard.sh, which fails the
# target if the suite changed the operator's host credentials file (sty_18403814),
# the top level of the real ~/.satelle, or the installed satelle/satelled
# binaries (sty_1b739a74).
CREDGUARD := sh scripts/credguard.sh --

# HERMETIC runs a command under an empty test-owned HOME/XDG and a minimal PATH so
# the suites do not depend on the operator's machine (sty_ec30f859). Test flags go
# through TESTFLAGS. credguard stays OUTSIDE it so it keeps watching the real host.
HERMETIC := sh scripts/hermetic.sh --

# STRICT=1 is the strict local mode (sty_53c3f311): tests run repeated and
# shuffled (-count=3 -shuffle=on) to expose order and repetition leaks, through
# scripts/skipcheck, which fails the run on a skip that is not on
# scripts/skip-allow.txt and names a test that stalls past STALL. With STRICT
# unset every variable below is empty. -tree . fails the run when a test writes
# into the source tree.
STRICT ?=
ifeq ($(STRICT),1)
STALL := 5m
SKIPCHECK := go run ./scripts/skipcheck -allow scripts/skip-allow.txt -stall $(STALL) -tree . --
STRICTFLAGS := -json -count=3 -shuffle=on -timeout 30m
endif

# TESTJSON=<file> writes the run's go test -json stream to that file instead of
# the terminal; CI uploads it and prints package times from it.
TESTJSON ?=
ifneq ($(TESTJSON),)
JSONFLAG := -json
JSONOUT := > $(TESTJSON)
endif

# test runs every package, the integration suite under tests/ included: it
# builds satelle and satelled once and drives them in throwaway homes.
test:
	$(CREDGUARD) $(HERMETIC) $(SKIPCHECK) go test $(STRICTFLAGS) $(JSONFLAG) $(TESTFLAGS) ./... $(JSONOUT)

# integration runs only the integration suite under tests/.
integration:
	$(CREDGUARD) $(HERMETIC) go test $(TESTFLAGS) ./tests/

# judgment: opt-in LLM rubric fixtures (sty_6830e78e). Costs tokens, not hermetic,
# never in default CI. See README ## Testing.
judgment:
	$(CREDGUARD) go test -tags llm ./tests/llm/...

