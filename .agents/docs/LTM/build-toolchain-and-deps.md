# Build Model, Toolchain, and Dependency Sourcing

## Summary

The project is one combined Rust staticlib (`libimbhgo.a`) fusing sable's runtime + IMBH + the glue handlers + the C ABI, linked into a Go package built with `-tags sable_extern_lib`. This document captures the build model's load-bearing facts, the toolchain pin, the cgo constraints, and how the upstream dependencies are sourced (IMBH from crates.io `0.1.0` in lockstep; sable as a git dep) — including the `replace` vs `require` subtlety that makes the binding externally buildable.

## Key Facts

- **One combined staticlib.** `rust/` (crate `imbhgo`, `crate-type = staticlib`) → `rust/target/release/libimbhgo.a` (~450 MB; a cold build pulls the full DataFusion tree, minutes). Build it first after any Rust change — the Go build/tests link against it.
- **`-tags sable_extern_lib`** makes sable's own Go package contribute no `-lsable`; the linker resolves everything against `libimbhgo.a`. `CGO_LDFLAGS -limbhgo`.
- **Combined staticlib retains dependency `#[no_mangle]` symbols** — the dead-code-elision worry didn't materialize (`nm libimbhgo.a` shows all `sable_*` and `imbhgo_*`); no `#[used]` shim needed.
- **Toolchain is pinned:** Go 1.26.4 (module pins `toolchain go1.26.4`) to match sable — the fused runtime reaches Go's internal ABI via `//go:linkname`, so the toolchain must match what sable certified. Go lives at `/home/moriyoshi/opt/go-1.26.4/bin`.
- **cgo cannot live in a `_test.go`** (Go forbids `import "C"` there) — the cgo bridge is in a normal `.go`, tests are pure Go. This is why `debug.go` (not a test file) holds the leak-counter accessors.
- Env baseline: Go 1.26.4 (originally arm64, sable-certified), cargo 1.96, arrow-go v18.5.1, IMBH arrow 58.3.0, valgrind 3.22.0.

## Details

### Dependency sourcing (externally buildable, no path deps)

- **IMBH from crates.io, in lockstep at `0.1.0`:** `imbh` / `imbh-core` / `imbh-lgtm` all pinned to `0.1.0`. The lockstep pin is **load-bearing** — the direct `imbh-core` dep must be the SAME crate instance as imbh's own transitive `imbh-core`, else `imbh::Attributes != imbh_core::Attributes` (and `imbh::AnyValue != imbh_core::AnyValue`) in the metric-series / discovery glue. A comment in `Cargo.toml` flags this. Features: imbh `["cdata","proto","search","serde"]`, imbh-lgtm `["source"]`. (The `serde` feature — for the `PageCursor` round-trip — pulls no new crate.)
- **`imbh-core` is a direct dep** because the imbh facade doesn't re-export the helpers the discovery glue needs (`canonical_json_object`, `to_hex()`).
- **sable as a git dep** pinned to a **`main`** commit: Rust `sable = { git = "https://github.com/moriyoshi/sable", rev = "0c6fe56" }`; Go a **direct** `require github.com/moriyoshi/sable v0.0.0-20260726045720-0c6fe56eb099` with **no `replace`**. `0c6fe56` carries the memory-safety fix (PR #1), the Apple-target port (PR #2), and upstream's Windows work — the portable fallback (#6), the fast crossing (#7), and IOCP fd-fusion (#10). sable has **no tags**, so a `main` SHA is the most durable pin available.
- Local checkouts still live at `../imbh` and `../sable` for co-development. When landing an upstream change, re-pin (bump the crates.io version, or the git `rev` + Go pseudo-version) — do not revert to a path dep.

### The two dep-sourcing findings worth not re-deriving

1. **A cargo git dep finds a crate in a repo `rust/` subdir** by scanning the git tree — no repo-root workspace manifest is needed (the sable repo has its crate under `rust/`). The initial worry was wrong; confirmed empirically. The repo is public, so https resolves without credential wiring.
2. **`replace` ≠ `require` for a git-pinned Go dep.** `replace` directives apply only to the main module and are **ignored for downstream consumers** — pinning sable via `replace`-to-git would leave importers with the unbuildable `v0.0.0` require. A **direct `require`** at the commit's pseudo-version is what makes the binding externally importable. Resolve the pseudo-version with `GOPROXY=direct GOSUMDB=off go list -m <mod>@<sha>`.

### The local gate (applies to subagents too)

```
# Rust side (only when rust/ changed)
cargo build --release --manifest-path rust/Cargo.toml
cargo clippy --release --manifest-path rust/Cargo.toml -- -D warnings
cargo test  --manifest-path rust/Cargo.toml

# Go side (always; requires a current libimbhgo.a)
gofmt -l <changed .go files>          # must print nothing
go build -tags sable_extern_lib ./...
go vet   -tags sable_extern_lib ./...
go test  -tags sable_extern_lib -race ./...
```

`make test` = `make rust` + `go test -tags sable_extern_lib -race ./...`. `make rust` builds the staticlib.

### The staticlib is a global mutable resource

Only ONE agent may rebuild `rust/` at a time — a pure-Go agent running `go test` concurrently could link a half-written archive. For any parallel dispatch: serialize rust-touching work, let the rust-owning unit run the cargo rebuild + full gate first, then run pure-Go units against the fresh archive.

## Files

- `rust/Cargo.toml` — crate type, features, dep pins (crates.io imbh, git sable, direct imbh-core).
- `go.mod` — `toolchain go1.26.4`, direct sable require (no replace).
- `Makefile` — `rust`, `test`, `example`, `leak-valgrind`.
- `ARCHITECTURE.md §3` — canonical build model + dependency-sourcing paragraph.

## Pitfalls

- Building Go without a current `libimbhgo.a` links stale symbols — rebuild the staticlib after any Rust change.
- A `replace`-to-git for sable would compile locally but break every downstream importer — always use a direct require for the external pin.
- Breaking the lockstep between `imbh`/`imbh-core`/`imbh-lgtm` splits the `imbh-core` crate instance and produces confusing type-mismatch errors in the glue.
- A build-tagged file (e.g. `examples/quickstart/main.go`, gated `//go:build sable_extern_lib`) must carry the constraint, or a plain `go build ./...` (no tag) tries to link it and fails. Keep example/demo mains behind the tag.
- **The long-standing "545d04f is not on main" note was stale.** sable PR #1 merged that branch on 2026-07-24 (merge commit `bff774f`), so `545d04f` *is* an ancestor of `main` — the old pin was already durable, and the TODO to "merge 545d04f and re-pin" had in fact been satisfied upstream without anyone re-checking. Verify with `gh api repos/moriyoshi/sable/compare/main...<rev>` before repeating that claim.
- **`required_signatures` is checked against the incoming commit, not the merge commit.** sable's `main` ruleset enforces it, and an agent cannot satisfy it: the SSH signing key is passphrase-protected and no `ssh-agent` runs here. PR #1's head `545d04f` was signed (`verified=true`) and merged fine; the unsigned PR #2 head sat at `mergeable_state=blocked` until a human signed it. A GitHub-created *merge* commit being signed (`bff774f`, `verified=true, signer=GitHub`) does **not** on its own satisfy the rule. Practical consequence: **an agent can push a feature branch and open the PR (branches are exempt), but a human must sign before it can land** — and signing rewrites the SHA, so re-pin *after* the merge, never before.
- **Corollary: never pin a branch head you just pushed.** PR #2 landed as a signed, extended head (`2212f2f`, which also ported sable's Go suite to macOS), so the `62f3f34` this repo had pinned was left an orphan reachable from no branch. Always re-pin to the post-merge `main` SHA and re-run the gate.
