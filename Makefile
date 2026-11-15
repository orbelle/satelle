BIN         := satelle
SERVE_BIN   := satelled
PREFIX      ?= $(HOME)/.local
INSTALL_DIR := $(PREFIX)/bin

PKG         := github.com/bobmcallan/satelle/internal/buildinfo
BASE_VERSION := $(shell awk '$$1=="satelle.version:" {print $$2}' .version)
BASE_SERVE_VERSION := $(shell awk '$$1=="satelled.version:" {print $$2}' .version)
VERSION     := $(shell bash scripts/build-version.sh $(BASE_VERSION))
SERVE_VERSION := $(shell bash scripts/build-version.sh $(BASE_SERVE_VERSION) serve-v)
COMMIT      := $(shell git rev-parse --short=12 HEAD 2>/dev/null || echo none)
BUILD_TIME  := $(if $(filter $(BASE_VERSION),$(VERSION)),$(shell awk '$$1=="satelle.build:" {print $$2}' .version),$(shell date -u +%Y-%m-%dT%H:%M:%SZ))
LDFLAGS     := -X $(PKG).Name=satelle -X $(PKG).Version=$(VERSION) -X $(PKG).Commit=$(COMMIT) -X $(PKG).BuildTime=$(BUILD_TIME)
SERVE_LDFLAGS := -X $(PKG).Name=satelled -X $(PKG).Version=$(SERVE_VERSION) -X $(PKG).Commit=$(COMMIT) -X $(PKG).BuildTime=$(BUILD_TIME)

.PHONY: build install uninstall test integration judgment check-serve-version

build:
	go build -ldflags "$(LDFLAGS)" -o $(BIN) ./cmd/satelle
	go build -ldflags "$(SERVE_LDFLAGS)" -o $(SERVE_BIN) ./cmd/satelled

check-serve-version:
	@bash scripts/check-serve-version.sh

install: build
	mkdir -p $(INSTALL_DIR)
	install -m 0755 $(BIN) $(INSTALL_DIR)/$(BIN)
	install -m 0755 $(SERVE_BIN) $(INSTALL_DIR)/$(SERVE_BIN)
	@echo "installed $(INSTALL_DIR)/$(BIN) and $(INSTALL_DIR)/$(SERVE_BIN) (version $(VERSION) / serve $(SERVE_VERSION))"
	@echo "next: cd <repo> && satelle init && satelle service install"

uninstall:
	rm -f $(INSTALL_DIR)/$(BIN) $(INSTALL_DIR)/$(SERVE_BIN)
	@echo "removed $(INSTALL_DIR)/$(BIN) and $(INSTALL_DIR)/$(SERVE_BIN) (run 'satelle service uninstall' first if the service is installed)"

CREDGUARD := sh scripts/credguard.sh --

HERMETIC := sh scripts/hermetic.sh --

STRICT ?=
ifeq ($(STRICT),1)
STALL := 5m
SKIPCHECK := go run ./scripts/skipcheck -allow scripts/skip-allow.txt -stall $(STALL) -tree . --
STRICTFLAGS := -json -count=3 -shuffle=on -timeout 30m
endif

TESTJSON ?=
ifneq ($(TESTJSON),)
JSONFLAG := -json
JSONOUT := > $(TESTJSON)
endif

test:
	$(CREDGUARD) $(HERMETIC) $(SKIPCHECK) go test -tags integration $(STRICTFLAGS) $(JSONFLAG) $(TESTFLAGS) ./... $(JSONOUT)

integration:
	$(CREDGUARD) $(HERMETIC) go test -tags integration $(TESTFLAGS) ./tests/

judgment:
	$(CREDGUARD) go test -tags llm ./tests/llm/...

