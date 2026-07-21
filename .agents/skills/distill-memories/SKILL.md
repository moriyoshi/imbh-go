---
name: distill-memories
description: Read `.agents/docs/JOURNAL.md` and `.agents/docs/LTM/` documents, find durable knowledge that belongs in the canonical docs, and update those documents with concise synthesized findings. Targets are `.agents/docs/OVERVIEW.md`, the root `ARCHITECTURE.md`, and the consolidated plan `.agents/docs/PLAN.md`. Use when journal or LTM notes have accumulated implementation or design knowledge that should be promoted into the project's canonical overview, architecture, or plan.
user-invocable: true
allowed-tools: Bash, Read, Write, Edit, Grep, Glob
---

# Distill Memories

## Overview

Promote durable facts from `.agents/docs/JOURNAL.md` and `.agents/docs/LTM/` into the project's canonical documentation, keeping these up to date:

a. `.agents/docs/OVERVIEW.md` ... high-level system understanding for coding agents (what imbh-go is, how imbh + sable + this binding fit together, the build model, key design decisions, status). Keep it a fast orientation read.
b. `ARCHITECTURE.md` (repository root) ... the canonical, human-reader-ready architecture document: component boundaries, the combined-staticlib build model, the data path, the async/streaming contract, the two-free ownership protocol, the op protocol and C ABI, status/milestones, and the source map. Favor durable design and narrative over patch-level history.
c. `.agents/docs/PLAN.md` ... the consolidated planning and prescription record whose rationale `ARCHITECTURE.md` synthesizes. It holds: the zero-copy design and phasing; the imbh prescription (I-1..I-5) and the sable prescription (S-1..S-5), including what each upstream actually implemented; the binding milestones (M0..M4); the two-free ownership protocol; and the I-4a lazy-scan detail. The imbh/sable prescription parts describe the two sibling repos, not this one — update them only to reflect what the upstream actually implemented or to correct a prescription that proved wrong.

When a durable fact lands, `ARCHITECTURE.md` is usually its home for as-built structure; `OVERVIEW.md` carries the shorter orientation version; and `PLAN.md` carries the design rationale, the upstream prescriptions, and the milestone/status detail. Keep the three in their own registers rather than duplicating prose.

## Sources: JOURNAL and LTM

- `.agents/docs/JOURNAL.md` is the append-only log of findings, insights, and code-review history. It holds the **freshest** durable facts, including changes not yet consolidated into LTM. The `good-sleep` / `reconcile-journal-ltm` skills consolidate JOURNAL entries into `.agents/docs/LTM/`; distill runs against whatever is present.
- `.agents/docs/LTM/` holds the consolidated, topic-organized reference material (`INDEX.md` is its table of contents).

Read both. When JOURNAL and LTM overlap, treat LTM as the settled synthesis and JOURNAL as the source of anything newer than the last consolidation. If JOURNAL records a substantial code change that the target docs do not yet reflect, that is exactly the gap this skill closes.

## Read in This Order

1. Read `.agents/docs/OVERVIEW.md` first.
2. Read `.agents/docs/JOURNAL.md` (at least the entries since the last consolidation record) and `.agents/docs/LTM/INDEX.md`.
3. Read the root `ARCHITECTURE.md` to see what the canonical architecture already records.
4. Open only the LTM documents that look relevant to the gaps, stale sections, or missing detail you identified.
5. For any candidate fact that concerns the design rationale, the upstream prescriptions, or the milestones, read the relevant section of `.agents/docs/PLAN.md` to check what is already documented.

Do not bulk-load every LTM file unless the set is still small enough that selective reading costs more than reading them all.

## Classify Findings Before Editing

For each candidate fact, decide whether it belongs in:

- `.agents/docs/OVERVIEW.md`: product scope, how the three pieces fit, the combined-staticlib build model, high-level design decisions, and current status. Keep it a fast orientation read.
- `ARCHITECTURE.md` (repository root): component boundaries, the build model, the data path, the async/streaming contract, the two-free ownership protocol, the op protocol and C ABI, status/milestones, and the source map. This is the canonical home for most durable design facts; keep it human-reader-ready.
- `.agents/docs/PLAN.md`: the deeper design *rationale* — why a decision was made, the alternatives weighed, what the upstreams (imbh, sable) actually implemented, and the milestone/status detail. Favor durable rationale over patch-level history.
- More than one: a single change often lands in several places (e.g. a change to the ownership protocol belongs in `ARCHITECTURE.md` §6, the `OVERVIEW.md` decisions summary, and `PLAN.md` §4.2). Update each in its own register, without duplicating prose.
- None of them: narrow bug history, one-off investigations, temporary workarounds, or details too fine-grained for canonical docs.

Prefer durable knowledge over incident history. Convert timelines into timeless guidance.

The source of truth for implementation detail is always the code (`rust/src/lib.rs`, `db.go`, `imbhgo.h`, the upstream crates under `../imbh` and `../sable`). Verify a fact against the code before writing it into a design doc; JOURNAL/LTM point you at what changed, not the exact current symbol or ABI.

## Update Strategy

When updating the target docs:

- Synthesize; do not copy JOURNAL/LTM prose verbatim.
- Merge into existing sections when possible instead of appending random new sections.
- Add a new section only when the information represents a stable topic the current document truly lacks.
- Keep summaries compact. Canonical docs should stay easier to scan than the underlying notes.
- Preserve exact file paths, crate/feature names, C-ABI symbol names (`imbhgo_*`, `sable_*`), and Arrow/DataFusion terms when they help precision.
- If multiple sources disagree, or JOURNAL and the current code disagree, call out the ambiguity or stop and ask the user before cementing one interpretation.

## Editing Heuristics

- Favor architectural patterns and contracts over patch-level history.
- Favor the current build/ownership/streaming model over implementation anecdotes.
- Omit test names unless they explain an important invariant or guarantee (e.g. the leak/UAF gates).
- Omit details already covered well in the target document.
- Fix obvious typos or stale wording in the touched sections if doing so improves clarity.

## Validation

Before finishing:

1. Re-read the edited sections of every document you changed.
2. Check that each added fact is supported by a source note (JOURNAL/LTM) and, for implementation detail, verified against the code.
3. Check that overview-level material did not leak into fine-grained design detail, and vice versa.
4. Keep the documentation style rules in `AGENTS.md`: half-width parentheses and half-width colons.
