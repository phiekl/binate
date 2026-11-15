# SPDX-FileCopyrightText: 2026 Philip Eklöf
#
# SPDX-License-Identifier: MIT

BUILD_DIR := build
BINARY := $(BUILD_DIR)/binate
MANPAGES := $(BUILD_DIR)/man/binate.1
MAN_DATE ?= $(shell git log -1 --format=%cs -- doc/binate.1.md 2>/dev/null)
GOAMD64 ?= v3
VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null)
MD2MAN ?= go-md2man
TAGS ?=
GOTAGS := $(if $(TAGS),-tags $(TAGS))

.PHONY: all build man test fuzz vet clean

all: build

# Pass the version through the environment, so the shell does not parse it.
build: export BINATE_VERSION = $(VERSION)
build:
	mkdir -p $(BUILD_DIR)
	CGO_ENABLED=0 GOAMD64=$(GOAMD64) go build $(GOTAGS) -trimpath -ldflags="-s -w -X main.version=$$BINATE_VERSION" -o $(BINARY) .

man: $(MANPAGES)

# doc/scripts/man.sed keeps options typeable in UTF-8 output.
$(BUILD_DIR)/man/binate.1: doc/binate.1.md doc/scripts/man.sed Makefile
	mkdir -p $(@D)
	$(MD2MAN) -in doc/binate.1.md -out $@
	sed -E -i -e '/^\.TH /s/""/"$(MAN_DATE)"/' -f doc/scripts/man.sed $@

test:
	CGO_ENABLED=0 go test $(GOTAGS) ./...

FUZZTIME ?= 10s
# Of each input that expands coverage, during which a worker reports nothing,
# so that of few workers, fuzzing mostly stalls at the default of 60s.
FUZZMINIMIZETIME ?= 5s
# Each fuzz test as package:name, as go test fuzzes one at a time.
FUZZ_TESTS := \
	display:FuzzContent display:FuzzContentPartial display:FuzzName display:FuzzStderr display:FuzzShellQuote \
	term:FuzzParseKey diff:FuzzLines diff:FuzzInline
# All are run, failing at the end if any failed.
fuzz:
	failed=; for t in $(FUZZ_TESTS); do \
		go test $(GOTAGS) ./internal/$${t%%:*} -run '^$$' -fuzz "^$${t#*:}\$$" -fuzztime=$(FUZZTIME) -fuzzminimizetime=$(FUZZMINIMIZETIME) -parallel=2 || failed="$$failed $$t"; \
	done; \
	if [ -n "$$failed" ]; then echo "failed:$$failed"; exit 1; fi

vet:
	go vet $(GOTAGS) ./...

clean:
	rm -rf $(BUILD_DIR)
