package imbhgo

import (
	"context"
	"strings"
	"testing"
	"time"
)

// dupGaugeRequest is one gauge datapoint at a fixed instant: ingesting it twice produces two points
// sharing a series (service + __name__ + no string attributes) *and* a timestamp — the pair PromQL has
// no meaning for, and the input every case below is about.
func dupGaugeRequest(t *testing.T, service, metric string, value float64, ts int64) []byte {
	t.Helper()
	return makeGaugeRequest(t, service, metric, []float64{value}, ts)
}

// TestDuplicatesDefaultFailsTheRead is the "before" half of the imbh 0.5.0 gate: with no policy set,
// ingest accepts both points and it is the *query* that fails — over the whole metric, for as long as
// the points are retained. Asserting it here is what gives the two remedies below their meaning; if a
// future imbh silently starts collapsing duplicates by default, this is the test that notices.
func TestDuplicatesDefaultFailsTheRead(t *testing.T) {
	db, err := OpenWith(DbOptions{Path: t.TempDir()})
	if err != nil {
		t.Fatalf("OpenWith(default duplicates): %v", err)
	}
	defer db.Close()

	base := int64(1_700_000_000_000_000_000)
	for i := 0; i < 2; i++ {
		r, err := db.IngestOTLPMetrics(dupGaugeRequest(t, "web", "cpu_seconds", 0.25, base))
		if err != nil {
			t.Fatalf("IngestOTLPMetrics #%d: %v", i, err)
		}
		if r.Rejected != 0 {
			t.Fatalf("ingest #%d rejected %d points under the default policy; want 0 (it rejects at read)", i, r.Rejected)
		}
	}

	_, err = db.QueryPromQLSeries(context.Background(), "cpu_seconds", base-int64(time.Minute), base+int64(time.Minute), int64(time.Second))
	if err == nil {
		t.Fatal("PromQL over a duplicated series succeeded under the default policy; want a duplicate-timestamp error")
	}
	// 0.5.0's diagnostic names the metric, the label set and the instant — that specificity is the
	// point of the release, so a bare "query failed" is a regression worth catching.
	if msg := err.Error(); !strings.Contains(msg, "cpu_seconds") {
		t.Fatalf("duplicate-timestamp error does not name the metric: %v", msg)
	} else {
		t.Logf("default policy → %v", msg)
	}
}

// TestDuplicatesLastWinsCollapsesAtRead: the read-time remedy. Same duplicated input as above, but the
// query now answers instead of failing — one instant is degraded rather than the whole metric. This is
// the only remedy for points already on disk, so it must work on a database that already holds them.
func TestDuplicatesLastWinsCollapsesAtRead(t *testing.T) {
	dir := t.TempDir()
	base := int64(1_700_000_000_000_000_000)

	// Write the duplicates under the default policy first, so the reopen below is genuinely reading
	// pre-existing bad data rather than data ingested under the policy that rescues it.
	db, err := OpenWith(DbOptions{Path: dir})
	if err != nil {
		t.Fatalf("OpenWith(default duplicates): %v", err)
	}
	for i := 0; i < 2; i++ {
		if _, err := db.IngestOTLPMetrics(dupGaugeRequest(t, "web", "cpu_seconds", 0.25, base)); err != nil {
			t.Fatalf("IngestOTLPMetrics #%d: %v", i, err)
		}
	}
	if err := db.Flush(); err != nil {
		t.Fatalf("Flush: %v", err)
	}
	db.Close()

	db, err = OpenWith(DbOptions{Path: dir, Duplicates: "last_wins"})
	if err != nil {
		t.Fatalf("OpenWith(last_wins): %v", err)
	}
	defer db.Close()

	series, err := db.QueryPromQLSeries(context.Background(), "cpu_seconds", base-int64(time.Minute), base+int64(time.Minute), int64(time.Second))
	if err != nil {
		t.Fatalf("QueryPromQLSeries under last_wins: %v", err)
	}
	total := 0
	for _, s := range series {
		total += len(s.Points)
	}
	if total == 0 {
		t.Fatal("last_wins collapsed the duplicated instant to nothing; want the series to still carry samples")
	}
	t.Logf("last_wins → %d series, %d samples", len(series), total)
}

// TestDuplicatesRejectDropsAtIngest: the ingest-side remedy. The repeat never reaches storage, and the
// producer learns about it at write time through Receipt.Rejected — the counter that was always 0
// before 0.5.0 — with DbStats.IngestRejected carrying the cumulative view.
func TestDuplicatesRejectDropsAtIngest(t *testing.T) {
	db, err := OpenWith(DbOptions{Path: t.TempDir(), Duplicates: "reject,recent=4096"})
	if err != nil {
		t.Fatalf("OpenWith(reject): %v", err)
	}
	defer db.Close()

	base := int64(1_700_000_000_000_000_000)
	first, err := db.IngestOTLPMetrics(dupGaugeRequest(t, "web", "cpu_seconds", 0.25, base))
	if err != nil {
		t.Fatalf("IngestOTLPMetrics (first): %v", err)
	}
	if first.Accepted != 1 || first.Rejected != 0 {
		t.Fatalf("first ingest: accepted=%d rejected=%d, want accepted=1 rejected=0", first.Accepted, first.Rejected)
	}

	repeat, err := db.IngestOTLPMetrics(dupGaugeRequest(t, "web", "cpu_seconds", 0.25, base))
	if err != nil {
		t.Fatalf("IngestOTLPMetrics (repeat): %v", err)
	}
	if repeat.Rejected != 1 || repeat.Accepted != 0 {
		t.Fatalf("repeat ingest: accepted=%d rejected=%d, want accepted=0 rejected=1", repeat.Accepted, repeat.Rejected)
	}

	st, err := db.Stats()
	if err != nil {
		t.Fatalf("Stats: %v", err)
	}
	if st.IngestRejected != 1 {
		t.Fatalf("DbStats.IngestRejected = %d, want 1", st.IngestRejected)
	}

	// A point at a *different* instant on the same series is not a duplicate: the guard keys on
	// (series, timestamp), so it must not degrade into "one point per series".
	next, err := db.IngestOTLPMetrics(dupGaugeRequest(t, "web", "cpu_seconds", 0.5, base+int64(time.Second)))
	if err != nil {
		t.Fatalf("IngestOTLPMetrics (next instant): %v", err)
	}
	if next.Accepted != 1 || next.Rejected != 0 {
		t.Fatalf("next-instant ingest: accepted=%d rejected=%d, want accepted=1 rejected=0", next.Accepted, next.Rejected)
	}

	// With the repeat never written, the read is clean — no duplicate-timestamp error.
	if _, err := db.QueryPromQLSeries(context.Background(), "cpu_seconds", base-int64(time.Minute), base+int64(time.Minute), int64(time.Second)); err != nil {
		t.Fatalf("QueryPromQLSeries after reject: %v", err)
	}
}

// TestDuplicatesRejectSurvivesWalReplay guards the ordering property 0.5.0's design turns on: the
// guard is process-local and starts empty, so a reopen replays the WAL tail with an empty guard. That
// is deliberately *more* permissive than the writer was — replay must never drop a row the writer
// accepted. Reopening after a hard close (no flush) and finding the accepted points still queryable is
// what that guarantees.
func TestDuplicatesRejectSurvivesWalReplay(t *testing.T) {
	dir := t.TempDir()
	base := int64(1_700_000_000_000_000_000)

	db, err := OpenWith(DbOptions{Path: dir, Duplicates: "reject", WalMode: "always"})
	if err != nil {
		t.Fatalf("OpenWith(reject): %v", err)
	}
	for i := 0; i < 3; i++ {
		ts := base + int64(i)*int64(time.Second)
		if _, err := db.IngestOTLPMetrics(dupGaugeRequest(t, "web", "cpu_seconds", float64(i), ts)); err != nil {
			t.Fatalf("IngestOTLPMetrics #%d: %v", i, err)
		}
		// The repeat is dropped by the writer's guard, so it never enters the WAL.
		if _, err := db.IngestOTLPMetrics(dupGaugeRequest(t, "web", "cpu_seconds", float64(i), ts)); err != nil {
			t.Fatalf("IngestOTLPMetrics repeat #%d: %v", i, err)
		}
	}
	db.Close() // no Flush: the points are in the WAL tail, not a sealed segment.

	db, err = OpenWith(DbOptions{Path: dir, Duplicates: "reject", WalMode: "always"})
	if err != nil {
		t.Fatalf("reopen(reject): %v", err)
	}
	defer db.Close()

	series, err := db.QueryPromQLSeries(context.Background(), "cpu_seconds", base-int64(time.Minute), base+int64(time.Minute), int64(time.Second))
	if err != nil {
		t.Fatalf("QueryPromQLSeries after replay: %v", err)
	}
	total := 0
	for _, s := range series {
		total += len(s.Points)
	}
	if total == 0 {
		t.Fatal("WAL replay under a reject policy lost every accepted point")
	}
	t.Logf("reject + WAL replay → %d series, %d samples", len(series), total)
}

// TestDuplicatesRejectsBadSpec: like Flush, a malformed duplicates spec fails the open instead of
// silently running a policy the caller did not ask for. Getting this wrong is worse than for flush —
// a typo falling back to the default would leave ingest accepting the repeats the caller asked to
// drop, and nothing would say so until a PromQL query failed.
func TestDuplicatesRejectsBadSpec(t *testing.T) {
	for _, spec := range []string{"nonsense", "reject,recent=lots", "reject,bogus=1"} {
		db, err := OpenWith(DbOptions{Path: t.TempDir(), Duplicates: spec})
		if err == nil {
			db.Close()
			t.Fatalf("OpenWith(Duplicates: %q) succeeded; want a spec-parse failure", spec)
		}
		if err.Error() == "" {
			t.Fatalf("OpenWith(Duplicates: %q): empty error message", spec)
		}
		t.Logf("Duplicates: %q → %v", spec, err)
	}
}
