# Project To-Dos

Follow-ups for the imbh-go binding. Each item should be resolved or removed once addressed. The
completed-and-verified milestones (M0–M4, the full LGTM surface, the leak gates) live in `JOURNAL.md`
and are not repeated here.

## Fillable coverage gaps

Missing capabilities that map to a real IMBH facade method and would slot into the existing zero-copy
stream / byte-`Call` machinery. Sourced from the 2026-07-24 feature-coverage matrix. Each notes the
upstream entry point and the wiring pattern.

### High value (SQL can't cleanly substitute)

- [x] **`LogPage` cursor paging.** ✅ 2026-07-24. `logpage.go`: `QueryLogPage(LogQuery, Cursor) *LogPage`
      + `LogPage{Entries,Next,Stats}`, opaque `Cursor`, `QueryStats`. `OP_LOG_PAGE=31` streams the page
      rows zero-copy via `query_batches_with_stats`; the `{next,stats}` (known only post-drain — schema
      metadata was ruled out) ride back through `OP_LOG_PAGE_META=32`, a byte-`Call` keyed by query id
      mirroring `OP_QUERY_ERROR`. Cursor carried opaquely (Go never inspects it → future-proof vs the
      keyset-paging switch the imbh docs flag); `after` round-trips through imbh's serde `PageCursor`
      (enabled imbh `serde` feature, pulls no new crate). **Also delivered `QueryStats`** (the same
      side-channel). Offset-cursor derivation mirrors `LogsApi::query`; revisit on a keyset move.
      *(imbh: `logs().query`, `LogQuery::after`, `query_batches_with_stats`)*
- [x] **Assembled `Trace` tree.** ✅ 2026-07-24. `trace_tree.go`: `TraceNode{Span; Children}` +
      `AssembleTrace([]Span) []*TraceNode` (parent→child forest; orphans surface as roots, dup SpanID
      first-wins, deterministic StartTime/SpanID ordering) + `(*DB).GetTraceForest(traceID)` (`GetTrace`
      was taken). Pure Go, 4 tests. *(binding-only)*
- [x] **Trace→log correlation drill-down.** ✅ 2026-07-24. Added `TraceID`/`SpanID` to Go `LogQuery` →
      `build_log_query` uses `imbh::TraceId::from_hex`/`SpanId::from_hex` → `.trace_id()`/`.span_id()`.
      Landed together with the predicate fields below. *(imbh: `LogQuery::trace_id`/`span_id`)*

### Richer typed-query fields (map to existing builders)

- [x] **`LogQuery` predicates.** ✅ 2026-07-24. Added `SeverityAtLeast` (→ `SeverityNumber(u8)`),
      `AttrExists`, `AttrMatches`, `AttrIn`/`AttrNotIn`, `AttrGt`/`Ge`/`Lt`/`Le`, `AttrRegex` to the Go
      struct + `LogQueryWire` + `build_log_query`. `TestLogQueryTraceCorrelationAndPredicates` covers
      trace-id, severity, and attr-exists filtering. *(imbh: `LogQuery` builder)*
- [x] **`traces().search` (`TraceSummary`).** ✅ 2026-07-24. `OP_TRACE_SEARCH=21` →
      `SearchTraces(TraceQuery) []TraceSummary`. `TraceQuery` wire covers service/name/status/kind/
      min-max duration/attr_eq/start-end/limit; `TraceSummary`→Arrow mapped binding-side (no upstream
      `search_batches` needed — the earlier "blocked" note was wrong). `traces_search.go`;
      `boolAt` reader added to `results.go`. Richer attr predicates (exists/matches/in/not_in/numeric/
      regex) ✅ added 2026-07-24 (LogQuery parity) — `TraceQuery` now has the full `Attr*` set.
      *(imbh: `traces().search`)*
- [x] **`metrics().instant` (`Vector`).** ✅ 2026-07-24. `OP_METRIC_INSTANT=22` →
      `QueryMetricInstant(MetricQuery) []InstantSample{Labels,Time,Value}` (one sample per series = last
      point of the range). Reuses `MetricQueryWire`/`build_metric_query`; `Vector`→Arrow via the shared
      `series_batch` helper. *(imbh: `metrics().instant`)*

### Discovery / catalog (small, flat-table shaped)

- [x] **`attrs()` discovery** ✅ 2026-07-24. `discovery.go`: `(*DB).AttrNames()` / `AttrValues(key)` via
      new ops `OP_ATTR_NAMES=15` / `OP_ATTR_VALUES=16` → `attrs().names()` / `values(&key)` → single Utf8
      Arrow column, decoded to `[]string`. *(the API is `pub async fn` — the earlier `pub fn` grep missed it.)*
- [x] **`metrics().catalog` / `series` / `exemplars`.** ✅ 2026-07-24. `OP_METRIC_CATALOG=17` →
      `MetricCatalog() []MetricInfo{Metric,Unit,Temporality,Kind}`; `OP_METRIC_SERIES=18` →
      `MetricSeries(metric) []string` (canonical JSON label-set via `imbh_core::canonical_json_object`,
      added `imbh-core` path dep to `rust/Cargo.toml`); `OP_METRIC_EXEMPLARS=19` →
      `MetricExemplars(metric) []Exemplar{Time,Value,TraceID,SpanID,Attributes}` (ids via `to_hex()`,
      SQL-NULL when absent). *(imbh: `metrics().catalog`/`series`/`exemplars`)*
- [x] **`logs().volume` / `volume_by`.** ✅ 2026-07-24. `OP_LOG_VOLUME=20` → `LogVolume(q, stepNs)` /
      `LogVolumeBy(q, stepNs, groupBy)` → `[]VolumeBucket{Time,Labels,Count}` over `logs().volume_by`
      (reuses `build_log_query` for the filter; labels canonical JSON). *(imbh: `logs().volume`)*

### Admin / lifecycle surface

- [x] **Read-only open** (`Db::open_read_only`) ✅ 2026-07-24. `admin.go`: `OpenReadOnly(path)` →
      new FFI `imbhgo_open_read_only` (mirrors `imbhgo_open`). Takes no writer lock, coexists with the
      single writer + other readers; writes on the handle are rejected (tested). *(imbh: `open_read_only`)*
- [x] **`DbBuilder` options** ✅ 2026-07-24. `admin.go`: `OpenWith(DbOptions)` → new FFI
      `imbhgo_open_opts` (JSON options → `DbBuilder` setters in `build_db`). Exposes read-only,
      allow-stale-reads, memory budget, compression (+zstd level), WAL mode (+interval), retention
      (days/max-disk), refresh (+ttl), background maintenance, promote keys. The two host-runtime-Handle
      variants (`Maintenance::Runtime`, `Ingest::Async`) are intentionally deferred — they need explicit
      tokio-runtime wiring. *(imbh: `DbBuilder`)*
- [x] **Ops passthrough** ✅ 2026-07-24. `ops.go`: byte-`Call` ops `OP_STATS=23`..`OP_EXPORT=30` →
      `Stats`/`Maintain`/`Compact`/`Snapshot`/`Segments`/`SegmentFiles`/`DurableThrough`/`Export`
      (+ `ExportRecords`, Arrow-IPC decode). Request `[db id][JSON args]`; reply JSON of a serde wire
      struct (imbh's return structs derive no serde, so each is mirrored field-by-field), or raw
      Arrow-IPC bytes for export. `Table` string type (7 variants). *(imbh: `Db` methods)*

## Robustness / packaging (not coverage — see JOURNAL "remaining")

- [x] On-disk durability test ✅ 2026-07-24. `durability_test.go` / `TestDurabilityReopen`:
      `Open(TempDir)` → ingest logs+gauge → `Flush()` (seal; required for cross-reopen persistence) →
      verify → `Close()` → re-`Open(same path)` → query without re-ingest, assert counts/values survive.
- [x] Concurrency-under-load test ✅ 2026-07-24. `concurrency_test.go` / `TestConcurrentQueries`:
      48 goroutines × 60 iters, mixed SQL + typed queries against one shared `Db`, every `Rows` drained
      + closed. **Surfaced a real sable FFI memory-safety bug** (empty-result pointer `0x1` from
      `Vec::as_ptr` rejected by Go's GC on stack scan; hit via `Rows` teardown's `fetchQueryError`).
      Fixed in `../sable` (null-for-empty in `sable_call_result`; non-scanned `uintptr` in
      `call.go::callResultBytes`). See JOURNAL 2026-07-24.
- [~] External consumability: **deps done** ✅ 2026-07-24 — no local path deps remain.
      - **imbh** consumed from crates.io `0.1.0`: `imbh`/`imbh-core`/`imbh-lgtm` pinned in lockstep so the
        `imbh-core` instance unifies.
      - **sable** pinned as a git dep at the **`main`** commit `0c6fe56` (carries the memory-safety fix
        from PR #1 and the Apple-target port from PR #2): Rust
        `sable = { git = "https://github.com/moriyoshi/sable", rev = "0c6fe56" }` (cargo finds the crate in
        the repo's `rust/` subdir), Go a **direct** `require github.com/moriyoshi/sable
        v0.0.0-20260726045720-0c6fe56eb099` with **no `replace`** (a replace is ignored for downstream
        consumers, so a direct require is what actually makes it externally buildable). Full gate green
        incl. the concurrency crash-catcher under `GOGC=10 -race`.
      - **Still open (infra):** CI + an amd64 run. **Follow-up:** the pin is still a fix-branch commit, not
        `main` — merge it and re-pin to a `main` commit/tag for durability. That merge needs a **signed**
        commit: sable's `main` ruleset requires verified signatures and the key is passphrase-protected
        (feature branches are exempt, which is why the branch push works unattended).
- [x] Upstream Arrow-native `imbh-lgtm` execute path — **DONE upstream AND adopted here** ✅ 2026-07-24.
      The three LGTM handlers (`promql`/`logql`-range/`traceql`) now call `execute_promql_batches` /
      `execute_logql_batches` / `execute_traceql_batches` and stream the upstream batch directly — dropped
      our manual `Vec<PromSeries>`→Arrow remap and `labels_json`. New schema is `labels:Map<Utf8View,Utf8View>`,
      `ts:Timestamp(ns)`, `value:Float64` for series and `{trace_id:Utf8View, span_ids:List<Utf8View>}` for
      traces; Go `decodeLabeledSeries`/`decodeTraceMatches` rewritten to read Map/List/StringView/Timestamp
      (new `mapStringAt`/`stringListAt`/`stringFromArray` readers). **Go-facing `Series`/`Point`/`TraceMatch`
      unchanged** — `lgtm_test.go` passed unmodified (behavior-preservation proof). arrow-go v18.5.1 imports
      Map/List/StringView/Timestamp over the C Data Interface cleanly; leak/UAF gates green ×3 under -race.
      `metric_instant` intentionally stays on the old `series_batch` schema (its own decoder).
      *(imbh-lgtm: `execute_*_batches`)*

## Watch-items / deferred (extracted by good-sleep 2026-07-25)

Not "just wire it" gaps — these are open follow-ups and revisit triggers surfaced across the JOURNAL
entries, now consolidated into `.agents/docs/LTM/`. Cross-referenced to the LTM doc that carries the detail.

### Infra (the only remaining non-code work — needs user decisions)

- [x] **Validate `release.yml` end-to-end on real runners.** ✅ 2026-07-26 — run 30185313898 completed
      `success`. All six real cells built, `publish` attached 6 assets + per-asset `.sha256` + a combined
      `SHA256SUMS` to `v0.1.0`, and **both `smoke` jobs passed**, proving a consumer can `go get` the module,
      `imbhgo-fetch` the prebuilt archive, and build + run with **no Rust toolchain**. Everything downstream
      of `Install zig` is now exercised. *(2026-07-26 "CD round 4" entry)*
- [~] **CI + an amd64 run.** Partly closed 2026-07-26: amd64 is no longer unexercised — `linux/amd64`
      glibc + musl **build** on real runners and the `smoke linux/amd64` consumer job passes. **Still open:**
      no *test* CI at all. `go test -race ./...` and the Rust suite have still only ever run on this arm64
      box; `release.yml` builds and publishes but never runs a test. A push/PR CI workflow running the
      standard gate (ideally on both arches) is the remaining gap. *(LTM: build-toolchain-and-deps)*
- [~] **`windows/amd64` — now builds; promote out of best-effort once it is link-tested.** Unblocked
      upstream 2026-07-26: sable landed the Windows fast crossing (its PR #7) and IOCP fd-fusion (#10), and
      now tests the **fast** staticlib for `x86_64-pc-windows-gnu` natively. Re-pinned here, the cell was
      reproduced green locally — a 340 MB archive of 2732 `Intel amd64 COFF` members exporting
      `imbhgo_open` / `imbhgo_shell_free` / `sable_goexec_init` / `sable_call_result`. Left `best_effort:
      true` deliberately: that flag gates only *failure*, so a green build already uploads and publishes,
      and nothing yet link-tests or runs the archive on Windows (`smoke` covers only linux/{amd64,arm64},
      and `link_windows.go`'s lib set is an unverified guess that must union with sable's — sable adds
      `-lkernel32` and `-ldbghelp` that ours omits). **Promote to a blocking cell once a windows `smoke`
      job exists**; until then a real regression there would pass unnoticed.
      *(2026-07-26 "CD round 5" entry)*
      **The predicted regression arrived 2026-07-28**, and not at link time: a downstream consumer's
      windows/amd64 release leg failed *before* the compiler, because `imbhgo-fetch -print-env` chose its
      shell dialect from the **target** GOOS and emitted cmd.exe's `set VAR=…` into git-bash. Fixed on main
      (`-shell sh|cmd|powershell`, POSIX default) — **but the fix is unreleased**, and consumers pin
      `imbhgo-fetch@v0.1.0`, so this needs a `v0.1.1` tag to reach anyone. A windows `smoke` job that runs
      the documented `eval "$(… -print-env)"` under the Actions `bash` shell would have caught it.
      *(2026-07-28 journal entry)*
- [ ] **Add `workflow_dispatch` to `release.yml`.** The `Resolve version` step already has a
      `github.event.inputs.version` branch, but the workflow declares only the `push`/`v*` trigger — so that
      branch is dead code and every re-validation costs a tag delete + re-push. Declaring the trigger makes the
      dead branch live and the release path cheap to iterate on. *(2026-07-25 "CD fix round 2" entry)*
- [x] **Pin sable to a `main` commit.** ✅ 2026-07-26 — **closed, and it was two stale items, not one.**
      (a) `545d04f` had been on `main` since sable PR #1 merged 2026-07-24 (`bff774f`); the "on a fix branch,
      not `main`" wording had gone stale and was copied across the docs unchecked. (b) The macOS port
      (sable PR #2) merged 2026-07-25; its head was signed and extended upstream before landing, so the
      `62f3f34` this repo pinned became an orphan on no branch. Pinned to `main` thereafter (briefly
      `30b2c30`, then `0c6fe56` once the Windows work landed) — see the "current pin" line above, which is
      the single place to read it from. Full gate re-run green at each step. Verify such claims with
      `gh api repos/moriyoshi/sable/compare/main...<rev>` rather than trusting prose.
      *(LTM: build-toolchain-and-deps, sable-ffi-integration)*
- [ ] **Wire an Apple `cargo check` into imbh-go's own CI.** Upstream sable now has Darwin/Windows CI plus
      emulation harnesses (its PRs #5/#6), but *this* repo has no guard: the Apple targets are only known to
      build because they were checked by hand. `cargo check --target {aarch64,x86_64}-apple-darwin` needs
      **no macOS SDK** (check never links), so a plain Linux job catches the next Linux-only-API regression
      before it reaches a release tag. Real-hardware certification (`GOOS=darwin make abi-check`) stays an
      upstream concern. *(2026-07-25 "CD fix round 3" entry)*

### Deferred designs / revisit triggers

- [ ] **LogPage keyset-paging revisit.** The offset-cursor derivation (`prev_offset + rows_returned`,
      full-page-iff-`rows==limit`) is replicated in the glue, mirroring `LogsApi::query`. An upstream move to
      keyset paging invalidates it (and the `after` `as_u64` read) — revisit then. The Go `Cursor` is opaque
      raw JSON, so only that one glue line breaks, not the Go API. *(LTM: streaming-query-errors-cancellation)*
- [ ] **Host-runtime-`Handle` DbBuilder variants.** `Maintenance::Runtime` and `Ingest::Async` are intentionally
      deferred in `OpenWith(DbOptions)` — they need explicit tokio-runtime wiring, a separate design.
      *(LTM: imbh-upstream-surface)*
- [ ] **PromQL metric-name sanitization.** Resolution is 1:1 + dots→underscores only, not full Prometheus name
      sanitization — a known follow-on if broader Prometheus name compatibility is wanted.
      *(LTM: lgtm-query-languages)*
- [ ] **Pending ASAN/LSan leak gate.** A sanitizer-based third leak check is still on the wishlist (the counter
      + Valgrind gates cover shell + buffer today). Design new FFI tests so they can also run under a sanitizer.
      *(LTM: leak-uaf-verification)*
- [~] **Confirm darwin/windows prebuilt link libs.** The sable half is **done upstream** ✅ 2026-07-26:
      `link_extern.go` on `main` is now GOOS-gated (`#cgo linux: -lgcc_s -lutil -lrt -lpthread -lm -ldl`,
      `#cgo darwin: -liconv`, `#cgo windows: -lkernel32 -lntdll -luserenv -lws2_32 -ldbghelp`) instead of
      emitting the Linux set unconditionally, and this repo is re-pinned to it. **Still open:** our own
      `link_darwin.go` (`-lpthread -lm -framework CoreFoundation -framework Security`) and `link_windows.go`
      (`-lws2_32 -lbcrypt -luserenv -lntdll -ladvapi32`) remain first-pass guesses, unverified against a real
      link. They are exercised by the `smoke` job, not by `build`, so no release run has reached them yet —
      note the two sets must *union* correctly with sable's, e.g. sable adds `-liconv` on darwin and
      `-ldbghelp` on windows that ours do not list. *(2026-07-25 prebuilt-distribution entry)*

## Notes

- The coverage matrix (README) and this list are **drift-prone** — IMBH evolved twice this session
  (`IngestReceipt` shape change; new `_batches` entry points). Re-verify against upstream when the
  facade or LGTM surface moves; regenerate ✅/❌ from the two greps recorded in the 2026-07-24 journal
  entries rather than hand-editing.
- `imbh-tui` is a useful **oracle** for the LGTM/typed surface — re-diff it when closing these.
