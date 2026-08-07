# Documents for both humans and coding agents

* [ARCHITECTURE.md](./ARCHITECTURE.md) ... system architecture (canonical; human-reader-ready, as-built). Read this before changing subsystem boundaries, the build model, the batch handoff, or the ownership protocol.

# Documents for coding agents

* [./.agents/docs/OVERVIEW.md](./.agents/docs/OVERVIEW.md) ... project overview for coding agents.
* [./.agents/docs/PLAN.md](./.agents/docs/PLAN.md) ... the consolidated planning and prescription record (zero-copy design and phasing, the imbh/sable upstream prescriptions, the binding milestones, the two-free ownership protocol, and the I-4a lazy-scan detail). Merges the former `docs/` design files.
* [./.agents/docs/JOURNAL.md](./.agents/docs/JOURNAL.md) ... findings, insights, and peer code review history. Append-only.
* [./.agents/docs/LTM/INDEX.md](./.agents/docs/LTM/INDEX.md) ... long-term memory index for durable project knowledge under `./.agents/docs/LTM/`.
* [./.agents/docs/TODO.md](./.agents/docs/TODO.md) ... open to-do items extracted from JOURNAL.md during `good-sleep` consolidation. Check and update this file when picking up or finishing work.

# Rules and protocols

## General

* imbh-go is a Go binding of IMBH (`../imbh`, a Rust embeddable observability DB) built on top of sable (`../sable`, a Rust-tokio ⇄ Go-scheduler fusion) as the FFI transport. The query path is fully async and streaming: each Arrow record batch crosses the FFI boundary zero-copy via the Arrow C Data Interface behind a single sable `Payload::Handle`. Read `ARCHITECTURE.md` (as-built) and `.agents/docs/PLAN.md` (the design, prescriptions, and milestones) before changing the build model, the batch handoff, or the ownership protocol.
* The project is one combined Rust staticlib (`rust/`, crate `imbhgo`, `crate-type = staticlib`) that fuses sable's runtime + imbh + the glue handlers + the `imbhgo_*`/`sable_*` C ABI, plus a Go package that wraps it. The Go package is built with `-tags sable_extern_lib` so sable's own Go package contributes no `-lsable`; the linker resolves everything against `libimbhgo.a`.
* The binding consumes both upstreams as external dependencies, not path deps: imbh from crates.io (`imbh`/`imbh-core`/`imbh-lgtm` pinned in lockstep at `0.6.0`, so the `imbh-core` instance unifies), and sable as a git dep pinned to a **`main`** commit (Rust `sable = { git = "https://github.com/moriyoshi/sable", rev = "0c6fe56" }`; Go a direct `require github.com/moriyoshi/sable <pseudo-version>` with no `replace` — a `replace` is ignored for downstream consumers, so the direct require is what keeps this binding externally buildable). Local checkouts still live at `../imbh` and `../sable` for co-development; when you land an upstream change there, re-pin the dep (bump the crates.io version, or the git `rev` + Go pseudo-version) rather than reverting to a path dep. Keep this binding sable-agnostic where imbh is concerned and imbh-agnostic where sable is concerned: the glue that knows about both lives only in `rust/` and the Go package here.

## File Management

* When you'd make summary documents for your work, write them under `./.agents/docs`, not under `/tmp`.
* Temporary files should be created under `./.agents-workspace/tmp`, not under `/tmp`.
* ❌ Do not build artifacts into the version-controlled tree. The Rust staticlib already builds under `rust/target/` (gitignored); write any scratch binaries to `./.agents-workspace/tmp`.
* ❌ Never delete user files without permission. Only safe to delete: files YOU created in THIS session that are in `./.agents-workspace/tmp/`. Always ask first if unsure. Assume all pre-existing files belong to the user.

## Building

* Go is installed at `/home/moriyoshi/opt/go-1.26.4/bin/go` (Go 1.26.4). The module pins `toolchain go1.26.4` to match sable — the fused runtime reaches Go's internal ABI via `//go:linkname`, so the toolchain version must match what sable certified. Put `/home/moriyoshi/opt/go-1.26.4/bin` on `PATH` before invoking `go`.
* Rust: `cargo build --release --manifest-path rust/Cargo.toml` (or `make rust`) produces `rust/target/release/libimbhgo.a` (~450 MB; a cold build pulls the full DataFusion tree and takes minutes). The Go build and tests link against this archive, so build it first after any Rust change.
* Run `gofmt -w` on every Go file you change before running `go build`, `go vet`, or `go test`, and before reporting a change as done.
* The standard local gate for any change you make — this applies to subagents too:
  ```
  # Rust side (only when rust/ changed)
  cargo build --release --manifest-path rust/Cargo.toml
  cargo clippy --release --manifest-path rust/Cargo.toml -- -D warnings
  cargo test --manifest-path rust/Cargo.toml            # or a focused crate

  # Go side (always; requires libimbhgo.a to be current)
  gofmt -l <changed .go files>                          # must print nothing
  go build -tags sable_extern_lib ./...
  go vet   -tags sable_extern_lib ./...
  go test  -tags sable_extern_lib -race ./...
  ```
  `make test` runs `make rust` + `go test -tags sable_extern_lib -race ./...` in one step. Fix violations and re-run until clean. Do not declare a change complete with a failing build, vet, clippy, or tests.

## Testing

* Make sure regression tests are ready for your fix.
* Always run the Go tests under `-race`. The binding hands Arrow buffers across the FFI boundary with a two-free ownership protocol (taken path `imbhgo_shell_free` vs abandoned path `imbhgo_batch_release`), so the load-bearing tests are the leak / use-after-free gates: full-drain, early-close-with-buffered-batch, and shutdown-with-open-cursor. Add or extend these whenever you touch batch handoff, handle lifetime, or stream cancellation.
* A leak/UAF gate under ASAN/LSan is still pending (see `TODO.md`); prefer designing new FFI tests so they can also run under a sanitizer.
* Tests must run without external daemons or network. imbh runs in-process (in-memory or a local segment store); do not introduce tests that need a live service.

## Git Workflow

* This directory is not yet a git repository. If you initialize one, the `.gitignore` files under `.agents/`, `.agents-workspace/`, and `.claude/` already exclude scratch/worktree/local-settings state.
* ❌ Do not run `git checkout` or `git restore` against the working tree — another agent may be working concurrently in the same directory.
* ❌ Never make discretionary commits. Commit or push only when the user explicitly asks.

## Documentation

* Try to write your work summary to one of the existing documents under `./.agents/docs`.
* ❌ Avoid editing any existing sections of `JOURNAL.md`. Append new entries to the end. (The sole exception is the `reconcile-journal-ltm` skill, which may remove entries already consolidated into `.agents/docs/LTM/` per the canonical `## LTM Consolidation Record`.)
* ❌ For repo-authored documentation only (e.g. `AGENTS.md`, `README.md`, `docs/**`, `.agents/docs/**`), never use full-width parentheses (`（` `）`). Use half-width parentheses (`(` `)`) with a half-width space before/after when adjacent to a non-whitespace character. This does not apply to generated or third-party reference files under `.agents/skills/**/references/**`.
* ❌ For repo-authored documentation only, never use full-width colons (`：`). Use a half-width colon followed by a half-width space. This does not apply to generated or third-party reference files under `.agents/skills/**/references/**`.

## Shell Pitfalls (prezto defaults)

The user's shell uses prezto, which sets aliases and options that break non-interactive scripts:

* ❌ `cp src dst` prompts interactively when `dst` exists (prezto aliases `cp` to `cp -i`). Always `rm -f dst` before `cp`.
* ❌ `cat > file <<'EOF'` and `echo > file` fail with `file exists` when the target exists (prezto enables `NO_CLOBBER`). Workaround: `rm -f file` before writing, or use `tee` / `/bin/cat`.
* ❌ `rm file` prompts for confirmation on some files (prezto aliases `rm` to `rm -i`). Always use `rm -f` for non-interactive deletion.
