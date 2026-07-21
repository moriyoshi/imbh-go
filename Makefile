# imbh-go — build the combined staticlib, then the Go binding.
#
# The combined libimbhgo.a contains sable's runtime + imbh + our handlers + the imbhgo_*/sable_* C
# ABI. The Go package is built with -tags sable_extern_lib so sable's own Go package contributes no
# -lsable; the linker resolves everything against libimbhgo.a.

RUST_DIR := rust
RUST_LIB := $(RUST_DIR)/target/release/libimbhgo.a
TAGS     := sable_extern_lib

.PHONY: all rust test test-v leak-valgrind example release-local clean

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
