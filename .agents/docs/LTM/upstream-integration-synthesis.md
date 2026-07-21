# Consuming the Two Upstreams: build, deps, and the imbh/sable surfaces (synthesis)

## Summary

imbh-go is glue between two independently-moving upstreams — **imbh** (the observability DB) and **sable** (the tokio ⇄ Go FFI transport) — fused into one combined Rust staticlib and wrapped by a Go package. Working on it means holding three things at once: the build/link model and how the deps are sourced, imbh's API surface (which drifts and has sharp constraints), and sable's FFI contract (its API shapes, real cancellation, and a memory-safety bug that shaped the pin). This synthesis is the integrator's map; drill into the sources for exact signatures and histories.

## Included Documents

| Document | Focus |
|----------|-------|
| [build-toolchain-and-deps.md](./build-toolchain-and-deps.md) | Combined staticlib, toolchain pin, cgo constraints, dependency sourcing |
| [imbh-upstream-surface.md](./imbh-upstream-surface.md) | imbh API constraints, drift history, column types, admin/ops surface + op-id map |
| [sable-ffi-integration.md](./sable-ffi-integration.md) | sable API shapes, cancellation on both paths, the empty-result `0x1` bug |

## Stable Knowledge

### Build & link model

- **One combined staticlib.** `rust/` (crate `imbhgo`, `crate-type = staticlib`) fuses sable's runtime + imbh + the glue handlers + the C ABI → `libimbhgo.a` (~450 MB; cold build pulls the full DataFusion tree, minutes). Build it first after any Rust change.
- **`-tags sable_extern_lib`** makes sable's Go package contribute no `-lsable`; the linker resolves everything against `libimbhgo.a` (`CGO_LDFLAGS -limbhgo`).
- **Dependency `#[no_mangle]` symbols survive** the combined staticlib (`nm`-verified) — no `#[used]` shim needed.
- **Toolchain pinned to Go 1.26.4** (module `toolchain go1.26.4`) — the fused runtime reaches Go's internal ABI via `//go:linkname`, so it must match what sable certified.
- **cgo cannot live in a `_test.go`** — the bridge is in a normal `.go`, tests are pure Go (why `debug.go` holds the leak accessors). A build-tagged file (e.g. the example) must carry its `//go:build` constraint or a plain `go build ./...` tries to link it and fails.
- **The staticlib is a global mutable resource** — only one agent rebuilds `rust/` at a time; serialize rust-touching work, run the rust-owning unit (cargo rebuild + full gate) first, then pure-Go units against the fresh archive.

### Dependency sourcing (externally buildable, no path deps)

- **imbh from crates.io, lockstep at `0.1.0`:** `imbh`/`imbh-core`/`imbh-lgtm` all pinned together. **Load-bearing** — the direct `imbh-core` dep must be the SAME crate instance as imbh's transitive one, else `imbh::Attributes != imbh_core::Attributes` in the glue. `imbh-core` is a direct dep because the facade doesn't re-export `canonical_json_object`/`to_hex()`. Features: imbh `["cdata","proto","search","serde"]`, imbh-lgtm `["source"]` (`serde` pulls no new crate).
- **sable as a git dep** at the memory-safety-fix commit: Rust `sable = { git = "…/sable", rev = "545d04f" }`; Go a **direct `require …@v0.0.0-…-545d04fc08c3` with NO `replace`**.
- **Two sourcing findings:** (1) a cargo git dep finds a crate in a repo `rust/` subdir by scanning the tree — no root workspace manifest needed. (2) `replace` ≠ `require` — `replace` is ignored for downstream consumers, so a `replace`-to-git would leave importers with the unbuildable `v0.0.0`; a direct `require` at the pseudo-version (resolve via `GOPROXY=direct GOSUMDB=off go list -m <mod>@<sha>`) is what makes the binding importable.
- Local checkouts at `../imbh`/`../sable` remain for co-development — on landing an upstream change, re-pin (bump version or `rev` + pseudo-version), don't revert to a path dep.

### imbh API surface — constraints that shape the binding

- **`to_sql` (private) + `sql_with_params` (`pub(crate)`) are inaccessible externally** → typed/LGTM queries use the eager `*_batches`; **SQL is the only lazy streaming path.**
- **`_batches` signatures are NOT uniform** — `logs().query_batches` returns `Result<Vec<RecordBatch>>` (stats variant is `query_batches_with_stats`); `metrics().range_batches`/`traces().span_metrics_batches` still return the `(batches, QueryStats)` tuple. Check each.
- **imbh return structs derive only `Debug, Clone` — no serde.** Ops-passthrough must mirror each into a `*Wire` serde struct field-by-field.
- **Grep `pub (async )?fn`, not `pub fn`** — discovery APIs (`attrs().names/values`, `metrics().catalog/series/exemplars`) are `pub async fn`; a `pub fn` grep wrongly marks them missing.
- **Arrow column types:** result strings `Utf8` (not `Utf8View`); `service`/`resource`/`scope` `Dictionary(Utf8)`; ids `FixedSizeBinary`; time `Timestamp`; LGTM batches `Map`/`List`/`StringView`.
- **imbh drifts and its tree can be mid-edit** — breaking changes have hit the build (`IngestReceipt.lsn` → `Option<NonZero<u64>>`, `.queued` → `is_queued()`, the `query_batches` stats split; crates `0.0.0→0.1.0`; `imbh-query-language`/`imbh-semantics` folded into `imbh-lgtm`). Re-run the gate against upstream before assuming currency; retry transient breaks (a half-applied `not_terms`). See the op-id map + admin/discovery family in the source doc.

### sable FFI contract

- **`Payload::Handle(sable::Handle)`** tuple variant; `Handle::new(ptr, release)`; its `Drop` is the abandoned-path net (feeds [[ffi-ownership-and-safety-synthesis]]).
- **Cancellation is real on both paths.** `NextCtx` (stream) already existed — cancellation needed no sable change. `sable.CallCtx` (byte-`Call`) genuinely **aborts the Rust-side future** (spawns the task, stores its `abort_handle` in `CANCELS`, `h.abort()` drops the in-flight future) — not just the Go wait. So prefer `CallCtx` over `Call` for any op that can run long; a read-only aggregate is cancel-safe.
- **An open stream holds its admission slot until `Close`** → deterministic backpressure.
- **The empty-result `0x1` memory-safety bug (fixed in `545d04f`):** `sable_call_result` returned `Vec::as_ptr()` for an empty `Vec<u8>` — the dangling-but-aligned sentinel `0x1` (align of `u8`) — which landed in a GC-scanned Go pointer slot (via `Rows` teardown's `fetchQueryError`-on-every-close, which returns empty bytes) and crashed under GC stack scan. Fix: Rust returns `null()` on empty; Go holds the pointer as a non-scanned `uintptr`, materializing an `unsafe.Pointer` only when `n>0 && ptr!=0`. **General rule: a C-owned pointer must never live in a GC-scanned Go pointer variable; empty Rust slices carry a non-null sub-page sentinel.**

## Operational Guidance

- After any upstream bump: rebuild the staticlib, run the full gate (`cargo build` + `clippy -D warnings` + `go build`/`vet`/`test -race`), and re-verify against source — imbh evolves frequently.
- **Do not trust a subagent's "clippy clean / tests pass"** — re-verify the gate yourself from source (it has been wrong, e.g. a `collapsible_if` regression reported clean). Note the `Initial.` commit did NOT pass `clippy -D warnings`; the tree is green now.
- Serialize rust-touching parallel work — the archive is a global resource.
- Re-pin, never revert to path deps; keep imbh's three crates in lockstep and use a direct Go `require` for sable.
- Verify upstream signatures before writing glue — twice this has changed a design (no-serde return structs → `*Wire` mirrors; post-drain-only `LogPage` cursor → byte-`Call` side-channel, not schema-metadata).

## Files

- `rust/Cargo.toml` (crate type, features, dep pins), `go.mod` (`toolchain`, direct sable require), `Makefile` (`rust`/`test`/`example`/`leak-valgrind`).
- `rust/src/lib.rs` — all handlers + `*Wire` mirrors + `build_db`.
- `admin.go`/`ops.go`/`discovery.go`/`traces_search.go` — the Go-facing admin/discovery surface.
- `../sable/rust/src/lib.rs` + `../sable/call.go` — `sable_call_result`/`_ctx`/`_cancel`, `callResultBytes`, `NextCtx`.
- `ARCHITECTURE.md §3` — canonical build model + dependency-sourcing paragraph.

## Tests

- `concurrency_test.go` `TestConcurrentQueries` — 48 goroutines × 60 iters mixed SQL + typed; the crash-catcher for the `0x1` bug (run under `GOGC=10 -race`: was ~1/25 crashes, now 0/40).
- `durability_test.go` `TestDurabilityReopen` — `Open(TempDir)` → ingest → `Flush()` → `Close` → reopen → data survives.
- `admin_ops_test.go`, `discovery_test.go`, `TestCountLogs` (the `CallCtx` scalar path). Full suite 46+ under `-race`.

## Pitfalls

- Breaking imbh's crate lockstep splits the `imbh-core` instance → confusing type-mismatch errors in the glue.
- A `replace`-to-git for sable compiles locally but breaks every downstream importer — use a direct require.
- Building Go without a current `libimbhgo.a` links stale symbols.
- Never store a C-returned pointer in a GC-scanned Go variable; watch any op that routinely returns empty bytes.
- `545d04f` is a fix-branch commit, not `main` — merge + re-pin to a durable `main` commit/tag is an open follow-up (with CI + an amd64 run). See `.agents/docs/TODO.md`.
