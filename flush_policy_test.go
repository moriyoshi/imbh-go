package imbhgo

import (
	"testing"
	"time"
)

// bufferedAndSealedRows sums buffered and sealed row counts across every table in a stats snapshot.
func bufferedAndSealedRows(t *testing.T, db *DB) (buffered, sealed uint64) {
	t.Helper()
	st, err := db.Stats()
	if err != nil {
		t.Fatalf("Stats: %v", err)
	}
	for _, ts := range st.Tables {
		buffered += ts.BufferRows
		sealed += ts.SegmentRows
	}
	return buffered, sealed
}

// TestFlushPolicySealsOnItsOwnClock is the regression gate for imbh 0.2.0's flush scheduler. The
// policy's cadence is independent of the maintenance (retention) interval: with maintenance set to an
// hour, a "interval=50ms" policy must still seal the buffer within a fraction of that. On imbh 0.1.x
// the maintenance interval *was* the seal cadence, so nothing would seal here for an hour — which is
// exactly what this asserts against.
//
// Nothing in the test calls Flush()/Maintain(); the scheduler thread has to do it.
func TestFlushPolicySealsOnItsOwnClock(t *testing.T) {
	dir := t.TempDir()
	bodies := []string{"alpha", "bravo", "charlie"}

	db, err := OpenWith(DbOptions{
		Path: dir,
		// Who runs the scheduler. The interval is retention's cadence, deliberately far longer than
		// the test, so a pass on the maintenance clock cannot be what seals.
		MaintenanceBackgroundNs: int64(time.Hour),
		// When it seals. Ticks fast so the periodic trigger is not rounded up past the deadline.
		Flush: "interval=50ms,tick=10ms",
	})
	if err != nil {
		t.Fatalf("OpenWith(flush policy): %v", err)
	}
	defer db.Close()

	if _, err := db.IngestOTLPLogs(makeLogsRequest(t, "checkout", bodies)); err != nil {
		t.Fatalf("IngestOTLPLogs: %v", err)
	}

	deadline := time.Now().Add(15 * time.Second)
	for {
		buffered, sealed := bufferedAndSealedRows(t, db)
		if buffered == 0 && sealed == uint64(len(bodies)) {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("flush scheduler never sealed: buffered=%d sealed=%d, want buffered=0 sealed=%d",
				buffered, sealed, len(bodies))
		}
		time.Sleep(10 * time.Millisecond)
	}

	// Sealing must not have cost rows: the data is still queryable afterwards.
	if n := countLogs(t, db); n != int64(len(bodies)) {
		t.Fatalf("log count after scheduler seal = %d, want %d", n, len(bodies))
	}
}

// TestFlushPolicyManualDisablesSealing is the other side of that gate: an explicit "manual" policy
// turns every trigger off, so a running scheduler thread must leave the buffer alone until the host
// asks. This distinguishes a set-but-manual policy from an unset one, which still seals on the
// maintenance tick.
func TestFlushPolicyManualDisablesSealing(t *testing.T) {
	dir := t.TempDir()
	bodies := []string{"one", "two"}

	db, err := OpenWith(DbOptions{
		Path: dir,
		// A maintenance interval short enough that an unset policy would have sealed well within the
		// observation window below — so this really tests "manual", not "nothing had time to run".
		MaintenanceBackgroundNs: int64(20 * time.Millisecond),
		Flush:                   "manual",
	})
	if err != nil {
		t.Fatalf("OpenWith(manual flush): %v", err)
	}
	defer db.Close()

	if _, err := db.IngestOTLPLogs(makeLogsRequest(t, "checkout", bodies)); err != nil {
		t.Fatalf("IngestOTLPLogs: %v", err)
	}

	time.Sleep(500 * time.Millisecond)
	if buffered, sealed := bufferedAndSealedRows(t, db); buffered != uint64(len(bodies)) || sealed != 0 {
		t.Fatalf("manual policy sealed on its own: buffered=%d sealed=%d, want buffered=%d sealed=0",
			buffered, sealed, len(bodies))
	}

	// An explicit flush still seals.
	if err := db.Flush(); err != nil {
		t.Fatalf("Flush: %v", err)
	}
	if buffered, sealed := bufferedAndSealedRows(t, db); buffered != 0 || sealed != uint64(len(bodies)) {
		t.Fatalf("explicit Flush under a manual policy: buffered=%d sealed=%d, want buffered=0 sealed=%d",
			buffered, sealed, len(bodies))
	}
}

// TestFlushPolicyRejectsBadSpec pins one of the two places where an options string is *not* forgiving
// (the other is Duplicates): a malformed flush spec fails the open (and says why) instead of silently
// running a different cadence. The string tags (compression/wal_mode/refresh) keep their default on an
// unknown value.
func TestFlushPolicyRejectsBadSpec(t *testing.T) {
	for _, spec := range []string{"interval", "bogus=5s", "interval=5parsecs"} {
		db, err := OpenWith(DbOptions{Path: t.TempDir(), Flush: spec})
		if err == nil {
			db.Close()
			t.Fatalf("OpenWith(Flush: %q) succeeded; want a spec-parse failure", spec)
		}
		if got := err.Error(); got == "" {
			t.Fatalf("OpenWith(Flush: %q): empty error message", spec)
		} else {
			t.Logf("Flush: %q → %v", spec, err)
		}
	}
}
