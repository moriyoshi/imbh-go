# imbh-go — build the combined staticlib, then the Go binding.
#
# The combined libimbhgo.a contains sable's runtime + imbh + our handlers + the imbhgo_*/sable_* C
# ABI. The Go package is built with -tags sable_extern_lib so sable's own Go package contributes no
# -lsable; the linker resolves everything against libimbhgo.a.

RUST_DIR := rust
RUST_LIB := $(RUST_DIR)/target/release/libimbhgo.a
TAGS     := sable_extern_lib

.PHONY: all rust test test-v leak-valgrind example release release-local clean

all: rust

# The heavy build (pulls the full DataFusion tree on a cold cache).
rust:
	cargo build --release --manifest-path $(RUST_DIR)/Cargo.toml

$(RUST_LIB): rust

test: $(RUST_LIB)
	go test -tags $(TAGS) -race ./...

test-v: $(RUST_LIB)
	go test -tags $(TAGS) -race -v ./...

# Buffer-level leak gate: run under Valgrind and assert no Rust/Arrow buffer or FfiBatch shell leaks.
# Complements the in-process live-batch counter (TestNoLeak) by proving the Arrow *buffers* are freed.
# Requires valgrind; slow. See scripts/valgrind-leak-gate.sh for the libc-malloc-only filter rationale.
leak-valgrind: $(RUST_LIB)
	bash scripts/valgrind-leak-gate.sh

# Run the runnable tour in examples/quickstart.
example: $(RUST_LIB)
	go run -tags $(TAGS) ./examples/quickstart

# Cut a release: rewrite the version across the tree, then run the full local gate against it.
# Stops short of committing and tagging — pushing the tag is what triggers publication, so it stays
# a deliberate manual step. Prints the remaining commands on success.
release: $(RUST_LIB)
	@[ -n "$(VERSION)" ] || { echo "usage: make release VERSION=vMAJOR.MINOR.PATCH" >&2; exit 2; }
	bash scripts/bump-version.sh "$(VERSION)"
	go build -tags $(TAGS) ./...
	go vet -tags $(TAGS) ./...
	go test -tags $(TAGS) -race ./...
	@echo
	@echo "Gate passed for $(VERSION). Remaining steps:"
	@echo "  git switch -c release/$(VERSION)"
	@echo "  git commit -S -am 'release: bump the module release to $(VERSION)'"
	@echo "  git push -u origin release/$(VERSION) && gh pr create --base main"
	@echo "  # after the PR merges, from an up-to-date main:"
	@echo "  git tag $(VERSION) && git push origin $(VERSION)   # this publishes"

# Package a prebuilt archive for the host cell (strip + zstd + checksum) into .agents-workspace/tmp/release.
# CI builds the full per-platform matrix; this is for testing the packaging locally. Override the cell
# via RUST_TARGET/GOOS/GOARCH/LIBC (see scripts/build-release.sh).
release-local:
	RUST_TARGET=$${RUST_TARGET:-$$(rustc -vV | sed -n 's/^host: //p')} \
	GOOS=$${GOOS:-$$(go env GOOS)} GOARCH=$${GOARCH:-$$(go env GOARCH)} \
	bash scripts/build-release.sh

clean:
	cargo clean --manifest-path $(RUST_DIR)/Cargo.toml
	go clean -testcache
