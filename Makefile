# gmk — convenience Makefile
#
# This Makefile is a thin human-friendly wrapper over `go build` and
# `go test`. None of it is required for the build to work — `go test ./...`
# and `go build ./cmd/gmk` do the same thing. It's here so devs can type
# `make examples` instead of remembering the integration package path.
#
# Self-hosting note: once Stage 5 lands file targets (with declared
# outputs), this Makefile becomes deletable — gmk will be able to drive
# its own examples via a top-level build.yml. Stage 5 is the right point
# to do that switch: it requires real input/output declarations to
# express "run all examples and cache the result".

.PHONY: all build test examples vet fmt clean help

# Default goal — quick smoke check.
all: vet test

# Build the gmk binary into ./gmk
build:
	go build -o gmk ./cmd/gmk

# Run the full test suite (all packages, race detector on).
test:
	go test -race -count=1 ./...

# Run only the examples regression battery.
# Walks every build.yml under examples/ and verifies parse, resolve,
# target consistency, structural shape, and actual execution (exit 0).
examples:
	go test -race -count=1 -v ./internal/integration/...

# Run only the example-execution test (verbose, shows shell output).
examples-run:
	go test -race -count=1 -v -run TestExamplesRun ./internal/integration/...

# go vet across the module.
vet:
	go vet ./...

# Standard gofmt over all .go files.
fmt:
	gofmt -w -s .

# Clean built binary and any .gmk-cache dirs in examples.
clean:
	rm -f gmk gmk.exe
	find examples -type d -name .gmk-cache -prune -exec rm -rf {} \;

# Short help text.
help:
	@echo "gmk Makefile targets:"
	@echo "  make build         — build ./gmk binary"
	@echo "  make test          — go test -race ./..."
	@echo "  make examples      — run the examples/ regression battery"
	@echo "  make examples-run  — same, verbose, with shell output"
	@echo "  make vet           — go vet ./..."
	@echo "  make fmt           — gofmt -w -s ."
	@echo "  make clean         — remove ./gmk and .gmk-cache/ in examples"
