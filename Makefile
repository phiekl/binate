# SPDX-FileCopyrightText: 2026 Philip Eklöf
#
# SPDX-License-Identifier: MIT

BUILD_DIR := build
BINARY := $(BUILD_DIR)/binate
MANPAGES := $(BUILD_DIR)/man/binate.1 $(BUILD_DIR)/man/binate-manual.7
# A man page's date: MAN_DATE if set, or its source's last commit date.
man_date = $(or $(MAN_DATE),$(shell git log -1 --format=%cs -- $(1) 2>/dev/null))
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

MAN_SCRIPTS := doc/scripts/md2man.sed doc/scripts/man.sed Makefile

# man_page renders Markdown $(1) as man page $(2) of section $(3), with
# volume title $(4), and post-processes it with sed scripts $(5) besides
# doc/scripts/man.sed, which keeps options typeable in UTF-8 output.
# doc/scripts/md2man.sed turns the Markdown's title and summary into the
# page's NAME. Links within a page leave their section's name, without the
# anchor go-md2man puts on the next line.
define man_page
	mkdir -p $(@D)
	{ printf '%s\n==\n\n# NAME\n\n%s - ' '$(2) $(3) "$(call man_date,$(1))" binate "$(4)"' '$(2)'; \
		sed -E -f doc/scripts/md2man.sed $(1); } > $@.md
	$(MD2MAN) -in $@.md -out $@
	rm $@.md
	sed -E -i -e '$$!N' -e 's/\n\\\[la\]#([^\\]|\\[^[])*\\\[ra\]//' -e 'P' -e 'D' $@
	sed -E -i -f doc/scripts/man.sed $(addprefix -f ,$(5)) $@
endef

$(BUILD_DIR)/man/binate.1: doc/binate.1.md doc/scripts/keys.sed $(MAN_SCRIPTS)
	$(call man_page,$<,binate,1,User Commands,doc/scripts/keys.sed)

$(BUILD_DIR)/man/binate-manual.7: doc/manual.md $(MAN_SCRIPTS)
	$(call man_page,$<,binate-manual,7,Miscellaneous Information Manual)

test:
	CGO_ENABLED=0 go test $(GOTAGS) ./...

FUZZTIME ?= 10s
# Of each input that expands coverage, during which a worker reports nothing,
# so that of few workers, fuzzing mostly stalls at the default of 60s.
FUZZMINIMIZETIME ?= 5s
# Each fuzz test as package:name, as go test fuzzes one at a time.
FUZZ_TESTS := \
	display:FuzzContent display:FuzzContentPartial display:FuzzName display:FuzzStderr display:FuzzShellQuote \
	term:FuzzParseKey diff:FuzzLines diff:FuzzInline \
	git:FuzzParseTree git:FuzzLooseHeader
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
