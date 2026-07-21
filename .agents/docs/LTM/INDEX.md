# Long-Term Memory Index

Durable, topic-organized project knowledge distilled from `.agents/docs/JOURNAL.md` by the `good-sleep` / `deep-sleep` skills. These documents are meant to be edited and refined over time, unlike the append-only JOURNAL.

## Synthesis documents

Second-stage consolidations (`deep-sleep`) — start here for orientation, then drill into the source documents they consolidate.

| Synthesis document | Consolidates | Summary |
|--------------------|--------------|---------|
| [ffi-ownership-and-safety-synthesis.md](ffi-ownership-and-safety-synthesis.md) | zero-copy-arrow-handoff, arrow-buffer-lifetime-rules, leak-uaf-verification | How batches cross the FFI boundary safely (two-free protocol + aliasing rules) and how it is proven leak/UAF-clean |
| [data-path-synthesis.md](data-path-synthesis.md) | ingest-and-backpressure, streaming-query-errors-cancellation, single-transport-typed-queries, lgtm-query-languages | The runtime data path: streaming Arrow for results, byte-`Call` for scalars/ingest |
| [upstream-integration-synthesis.md](upstream-integration-synthesis.md) | build-toolchain-and-deps, imbh-upstream-surface, sable-ffi-integration | Consuming the two moving upstreams: build/link model, dep sourcing, imbh + sable surfaces |

## Source documents

Topic-level notes (`good-sleep`) with the full detail behind each synthesis.

| Document | Summary |
|----------|---------|
| [zero-copy-arrow-handoff.md](zero-copy-arrow-handoff.md) | Arrow C Data Interface handoff + the two-free ownership protocol |
| [arrow-buffer-lifetime-rules.md](arrow-buffer-lifetime-rules.md) | cdata import is a move; `String.Value` aliases the buffer; `strings.Clone` on copy-out |
| [leak-uaf-verification.md](leak-uaf-verification.md) | The two leak gates: in-process `LIVE_BATCHES` counter + the Valgrind buffer gate |
| [streaming-query-errors-cancellation.md](streaming-query-errors-cancellation.md) | Per-batch cursor, out-of-band error channel, `NextCtx` cancellation, the scalar side-channel pattern |
| [ingest-and-backpressure.md](ingest-and-backpressure.md) | OTLP ingest (byte-`Call`, 26-byte receipt) + backpressure via the global admission cap |
| [single-transport-typed-queries.md](single-transport-typed-queries.md) | Arrow-everywhere architecture; native-JSON typed queries + Go-side struct decoders |
| [lgtm-query-languages.md](lgtm-query-languages.md) | PromQL/LogQL/TraceQL wiring, Arrow-native `execute_*_batches`, and language semantics |
| [imbh-upstream-surface.md](imbh-upstream-surface.md) | IMBH API constraints, drift history, column types, the admin/ops surface + op-id map |
| [build-toolchain-and-deps.md](build-toolchain-and-deps.md) | Combined staticlib build model, toolchain pin, cgo constraints, dependency sourcing |
| [sable-ffi-integration.md](sable-ffi-integration.md) | sable API shapes, real cancellation on both paths, the empty-result `0x1` memory-safety bug + fix |
