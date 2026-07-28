# Journal

Append-only log of findings, insights, and peer code-review history. Newest entries at the bottom. Do not edit existing entries; append new ones. The `good-sleep` skill consolidates entries into `.agents/docs/LTM/`; `reconcile-journal-ltm` is the only workflow allowed to remove already-consolidated entries.

New work goes below this line as a fresh dated entry. Everything through the 2026-07-24 session has been
consolidated into `.agents/docs/LTM/` and removed from this file — see the canonical record at the bottom.

---

## LTM Consolidation Record

The journal has been **audited against `.agents/docs/LTM/`** (`reconcile-journal-ltm`, 2026-07-25):
every substantive entry from the initial build arc (feasibility → M0–M4 → typed queries → LGTM →
externalization) had its durable knowledge verified present in the LTM documents below, its open
follow-ups moved to `.agents/docs/TODO.md`, and the original entries then removed (LTM is the durable
form). This is the single canonical consolidation record; older per-pass records were merged into it.

### Journal section → LTM document

| Consolidated journal section | LTM document(s) |
|------------------------------|-----------------|
| Agent harness adopted | build-toolchain-and-deps |
| Session summary: feasibility → design → upstream reviews → M0 | zero-copy-arrow-handoff, streaming-query-errors-cancellation, build-toolchain-and-deps |
| Canonical ARCHITECTURE.md added | build-toolchain-and-deps |
| M2 ingest delivered (the full loop) | ingest-and-backpressure |
| M3 errors & cancellation delivered | streaming-query-errors-cancellation |
| M4 hardening: the leak gate | leak-uaf-verification |
| M4 hardening: Valgrind buffer-leak gate | leak-uaf-verification |
| M4: ingest backpressure | ingest-and-backpressure |
| M1 typed queries (scoped, native/JSON encoding) | single-transport-typed-queries |
| Finding: "typed-struct results" mostly reframe as Arrow | single-transport-typed-queries |
| Go-side Arrow→struct decoder (+ a real zero-copy footgun) | arrow-buffer-lifetime-rules, single-transport-typed-queries |
| M1 typed queries complete (span-metrics + Matrix/RED decoders) | single-transport-typed-queries |
| Capstone: implementation phase complete | zero-copy-arrow-handoff, arrow-buffer-lifetime-rules, single-transport-typed-queries |
| User-facing docs, example, license (packaging polish) | build-toolchain-and-deps |
| LGTM query languages (PromQL / LogQL / TraceQL) → Arrow | lgtm-query-languages |
| Capstone II: full binding (SQL + typed + LGTM) | single-transport-typed-queries, lgtm-query-languages |
| Upstream IMBH drift: 3 breaking changes fixed, traces().get_batches | imbh-upstream-surface, ingest-and-backpressure |
| Diffed against imbh-tui: LogQL selectors closed; raw metric points | lgtm-query-languages, single-transport-typed-queries |
| README feature-coverage matrix (+ doc drift fixes) | imbh-upstream-surface |
| tackle-todos sweep: LogQuery builder + Trace tree assembly | imbh-upstream-surface, single-transport-typed-queries |
| tackle-todos sweep (cont'd): discovery/catalog surface + clippy repair | imbh-upstream-surface |
| tackle-todos sweep (final round): traces().search + robustness + sable bug | imbh-upstream-surface, sable-ffi-integration |
| Sweep close-out (2026-07-24) | imbh-upstream-surface |
| sweep addendum: metrics().instant + TraceQuery predicate parity | imbh-upstream-surface |
| adopt imbh-lgtm's Arrow-native execute path | lgtm-query-languages |
| Admin/lifecycle surface + LogPage cursor paging | imbh-upstream-surface, streaming-query-errors-cancellation |
| imbh migrated to crates.io 0.1.0 | build-toolchain-and-deps |
| sable pinned as a git dep | build-toolchain-and-deps |
| Session capstone: remaining-TODO sweep + externalization + doc sync | build-toolchain-and-deps, streaming-query-errors-cancellation, imbh-upstream-surface |
| Context-only query API + OP_LOG_COUNT | single-transport-typed-queries, sable-ffi-integration |
| Finding: sable.CallCtx genuinely aborts the Rust-side future | sable-ffi-integration |

### LTM documents (all standalone; no synthesis/merge layer yet)

| Document | Captures |
|----------|----------|
| zero-copy-arrow-handoff | Arrow C Data Interface handoff + the two-free ownership protocol |
| arrow-buffer-lifetime-rules | Move-on-import, `String.Value` aliasing, the `strings.Clone` rule |
| leak-uaf-verification | The `LIVE_BATCHES` counter gate + the Valgrind buffer gate |
| streaming-query-errors-cancellation | Per-batch cursor, out-of-band error channel, `NextCtx`, the scalar side-channel pattern |
| ingest-and-backpressure | OTLP byte-`Call` ingest + the global admission cap |
| single-transport-typed-queries | Arrow-everywhere architecture, native-JSON typed queries, Go-side decoders |
| lgtm-query-languages | PromQL/LogQL/TraceQL wiring, `execute_*_batches`, language semantics |
| imbh-upstream-surface | IMBH constraints, drift, column types, admin/ops surface + op-id map, working practices |
| build-toolchain-and-deps | Combined staticlib build model, toolchain pin, cgo constraints, dependency sourcing |
| sable-ffi-integration | sable API shapes, real cancellation, the empty-result `0x1` memory-safety bug + fix |

Open follow-ups extracted to `.agents/docs/TODO.md` ("Watch-items / deferred"). Full index:
`.agents/docs/LTM/INDEX.md`.

---

## Deep Sleep Consolidation Record

`deep-sleep` (2026-07-25) — second-stage consolidation. The 10 topic-level LTM documents were grouped
into 3 synthesis documents for faster orientation; **all source documents were kept intact** for
traceability (no deletions, no overwrites).

| Synthesis document | Consolidates |
|--------------------|--------------|
| ffi-ownership-and-safety-synthesis | zero-copy-arrow-handoff, arrow-buffer-lifetime-rules, leak-uaf-verification |
| data-path-synthesis | ingest-and-backpressure, streaming-query-errors-cancellation, single-transport-typed-queries, lgtm-query-languages |
| upstream-integration-synthesis | build-toolchain-and-deps, imbh-upstream-surface, sable-ffi-integration |

Every source document feeds a synthesis; none were left standalone. `.agents/docs/LTM/INDEX.md` now
separates synthesis documents from source documents. See it for the full index.

---

## Prebuilt libimbhgo.a distribution (2026-07-25)

Added a distribution path so consumers can build the binding **without a Rust toolchain**: per-platform
prebuilt `libimbhgo.a` archives on GitHub Releases plus a Go fetch tool.

**Why the previous model blocked consumers.** The cgo link line lived in `db.go` as
`-L${SRCDIR}/rust/target/release -limbhgo`. For a downstream `go get` user `${SRCDIR}` is the
read-only module cache with no archive, so the package could not link without cloning and building
the Rust side. The sanctioned override (per sable's link_extern.go) is `CGO_LDFLAGS="-L<dir>
-limbhgo"`; the header ships with the source so `-I${SRCDIR}` keeps working.

**What landed.**
- `link_linux.go` / `link_darwin.go` / `link_windows.go`: GOOS-gated `#cgo LDFLAGS`, moved out of
  `db.go` (which keeps only `CFLAGS: -I${SRCDIR}` + the Arrow-struct preamble). The `-L${SRCDIR}/
  rust/target/release` search dir stays as the local-dev default; for a consumer that dir is absent
  in the module cache, so the linker resolves `-limbhgo` from the `CGO_LDFLAGS` dir instead. System
  libs are now per-OS (Linux keeps `-lpthread -lm -ldl`; darwin/windows differ).
- `internal/release`: cgo-free single source of truth for `Version`, the asset naming/URL scheme,
  and glibc-vs-musl detection. `version.go` re-exports `imbhgo.Version` from it. Keeping this cgo-free
  is load-bearing: the fetch tool must build before the archive it fetches exists.
- `cmd/imbhgo-fetch`: pure-Go tool — resolves the cell (GOOS/GOARCH + libc), downloads the
  `.a.zst` asset, verifies SHA-256 against the release `SHA256SUMS`, zstd-decompresses into a cached
  `libimbhgo.a`, and prints `CGO_LDFLAGS`. Idempotent (checksum sidecar), `-force`, `-print-env`,
  `-base-url` (mirror/test) overrides. httptest-based unit tests, no network.
- `scripts/build-release.sh` + `.github/workflows/release.yml`: cross-build the 7-cell matrix
  (Linux amd64/arm64 glibc+musl via zig cc, macOS amd64/arm64 native, Windows amd64 best-effort),
  strip, `zstd -19`, publish assets + `SHA256SUMS`, then a **no-Rust smoke job** that fetches and
  builds a consumer with only Go installed.

**Verified locally (arm64).** Synthesized an asset from the existing archive, served it over HTTP,
ran the real fetch tool (download → checksum → decompress → correct `CGO_LDFLAGS`), then built and ran
`examples/quickstart` from a source copy with **no `rust/target`** — linking solely via the fetched
cache archive. It executed real IMBH queries, proving the end-to-end no-Rust path. `zstd -3` already
shrinks the archive 491 MB → 81 MB (release uses `-19`).

**Constraints to remember.** The whole assembly is pinned to Go `1.26.4` (`//go:linkname`); `go.mod`
enforces it, so archives key to the module release, not a separate Go-version axis. Prebuilt matrix is
arch x OS x libc. `Version` in `internal/release/release.go` must match the release tag (CI checks
this). darwin/windows system-lib sets in the `link_*.go` files are first-pass and to be confirmed on
real runners; sable's link_extern.go emits Linux-only libs unconditionally, so non-Linux cells may
need an upstream sable GOOS-gate + re-pin.

**Findings & verification details (2026-07-25, follow-on to the entry above).**

- *The read-only module cache is the crux, not the archive format.* A prebuilt `.a` is trivial to
  produce; the real blocker was that `-L${SRCDIR}/...` resolves to the immutable module cache for a
  `go get` consumer. The fix is not to write into `${SRCDIR}` but to let `CGO_LDFLAGS` (searched by
  the linker regardless of order) point at a user-writable cache dir — a nonexistent `${SRCDIR}` `-L`
  dir is silently ignored, so the same link line works for both local dev and prebuilt consumers.
- *Chicken-and-egg avoided by an internal cgo-free package.* `cmd/imbhgo-fetch` must build with only
  Go and no archive. Putting `Version` + asset naming in `internal/release` (no `import "C"`) and
  re-exporting `imbhgo.Version` from it keeps the fetcher independent of the very library it fetches.
  Importing the top-level `imbhgo` package from the tool would have reintroduced the cgo/archive dep.
- *`strip` barely shrinks a static archive; compression is the real lever.* `strip -S` on the 468 MiB
  combined `.a` did not reduce size (static archives are relocatable-object bundles). `zstd -3` alone
  took it 491 MB → 81 MB in <1 s; `-19` (release) is smaller still. So the distribution win comes from
  compression + GitHub Releases, which is also why `go:embed` is unusable here (too big for the module).
- *Decisive no-Rust proof method.* rsync the repo minus `rust/target` into a copy, then build+run
  `examples/quickstart` from it with the archive supplied only via `CGO_LDFLAGS` pointing at the
  fetch-tool cache. Because the copy has no `rust/target/release/libimbhgo.a`, a successful link+run
  can *only* have resolved `-limbhgo` from the fetched archive. It ran real SQL/LogQL/metric queries.
- *Build-graph facts.* `go mod tidy` promoted `github.com/klauspost/compress` from indirect to a
  direct require (used by the fetcher for zstd); `go.sum` needed no change (already transitively
  present). `proto-cdata/` is a *separate module* (own `go.mod`, own `libprotobatch.a`) so it is
  excluded from `./...` and untouched by the link-file refactor. Gate green: gofmt/build/vet clean,
  `go test -tags sable_extern_lib -race ./...` passes incl. the leak/UAF gates after the link change.
- *CI guardrail.* `release.yml` fails fast if the pushed tag != `internal/release.Version`, preventing
  an asset set whose names disagree with the version compiled into the binding.

**CD fix: pin Zig version in release.yml (2026-07-25).**

- *Symptom.* The Release workflow (run 30112128162) failed at the `Install zig (cross C compiler)`
  step with `Unexpected HTTP response: 404`; the post-step then reported `Unable to locate executable
  file: zig`. No Go/Rust build ever ran — the failure is upstream of compilation.
- *Root cause — not a toolchain mismatch.* The Go pin is correct and consistent (`go.mod` `toolchain
  go1.26.4` == `release.yml` `GO_VERSION: 1.26.4`). The break was `mlugg/setup-zig` with no `version:`
  input: an unpinned resolve tracks the *latest* Zig, and Zig's download index prunes/moves older
  tarballs, so the download starts 404-ing with no change on our side. It's a dependency/infra break,
  not a code or toolchain problem.
- *Fix.* Added `with: version: 0.14.1` to the `Install zig` step in `.github/workflows/release.yml`
  (action SHA pin unchanged: `mlugg/setup-zig@53fc45b # v1.2.2`). Pinning makes the cross-C compiler
  (`zig cc` for the Linux glibc/musl + Windows-gnu cells) reproducible.
- *Scope / verification.* Workflow-only change; the local cargo/go gate does not cover
  `.github/workflows/`, so it is exercised only when Release runs on a `v*` tag push. Not yet
  validated on a real runner — re-run the failed workflow or re-tag to confirm.
- *Follow-ups if it recurs.* `mlugg/setup-zig` also accepts a `mirror:` input if the default mirror
  is the one 404-ing; a newer setup-zig release hardened mirror-fallback (re-pin the SHA if bumped).
  If `0.14.1` proves wrong for any target's `zig cc` behavior, swap to the validated version.

**CD fix round 2: setup-zig v2, retired macos-13 runner, macOS-hostile build script (2026-07-25).**

- *Symptom.* Release run 30117003086 (tag `v0.1.0`) failed in every build cell. Three independent
  causes, not one — the previous entry's Zig version pin was necessary but not sufficient.
- *Cause 1 — setup-zig v1.x cannot download Zig 0.14.1 at all.* All 5 zig-using cells (4 Linux + the
  best-effort Windows cell) died at `Install zig` with every mirror returning 404, then
  `Unable to locate executable file: zig`. v1.2.2 unconditionally builds the tarball name in the
  pre-rename `zig-<os>-<arch>-<ver>` order, but Zig flipped it to `zig-<arch>-<os>-<ver>` starting
  with **0.14.1** — the exact version the last fix pinned. Verified directly: the official
  `.../download/0.14.1/zig-x86_64-linux-0.14.1.tar.xz` returns 200, the `zig-linux-x86_64-…` name
  v1.2.2 requests returns 404. v1 also probed `ziglang.org/builds` (nightlies) as its official
  fallback rather than `ziglang.org/download/<ver>`, so the last-ditch attempt 404s too.
- *Fix 1.* Re-pinned to `mlugg/setup-zig@d1434d08867e3ee9daa34448df10607b98908d29 # v2.2.1`, whose
  `getTarballName()` applies the rename cutoff (`< 0.14.1` -> old order) and whose fallback is
  `download/<ver>`; it also refreshes the mirror list from `download/community-mirrors.txt` at run
  time. Inputs are source-compatible, so `version: 0.14.1` carries over unchanged.
- *Not the cause: the Codeberg move.* Development moved to `codeberg.org/mlugg/setup-zig`, but
  `github.com/mlugg/setup-zig` is a live read-only mirror carrying all tags through v2.2.1, and
  GitHub Actions cannot reference actions hosted off GitHub — so the `mlugg/setup-zig@<sha>` form
  stays correct. The run log confirms the action itself downloaded fine; only Zig's tarball 404'd.
- *Cause 2 — `macos-13` is retired.* The darwin/amd64 job was never assigned a runner (no
  `runner_name`, no logs, queued 4h13m until the run was cancelled). GitHub retired the macOS 13
  image on 2025-12-04. Fixed by moving that cell to `macos-15-intel`, the last x86_64 macOS label
  Actions will offer (available to ~2027-08).
- *Cause 3 — `scripts/build-release.sh` aborted on macOS.* darwin/arm64 failed in 20s with
  `line 54: cargo_env[@]: unbound variable`. macOS ships bash 3.2, where an empty array expands as
  *unset* under `set -u`; only the darwin cells hit it because they alone pass an empty `CROSS_CC`,
  leaving `cargo_env` empty. Fixed with the `${cargo_env[@]+"${cargo_env[@]}"}` guard. A second
  latent macOS break was fixed pre-emptively in the same pass: line 75 called `sha256sum`, which is
  coreutils-only and absent from the macOS runner image, so both darwin cells would have failed at
  the checksum step even after the array fix — now dispatched through a `sha256()` helper that falls
  back to `shasum -a 256` (identical output format).
- *Verification.* `bash -n` clean; the empty-array guard exercised standalone under `set -u`; the
  no-`CROSS_CC` path re-run locally and confirmed to reach `cargo build` (it now fails only on the
  deliberately bogus target triple, not at line 54); workflow YAML re-parsed and the matrix/step pins
  re-read from it. Runner-side behaviour (`macos-15-intel` availability, the v2.2.1 Zig download) is
  still only validated against upstream URLs and GitHub's tag/API data — a real Release run is needed
  to confirm. Re-running requires deleting and re-pushing `v0.1.0`, since the trigger is a tag push.
- *Follow-ups.* (a) The whole release path is still **unvalidated on a real runner** — three separate
  breaks each masked the next, and nothing downstream of `Install zig` (cargo cross-build, strip,
  zstd, upload, `publish`, the no-Rust `smoke` job) has ever executed. Expect further first-run
  failures past this point, particularly the darwin cells and the `link_darwin.go`/`link_windows.go`
  system-lib sets already flagged in `TODO.md`. (b) Zig's tarball-naming cutoff is a *version*-keyed
  rule, so the `version: 0.14.1` pin and the setup-zig major are coupled — re-check both together if
  either moves. (c) `release.yml` still reads `github.event.inputs.version` under an
  `event_name == 'workflow_dispatch'` branch, but the workflow declares only a `push` tag trigger, so
  that branch is dead; adding `workflow_dispatch` would make re-validation far cheaper than the
  delete-and-re-push-the-tag cycle currently required. (d) Both `actions/checkout` and setup-zig now
  emit Node-20 deprecation warnings on the runner; harmless today, a forced-migration risk later.

**CD fix round 3: zig-as-cargo-CC wiring, and sable did not compile on macOS (2026-07-25).**

- *Where round 2 got to.* Release run 30145742724 confirmed all three round-2 fixes: `Install zig`
  went green on all five zig cells, `macos-15-intel` got a runner, and both darwin cells ran the
  real build for the first time. Every cell then failed further downstream — exactly the "nothing
  past `Install zig` has ever executed" risk that entry called out.
- *Cause 4 — `CROSS_CC` is a command line, but cargo/cc-rs want an executable path.* All five
  zig cells died in `Build, strip, compress`. Two distinct symptoms, one root cause:
  `CARGO_TARGET_<TRIPLE>_LINKER=${CROSS_CC%% *}` truncated to bare `zig`, so rustc invoked
  `zig -m64 …` → `error: unknown command: -m64`; and `CC_<target>="zig cc -target …"` made cc-rs
  keep only the first word, invoking `zig -E …` and breaking `zstd-sys` in every cell.
- *Fix 4.* `scripts/build-release.sh` now generates executable **shims** (`mkshim`) and points
  `CC_`/`CXX_`/`AR_` at them. Two details, both found by running it rather than reasoning about it:
  (a) cc-rs *appends its own* `--target=<rust triple>`, and zig triples are arch-os-abi vs Rust's
  arch-vendor-os-abi, so zig rejects it (`unable to parse target query
  'x86_64-unknown-linux-musl': UnknownOperatingSystem`) — the shim strips that flag, since the
  `-target` baked in from `CROSS_CC` is authoritative; (b) the linker override was **deleted
  outright**. `rust/` is `crate-type = ["staticlib"]` with no bins, so cargo never links a target
  artifact (rustc archives the `.a` itself) — the override bought nothing, and on the cell where
  target == host it hijacked *build-script* linking, which is what produced `-m64`. The global
  `export CC` went too: it aimed the cross compiler at host build scripts.
- *Cause 5 — sable does not build for Apple targets.* Both darwin cells failed with 6 × `E0425`
  (`eventfd`, `EFD_NONBLOCK`, `EFD_CLOEXEC` not in `libc`). sable's README claimed the completion
  doorbell was "the one genuinely OS-specific piece" and that macOS needed "no further Rust/Go
  changes beyond certification". Both claims were wrong: `doorbell.rs` *is* abstracted, but
  `goexec.rs` and `lib.rs` each called `libc::eventfd` unconditionally. sable's
  `SABLE_PIPE_DOORBELL=1` mode only exercised the completion doorbell, so Linux never caught it.
- *Fix 5 (upstream, sable `62f3f34`, branch `fix/macos-eventfd-port`).* `goexec.rs` now uses
  `doorbell::Doorbell` for its per-worker doorbells — no Go change needed, because `goexecWorker`
  already reads ≤8 bytes then drains, the contract `doorbell.rs` documents. `rust_awaits_go` stays
  Linux-only *by design*: its eventfd is a **value channel** (Go writes a `u64` Rust reads) shared
  under a single fd, so a pipe/socketpair port means widening `sable_go_compute` to two fds and
  reworking a close protocol the code flags as fd-reuse-race-sensitive — real design work, for
  demo surface no embedder uses. Its two entry points stay **present on every target** because
  `bridge_fast.go` calls them unconditionally (gating the symbols out would break the Go link off
  Linux); off Linux they fall back to a plain compute and still complete their token. sable's
  README/JOURNAL were corrected to match.
- *The verification lesson.* After two rounds of fixing blind, this round was verified locally
  first. Three techniques worth reusing: (1) `cargo check --target {aarch64,x86_64}-apple-darwin`
  needs **no macOS SDK** (check never links), so Apple-target breakage is catchable from Linux —
  it reproduced the exact 6 CI errors in seconds; (2) `zig cc -target aarch64-macos` bundles macOS
  libc headers, and a staticlib needs no linker, so the **whole darwin cell** was reproduced on
  this Linux box — a 330 MB `libimbhgo.a` of 1878 `Mach-O 64-bit arm64` members carrying
  `imbhgo_open` / `sable_goexec_worker_efd` / `sable_spawn_rust_awaits_go`; (3) the full release
  script was run for `x86_64-unknown-linux-musl` (cross) and `aarch64-unknown-linux-gnu`
  (target == host, the `-m64` pathology), both producing real artifacts.
- *Gate.* imbh-go re-pinned to sable `62f3f34` / Go `v0.0.0-20260725060011-62f3f347a505`; cargo
  build + clippy clean, `gofmt`/`go build`/`go vet` clean, `go test -tags sable_extern_lib -race
  ./...` green incl. the leak/UAF gates. Upstream sable: `make test`, `test-safe`, `test-pipe` all
  green under `-race`, and `TestGoExec` passes 3/3 under `SABLE_PIPE_DOORBELL=1` — the macOS
  primitive — which is the direct evidence for the `goexec.rs` swap. Pre-existing and untouched in
  sable: 32 `not_unsafe_ptr_arg_deref` clippy errors (identical count at the parent commit) and
  `cargo fmt` drift in files this change never touched.
- *Still unverified.* No release run has yet gone past `Build, strip, compress` on a real runner:
  `upload-artifact`, `publish`, and the no-Rust `smoke` job remain unexercised, as do the darwin
  cells' **native** clang path (local proof used `zig cc`) and sable on real macOS hardware. The
  `link_darwin.go` / `link_windows.go` system-lib sets in `TODO.md` are the likeliest next snag,
  and they surface in `smoke`, not in `build`.
- *Addendum — sable PR #2, and a stale doc claim corrected.* Opened
  https://github.com/moriyoshi/sable/pull/2 for the macOS port (1 commit, 4 files, `MERGEABLE`). Two
  findings while doing so. (1) **The "545d04f is not on `main`" note was stale**: sable PR #1 merged
  on 2026-07-24 as `bff774f`, so that commit had been an ancestor of `main` all along and the
  matching TODO was already satisfied upstream — the claim had simply been copied forward across
  `TODO.md` / `PLAN.md` / `ARCHITECTURE.md` / LTM without re-checking. `gh api
  repos/<repo>/compare/main...<rev>` settles it in one call. (2) **`required_signatures` is checked
  against the incoming commit, not the merge commit.** PR #2 reports `mergeable_state=blocked`
  because `62f3f34` is unsigned, whereas PR #1 merged because its head `545d04f` was itself signed
  (`verified=true`). GitHub *does* sign merge commits it creates (`bff774f` is `verified=true,
  signer=GitHub`), but that alone does not satisfy the rule — an initial reading that assumed it did
  was wrong. Unblock by signing + force-pushing (interactive; the SSH key is passphrase-protected and
  no agent runs here) or by squash-merging so GitHub authors the landing commit. Both change the SHA,
  so imbh-go must be re-pinned after the merge either way.
- *Addendum 2 — sable PR #2 merged; re-pinned onto `main` (2026-07-26).* Upstream landed the macOS port,
  so the pin moved from the fix-branch commit to **`main` at `30b2c30`** /
  `v0.0.0-20260726022811-30b2c30322c5`. Three things worth recording. (1) **The pinned SHA did not
  survive the merge.** PR #2 was signed and *extended* upstream before landing — the merged head was
  `2212f2f`, "Port the Go build and test suite to run on macOS", which builds on our change — so the
  `62f3f34` this repo pinned ended up an orphan reachable from no branch. `compare/main...62f3f34`
  reported `ahead=1`, which is the tell; the *content* was on `main` (verified by reading `goexec.rs`
  and `lib.rs` at `ref=main`), only the commit identity differed. General rule: never treat a branch
  head you pushed as a durable pin — re-pin to the post-merge `main` SHA and re-run the gate.
  (2) **`main` had moved 12 commits ahead**, adding upstream CI/emulation harnesses, native Windows CI,
  and a Windows portable port — so this was not a no-op re-pin and the whole gate was re-run: cargo
  build + clippy clean, `gofmt`/`go build`/`go vet` clean, `go test -tags sable_extern_lib -race`
  green incl. the leak/UAF gates, plus a full `aarch64-apple-darwin` staticlib cross-built again with
  `zig cc` (Mach-O arm64 confirmed). (3) **Upstream GOOS-gated `link_extern.go`** — it now emits
  `#cgo linux`/`darwin`/`windows` sets instead of the Linux libs unconditionally, which closes the
  sable half of the darwin/windows link-libs TODO. Our own `link_darwin.go`/`link_windows.go` are
  still unverified first-pass guesses, and they must *union* correctly with sable's (sable adds
  `-liconv` on darwin and `-ldbghelp` on windows that ours do not list). Those surface in `smoke`.
- *Doc-hygiene note.* Two TODO items turned out to be **already satisfied upstream but never
  re-checked**, and the stale wording had been copied forward across `TODO.md` / `PLAN.md` /
  `ARCHITECTURE.md` / `OVERVIEW.md` / LTM — including, briefly, into this session's own edits before
  it was caught. `545d04f` had been on `main` since PR #1 merged on 2026-07-24. One `gh api
  repos/<repo>/compare/main...<rev>` call settles such a claim; prefer that over trusting the prose.

**CD round 4: the release actually shipped (2026-07-26).**

- *Outcome.* Run 30185313898 concluded **`success`**. All six real cells built, `publish` attached six
  `.a.zst` assets + per-asset `.sha256` + a combined `SHA256SUMS` to `v0.1.0`, and both `smoke` jobs
  passed. That closes the standing "nothing downstream of `Install zig` has ever executed" gap: the
  cargo build, strip, zstd, upload, publish, and the no-Rust consumer path are all now exercised.
- *The macOS port holds on real hardware.* Both darwin cells build with the runners' **native clang**,
  not just the local `zig cc` reproduction — so the sable eventfd port (upstream PR #2) is confirmed
  end-to-end, not merely "compiles under a cross toolchain".
- *`continue-on-error` semantics, settled empirically.* The docs pages for `jobs.<id>.continue-on-error`
  and `needs` do not state how they interact, so this run is the evidence: `windows/amd64` failed, yet
  `publish` (which declares `needs: build`) **ran**, and the overall run conclusion was `success`. A
  failed best-effort matrix leg neither fails the run nor blocks dependents.
- *Windows cannot pass as configured — not a regression.* It dies compiling sable with **default
  features**: `std::os::unix::io::RawFd` in `reactor.rs`/`lib.rs`, plus `libc::read`'s size arg being
  `u32` on Windows vs `usize`. Upstream is explicit that Windows runs the **portable fallback** only
  (`--no-default-features` + Go `-tags sable_portable`), with the fast path pending an IOCP doorbell.
  Wiring that up here is a feature (a cargo feature to drop sable's defaults for that cell, plus a Go
  build combining `sable_portable` with `sable_extern_lib`), not a CI fix. Decision deferred to TODO.
- *The `smoke` failure was a Go-checksum-DB race, not our code.* Both smoke jobs failed with
  `sum.golang.org/lookup/...@v0.1.0: 500 Internal Server Error`. `sum.golang.org` only learns of a
  version when someone asks for it; the first lookup makes it fetch from `proxy.golang.org`, and until
  that lands it answers 500. The timing is unambiguous: the proxy recorded `v0.1.0` at **02:54:25Z**
  and the job asked at **~02:55:5xZ**. Confirmed two ways — the full consumer path reproduced locally
  (`go get` → `imbhgo-fetch` → build with `-tags sable_extern_lib` → ran, no Rust toolchain), and a
  plain re-run of the unchanged jobs went green. **Every first release of a new tag will race this**,
  so `release.yml` now retries both module fetches with backoff. Deliberately *not* `GONOSUMDB` /
  `-insecure`: that would "fix" it by skipping the very verification this job exists to prove.
- *Self-caught bug in that fix.* The first draft wrote `retry go run ... -print-env >fetch-env.sh`.
  The redirection binds to the `retry` call, so the file is opened (and truncated) once and every
  attempt appends — a partial failed attempt would concatenate with the successful one into an invalid
  env script that `eval` would then run. Redirection now lives inside the retried function, so each
  attempt truncates.
- *Still open.* No **test** CI exists — `go test -race` and the Rust suite have still only ever run on
  the local arm64 box; `release.yml` builds and publishes but never tests. And `link_darwin.go` /
  `link_windows.go` remain unverified for their own platforms: `smoke` only covers linux/{amd64,arm64},
  so the darwin link sets were never exercised by a real link even though the darwin *archives* ship.

**CD round 5: Windows unblocked upstream; re-pinned to sable `0c6fe56` (2026-07-26).**

- *What landed upstream.* sable merged the Windows fast crossing (its PR #7) and **IOCP fd-fusion**
  (#10, "Go owns the overlapped read, Rust awaits the count"), plus a `win_reactor` module gated
  `#[cfg(all(feature = "fast", windows))]` mirroring the Unix `reactor`. The previously-fatal
  `use std::os::unix::io::RawFd` lines in `reactor.rs`/`lib.rs` are now behind `cfg(unix)`. Upstream CI
  builds the **fast** staticlib for `x86_64-pc-windows-gnu` natively — the exact triple this cell uses.
- *Re-pinned* to `main` at `0c6fe56` / `v0.0.0-20260726045720-0c6fe56eb099`. Full Linux gate green
  (cargo build + clippy, gofmt/build/vet, `go test -race` incl. the leak/UAF gates).
- *The windows cell now builds.* Reproduced locally through the real release script: a 340 MB
  `libimbhgo.a` of **2732 `Intel amd64 COFF` members** exporting `imbhgo_open`, `imbhgo_shell_free`,
  `sable_goexec_init`, `sable_call_result`, then stripped/compressed/checksummed to a 46 MB asset.
- *Two local-only tooling notes.* (1) The first attempt died on `error calling dlltool
  'x86_64-w64-mingw32-dlltool': No such file or directory` — a **host** gap, not a code problem: rustc
  needs dlltool for windows-gnu import libraries. CI already installs `binutils-mingw-w64-x86-64` (for
  `strip`), which provides it; locally a `printf '#!/bin/sh\nexec zig dlltool "$@"\n'` shim sufficed,
  since zig ships an llvm-dlltool drop-in. Deliberately **not** added to the shipped script — CI has the
  real binutils. (2) Re-pinning hit the *same* `sum.golang.org` 500 diagnosed in round 4, on a
  minutes-old commit — independent confirmation that the retry added to `release.yml` addresses a real,
  recurring race rather than a one-off.
- *Kept `best_effort: true`, and why.* The flag gates only **failure**; a green build still uploads and
  publishes, so keeping it costs nothing but regression detection. What is still unverified is *this
  binding* on Windows, not sable: nothing link-tests or runs the archive, because `smoke` covers only
  linux/{amd64,arm64}, and `link_windows.go`'s set is a first-pass guess that must union with sable's
  (sable contributes `-lkernel32` and `-ldbghelp` that ours omits). The workflow comment was rewritten
  to state that current reason instead of the stale "unverified sable support". Promote the cell to
  blocking once a windows smoke job exists.

## 2026-07-28 — `imbhgo-fetch -print-env` picked its shell from the wrong axis (downstream windows CI break)

A consumer (`moriyoshi/cornus`, [run 30339923183](https://github.com/moriyoshi/cornus/actions/runs/30339923183/job/90214109442))
failed its `windows/amd64` release leg. The interesting part is *where*: not at the link step everyone
expected (see the standing windows TODO), but three lines earlier, in our own fetch tool.

- *The failure.* The consumer does exactly what our README documents —
  `eval "$(go run …/cmd/imbhgo-fetch@v0.1.0 -print-env)"` — then asserts `CGO_LDFLAGS` is non-empty.
  The download itself was **entirely successful** (`installed C:\Users\runneradmin\AppData\Local\imbhgo\v0.1.0\windows-amd64\libimbhgo.a`),
  and the very next line was `ERROR: imbhgo-fetch did not report CGO_LDFLAGS`.
- *Root cause.* `emit` branched on `o.goos == "windows"` and printed cmd.exe syntax, `set CGO_LDFLAGS=…`.
  The consumer's shell is git-bash (what the GitHub Actions `bash` shell is on a windows runner), where
  `set VAR=…` is the POSIX builtin that assigns **positional parameters** — it succeeds, exports nothing,
  and returns 0. A silent no-op, which is why the failure surfaced as an empty variable rather than an
  eval error.
- *The category of bug.* The dialect was derived from the wrong axis. **The target GOOS says nothing
  about the shell the tool was invoked from** — they are independent, and doubly so when cross-compiling
  (`-os windows` from Linux hit the same branch). The only thing that knows the dialect is the caller,
  so it became a flag: `-shell sh|cmd|powershell`, POSIX default, validated in `run` before any download.
- *Quoting hardened while there.* `%q` (Go quoting, inside double quotes) happened to survive bash for
  `C:\…` paths, but not a `$`. Now single-quote escaping per dialect (`'\''` for POSIX, doubling for
  PowerShell). Separately, `cmd/go`'s `CGO_LDFLAGS` splitter only honours a quote that **opens a whole
  field**, so a cache dir containing a space needs `'-LC:\Program Files\…'`, not `-L'C:\Program Files\…'`
  — routine under a Windows user profile, and previously broken. `ldflagsFor` handles it.
- *Regression gates.* `TestFetchPrintEnvIsPOSIXForWindowsTarget` (target windows must still print
  `export`), `TestEnvLineDialects`, `TestEnvLineQuotesEmbeddedQuote`, `TestLdflagsForQuotesWholeSearchPath`,
  `TestRunRejectsUnknownShell`. Full Go gate green under `-race`; no Rust change.
- *The real lesson, and the cost.* This is the second-order cost of `best_effort: true` on a cell with no
  smoke job: we publish a windows archive that no CI ever *consumes*, so the first consumer is the test.
  A windows smoke job running the documented `eval` line under the Actions `bash` shell would have caught
  this in one step, and would still catch the link-time failure the TODO predicts. Note also that the fix
  reaches nobody until a **`v0.1.1`** tag exists — consumers pin `imbhgo-fetch@v0.1.0`, and a module-proxy
  pin is immune to fixes on `main`.

## 2026-07-28 — CI at last: `ci.yml` runs the standard gate on both Linux arches (+ an Apple compile guard)

Until today the repo had CD but no CI: `release.yml` builds six cells and publishes them on a tag, but it
never runs a test, so `go test -race ./...` and clippy had only ever run on one arm64 developer box. Added
`.github/workflows/ci.yml` (push to `main`, every PR, `workflow_dispatch`; `concurrency` cancels superseded
PR runs but never a `main` run).

- *Shape.* Three jobs. `lint` needs neither Rust nor the 450 MB archive — `gofmt -l`, `go vet` + `go test
  -race` over the deliberately cgo-free `./cmd/...` and `./internal/...`, and a `CGO_ENABLED=0`
  cross-compile of `imbhgo-fetch` for all five published cells — so a formatting or pure-Go break reports
  in about a minute instead of after a DataFusion build. `gate` is the real thing on `linux/amd64`
  (`ubuntu-latest`) and `linux/arm64` (`ubuntu-24.04-arm`): `cargo build --release` (host build, no
  `--target`, so the archive lands where `link_linux.go`'s `-L${SRCDIR}/rust/target/release` already looks
  and cgo needs no `CGO_LDFLAGS`) → `go build` → `go vet` → `go test -race` → `go run ./examples/quickstart`
  → `cargo clippy -- -D warnings`. `apple-check` is compile-only for the two Apple triples.
- *`if: ${{ !cancelled() }}` from `go vet` onward.* One failing check must not hide the others; a single run
  should report vet + tests + clippy together rather than one per push. Clippy is deliberately **last**: it
  reaches the workspace crate through `RUSTC_WORKSPACE_WRAPPER`, so only `rust/src/lib.rs` recompiles once
  the release build above is warm, and its `-D warnings` failure never costs us the test signal.
- *No `cargo test`, deliberately.* `rust/` carries no `#[test]` and its only target is a `staticlib` whose
  sable half resolves Go runtime symbols; a test harness would have nothing to assert and nothing to link
  against. The Rust code is covered through the Go suite.
- *No `cargo fmt --check`, and this is a real finding.* `rust/src/lib.rs` is **not** rustfmt-clean at the
  default `max_width = 100` — it is written at roughly 110 and rustfmt wants to break `split_json_req`,
  the `sable::register(OP_*, …)` block, the `let`-chains, and more. Adding the check would have made CI red
  on day one. Landing it later means either accepting a large reformat diff or committing a `rustfmt.toml`
  with the wider width; either is a deliberate decision, not a drive-by.
- *The Apple guard runs on macOS, and the old TODO's premise was wrong.* The standing item claimed `cargo
  check --target {aarch64,x86_64}-apple-darwin` "needs no macOS SDK (check never links), so a plain Linux
  job works". Check never links, but it **does run build scripts** — and those compile C. Measured on this
  Linux box: `cargo check --release --target x86_64-apple-darwin` dies in cc-rs building `zstd-sys` with
  `cc: error: unrecognized command-line option '-arch'` (cc-rs hands the host gcc `-arch x86_64
  -mmacosx-version-min=10.7`). Making it work on Linux would need the same zig-cc shims
  `scripts/build-release.sh` carries. A `macos-14` runner compiles both triples natively with Xcode's clang
  and no cross setup — and the repo is public, so the runner is free. Note the aarch64 case would have
  hidden this: `rust/target/aarch64-apple-darwin/` already existed locally with cached build-script output,
  so only the untouched x86_64 triple exposed the failure. Pick the cold cell when validating a cross build.
- *Caching.* `Swatinem/rust-cache` with `workspaces: rust` and `save-if: github.ref == 'refs/heads/main'`,
  so PR branches restore main's warm entry but can never evict it (repo-wide cache is 10 GB and this tree
  is large: the host `rust/target/release` here is 6.8 GB, each `--target` cell about 1.6 GB).
- *Local dry-run before landing.* Every step was executed on this box first: `gofmt -l .` clean, `go vet`
  clean, the cgo-free tests pass, all five cross-compiles of `imbhgo-fetch` build, `go test -race -count=1
  ./...` green in 8.6 s wall (the Rust build, not the suite, is what makes CI slow), the quickstart prints
  its three sections, and `cargo clippy --release -- -D warnings` is clean.
- *Still open.* The first real-runner run is the validation bar (same one `release.yml` had to clear) —
  watch runner disk and cold-build wall clock (`timeout-minutes: 120`). And `windows/amd64` is still
  build-only and best-effort: the smoke job the previous entry argued for is not part of this change.

## 2026-07-28 — a native `windows/amd64` gate (the cell we published but never linked)

Follow-up to the CI entry above. `windows/amd64` was the one published cell with no verification of any
kind: `release.yml` cross-builds it with zig, `best_effort: true` gates only *failure*, so a green build
uploads and publishes an archive that nothing has ever linked or run. Added `gate-windows` to `ci.yml`.

- *Recipe, borrowed rather than invented.* sable's own `verify-windows` job certifies this exact triple
  natively, so this follows it: `windows-latest`, `defaults.run.shell: bash` (Actions' bash there is
  git-bash, which is where `pwd -W` comes from), `cargo build --release --target x86_64-pc-windows-gnu`,
  then `CGO_LDFLAGS="-L$(pwd -W)/rust/target/x86_64-pc-windows-gnu/release"`. The **gnu** ABI is not a
  preference: Go's cgo on Windows links with mingw gcc, not MSVC. Only `-L` is passed, so `-limbhgo` and
  the Win32 libs still come from `link_windows.go` — the job exercises those directives instead of
  bypassing them, and it is the same shape `imbhgo-fetch -print-env` emits to a consumer.
- *The "lib set must union with sable's" worry was already answered.* The standing TODO said
  `link_windows.go` omits `-lkernel32` and `-ldbghelp` that sable adds. Reading the pinned module shows
  sable's `link_extern.go` — the file active under `-tags sable_extern_lib` — carries
  `#cgo windows LDFLAGS: -lkernel32 -lntdll -luserenv -lws2_32 -ldbghelp` itself, so cgo already collects
  the union from both packages. Ours adds `-lbcrypt` and `-ladvapi32` on top. What was never proven is
  that a real mingw `ld` resolves the combined set against a 340 MB COFF archive; that is the actual gate.
- *One deliberate PATH step.* Rust's `x86_64-pc-windows-gnu` is the **MSVCRT** mingw flavour, i.e.
  `C:\msys64\mingw64` — the `ucrt64` tree is the incompatible one — and the runner image also has
  Strawberry Perl's gcc on PATH. The C dependencies (zstd-sys) and Go's cgo must use the *same* gcc, or
  the archive's libgcc/msvcrt references disagree with the linker's, which is precisely the failure class
  this job is for. So mingw64 is prepended and a `Toolchain report` step prints `gcc -dumpmachine`.
- *`-race` is `continue-on-error`, on purpose and temporarily.* sable's job notes the race detector is
  unavailable on the fused Windows path. Rather than assume that carries over, the step runs and reports.
  One CI run answers it; then it either becomes a hard step or is deleted with a note. Everywhere else
  `-race` stays mandatory — the two-free Arrow ownership gates are the reason.
- *Still not covered by this.* The gate builds from source, so it never fetches a published asset: the
  `-print-env` consumer flow that broke a downstream release stays untested until a windows `smoke` job
  exists in `release.yml`. And the release-matrix cell stays `best_effort: true` until this job is green.

## 2026-07-28 — the windows gate's first run: it links, and it found a real hole in our error reporting

`gate-windows` went red on its first run, and the shape of the failure is the interesting part.

- *What passed.* `cargo build --release --target x86_64-pc-windows-gnu` (native, ~32 min), **`go build`
  — the link gate** — and `go vet`. So the standing worry is settled: `link_windows.go`'s directives plus
  sable's `link_extern.go` Win32 set do resolve against the 340 MB COFF archive under a real mingw `ld`.
  The archive we have been publishing as `best_effort` links.
- *What failed.* Exactly four tests, all of them the **on-disk** open path: `TestOpenReadOnly`,
  `TestOpenWith`, `TestOpsPassthrough`, `TestDurabilityReopen`, each at 0.00s with
  `imbhgo: open database at C:\Users\RUNNER~1\...\001 failed`. Everything else — the whole in-memory
  suite, streaming, cancellation, backpressure, the leak/UAF gates — passed on Windows.
- *Correction to the `-race` assumption.* The `-race` step reported step-conclusion `success`, but that
  is only `continue-on-error` masking it: the log shows it ran and produced the *same* four failures,
  with no race report. So the detector **is** available on this binding's Windows path, contrary to the
  note in sable's `verify-windows` job that we inherited. Once the open path works, `-race` should become
  the hard step and the plain run should go.
- *The real finding: we could not diagnose it, and that is our bug.* All four `imbhgo_open*` entry points
  are direct C calls whose only return is a `u64` handle, and every failure arm was `Err(_) => 0`. The
  cause — wrong permissions, `writer.lock` held by another writer, an unsupported platform — was thrown
  away in Rust, and Go invented "open database at <path> failed". That is a defect on every platform, not
  just Windows; it merely took a platform we could not run locally to make it hurt.
- *Fix.* The open entry points now take an `err_id: u64`, a caller-allocated id drawn from the same
  counter as query ids, and record the failure under it via the existing `QUERY_ERRORS` slot map; Go
  fetches it through the `OP_QUERY_ERROR` byte-Call it already uses for terminal stream errors. No new
  transport, one new parameter per entry point (`imbhgo.h` updated to match). `openFailure` composes
  "imbhgo: <what>: <cause>" and falls back to the old generic form only if nothing was recorded.
  Sample output now: `open database at /tmp/x: open error: database lock held: /tmp/x/writer.lock`,
  `open read-only database at /tmp/x/nope: open error: ... does not exist`, `storage error: I/O error:
  Not a directory (os error 20)`. Regression gates: `TestOpenErrorReportsCause` (second `Open` of a live
  path must name a cause, not end in the generic " failed") and `TestOpenWithErrorReportsCause`.
- *While refactoring, one fidelity bug avoided.* Folding the fetch into a shared `takeStoredError`
  initially swallowed the transport error `fetchQueryError` used to return, which would have reported a
  failed byte-Call as a clean end-of-stream. It returns `(string, error)` for that reason.
- *Upstream context for the next step.* imbh's own CI is `ubuntu-latest` for every job, so its on-disk
  path has never run on Windows. The writer lock uses `fs4` (cross-platform), and there is no
  path→URL conversion in imbh to blame, so the cause is genuinely unknown until the next CI run prints
  it. That run decides whether this is ours to fix, an upstream issue to file, or a documented skip.

## 2026-07-28 — the error-slot map did leak, on abandoned streams (found by asking, not by a test)

Prompted by a review question about `takeStoredError`'s thread safety. It is safe — the map is
`Mutex`-guarded, the fetch is a `remove` (take-once), and every id comes from the single `queryCtr`,
which is *why* opens reuse the query counter rather than getting their own: a second counter would hand
out colliding ids into a shared map. But the follow-up question — "so then don't they leak?" — was the
right one, and the answer was yes.

- *The reachable leak.* `fetchQueryError` is called only from `finish`, and `finish` runs only when
  `Next` reaches end-of-stream (or an import error). A caller that abandons a stream — `Close` without
  draining, i.e. a cancelled request handler or an early return under `defer rows.Close()` — never runs
  `finish`. If the handler had already recorded a terminal error, that entry was held until the process
  exited. Plan errors make this trivially reachable: they are stored the moment the handler starts,
  before Go pulls anything.
- *Reproduced before fixing.* A probe that queries `SELECT * FROM no_such_table`, waits for
  `imbhgo_pending_query_errors()` to reach 1, then `Close()`s without draining: the count stayed at 1
  indefinitely. Now `TestAbandonedStreamClearsErrorSlot`, with `TestDrainedStreamKeepsItsError` guarding
  the other side (a drained stream's `Err()` must survive `Close`, and `Close` stays idempotent).
- *Fix.* `Rows.Close` clears the slot when the stream never ended, guarded by `r.ended` so the drained
  path — where `finish` sets `ended` *before* calling `Close` — does not pay a second byte-Call. The
  message is discarded rather than stored into `r.err`: `Err` is documented as meaningless before
  iteration ends, and the point here is slot hygiene, not reporting.
- *Residual window, left open deliberately and documented at the call site.* If the handler records an
  error *after* Close's fetch, nothing claims it. Rust already avoids this for the common case — a send
  to a dropped receiver returns without storing — so the window needs a scan/export error computed
  concurrently with the abandon. Closing it properly means skipping the store once `tx.is_closed()`, at
  roughly a dozen store sites; `TestNoLeak`'s `pendingQueryErrors() == 0` assertion is the tripwire that
  would justify that work.
- *Note on what the existing gate did and did not prove.* `TestNoLeak` has always asserted the pending
  count is zero, and it passed — because the streams it abandons do not also fail. A leak gate only
  covers the paths it walks.
- *Not affected: the new open path.* `openFailure` always fetches, so a failed open cannot strand a
  slot; the only loss there is a transport failure on the fetch itself.

## 2026-07-28 — what the windows gate found: an upstream Unix-only durability idiom (imbh#3)

With the open error finally crossing the FFI boundary, the second `gate-windows` run named the cause in
one line, on all five durable-DB tests:

```
imbhgo: open database at C:\Users\RUNNER~1\...\001: storage error: WAL dir fsync: Access is denied. (os error 5)
```

- *Root cause, upstream.* `imbh-storage`'s `fsync_dir` (`crates/imbh-storage/src/wal.rs:315`, identical in
  the published `0.1.0` and `main` @ `bd8e359`) does `File::open(dir)` + `sync_all()` — the Unix idiom for
  making a newly created file's *directory entry* durable. Windows refuses to open a directory as a file
  without `FILE_FLAG_BACKUP_SEMANTICS`, so it returns `ERROR_ACCESS_DENIED`. The first call site is fresh-DB
  first-segment creation (`wal.rs:357`), which is why *every* durable open fails and why it fails at 0.00s.
  The other is rotation (`wal.rs:421`).
- *Filed as [moriyoshi/imbh#3](https://github.com/moriyoshi/imbh/issues/3)* with the `#[cfg(windows)]`
  no-op fix and the SQLite/LMDB/RocksDB precedent (NTFS journals the metadata; a directory handle cannot
  be flushed with `FlushFileBuffers` even with the backup-semantics flag — flagged in the issue as the one
  claim not verified here, since it decides no-op vs. flag).
- *Why it survived to a release.* Every job in imbh's `ci.yml` is `ubuntu-latest`. A single
  `windows-latest` `cargo test` on `imbh-storage` would have caught it at first-segment creation. The
  triple itself builds fine — this is one Unix-shaped call, not a port.
- *What this says about the gate.* It paid for itself on its first two runs: it proved the link (the thing
  the standing TODO doubted) and found a genuine platform defect nobody knew about, in a cell we had been
  *publishing* as `best_effort` for two releases. Note the ordering that made it cheap: run 1 said
  "failed", run 2 named the cause — the error-surfacing fix in between is what turned an opaque red into a
  one-line diagnosis, which is the argument for never letting an FFI boundary swallow an error.
- *Also settled: `-race` works on Windows.* Its step-conclusion `success` was `continue-on-error` masking
  an identical failure with no race report. Once the suite passes there, `-race` becomes the hard step and
  the plain run goes — the note inherited from sable's `verify-windows` job does not apply to this binding.
- *Status.* `windows/amd64` is in-memory-only until an imbh release carries the fix. Open decision, for the
  user: skip the five durable-DB tests on Windows and merge the gate green now, or hold the PR until imbh
  ships and we re-pin. Everything else on Windows passes: streaming, cancellation, backpressure, the Arrow
  leak/UAF gates, the whole in-memory surface.

## 2026-07-28 — imbh 0.1.1 re-pinned: the Windows blocker is fixed upstream, `-race` promoted

`imbh#3` was fixed and published within hours of being filed, so no test skip was ever needed.

- *The upstream fix* (`ba448cd`, published as `imbh`/`imbh-core`/`imbh-lgtm`/`imbh-storage` `0.1.1`) is the
  `#[cfg(windows)]` no-op, and it landed at **two** sites, not one: `wal.rs`'s `fsync_dir` (which our
  report found) and a second in `imbh-storage/src/lib.rs`'s Parquet/manifest write path (which it missed —
  the report generalized from one stack). Upstream's comment is more careful than our issue text was: it
  spells out that `FlushFileBuffers` takes a *file* handle and the volume-wide flush needs administrator
  rights, so a library has no directory-fsync primitive at all on Windows; and it flags the manifest site
  as the more exposed of the two, since a durable manifest edit pointing at a lost rename is a dangling
  reference rather than merely lost data. That trade is now documented in imbh's ARCHITECTURE.md §7.
  Upstream also added a Windows CI job — the systemic gap the issue pointed at.
- *Re-pin mechanics, worth remembering.* The declared requirements are caret ranges (`"0.1.0"` = `^0.1.0`),
  so the actual pin lives in `Cargo.lock` and `cargo update -p imbh -p imbh-core -p imbh-lgtm` was the
  whole functional change; cargo's semver unification keeps the `imbh-core` instance shared within `0.1.x`
  automatically, so the lockstep worry in the Cargo.toml comment does not bite for a patch bump. The
  declared versions were bumped to `0.1.1` anyway, along with every doc that named `0.1.0` as the current
  pin (`AGENTS.md`/`CLAUDE.md` — a symlink to it, `ARCHITECTURE.md`, `OVERVIEW.md`, `PLAN.md`, `TODO.md`,
  `README.md`), because a stale version in a prescription doc is the kind of thing that gets copied.
- *`-race` promoted on the Windows leg.* It went in as `continue-on-error` on the inherited assumption
  (from sable's `verify-windows` note) that the detector is unavailable on the fused Windows path.
  Measurement beat the assumption: it ran and failed identically to the plain run, with no race report. So
  it is now the hard step and the plain `go test` it hedged against is gone.
- *Not promoted: the release-matrix cell.* `windows/amd64` stays `best_effort: true`. The standing
  condition for promotion was a windows `smoke` job, and this gate is not one — it builds from source and
  never fetches a published asset, so the `-print-env` consumer flow that broke a downstream release
  remains untested. Worth keeping those two distinct: "the archive links and the suite passes" is what the
  gate proves; "a consumer can fetch and use the published archive" is what smoke would.
