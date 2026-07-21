package imbhgo

import (
	"context"
	"testing"
	"time"
)

// TestQueryLogPage walks a log query one bounded page at a time via the resume cursor, and checks the
// scan statistics ride back alongside the rows. Ingested bodies are distinct and time-ordered, so a
// forward walk must return every row exactly once, in order, across the pages.
func TestQueryLogPage(t *testing.T) {
	base := int64(1_700_000_000_000_000_000)
	bodies := []string{"b0", "b1", "b2", "b3", "b4", "b5", "b6", "b7", "b8", "b9"}
	const pageLimit = 4
	wantPages := (len(bodies) + pageLimit - 1) / pageLimit // 10 / 4 → 3 pages (4 + 4 + 2)

	db, err := OpenInMemory()
	if err != nil {
		t.Fatalf("OpenInMemory: %v", err)
	}
	defer db.Close()

	if _, err := db.IngestOTLPLogs(makeLogsRequest(t, "checkout", bodies)); err != nil {
		t.Fatalf("IngestOTLPLogs: %v", err)
	}
	if err := db.Flush(); err != nil {
		t.Fatalf("Flush: %v", err)
	}

	q := LogQuery{
		Service: "checkout",
		Limit:   pageLimit,
		Start:   base - int64(time.Hour),
		End:     base + int64(time.Hour),
	}

	var got []string
	var after Cursor
	pages := 0
	for {
		pg, err := db.QueryLogPage(context.Background(), q, after)
		if err != nil {
			t.Fatalf("QueryLogPage (page %d): %v", pages, err)
		}
		pages++
		// Every page but possibly the last carries exactly pageLimit rows; stats agree with the rows.
		if pg.Stats.RowsReturned != uint64(len(pg.Entries)) {
			t.Fatalf("page %d: Stats.RowsReturned=%d, len(Entries)=%d", pages, pg.Stats.RowsReturned, len(pg.Entries))
		}
		if pg.Next.HasMore() && len(pg.Entries) != pageLimit {
			t.Fatalf("page %d: has a next cursor but only %d rows (want a full page of %d)", pages, len(pg.Entries), pageLimit)
		}
		for _, e := range pg.Entries {
			got = append(got, e.Body)
		}
		if !pg.Next.HasMore() {
			break
		}
		after = pg.Next
		if pages > len(bodies)+1 {
			t.Fatalf("paging did not terminate after %d pages", pages)
		}
	}

	if pages != wantPages {
		t.Fatalf("walked %d pages, want %d", pages, wantPages)
	}
	if len(got) != len(bodies) {
		t.Fatalf("collected %d rows across pages, want %d", len(got), len(bodies))
	}
	// Forward walk ⇒ rows come back in ingest (time) order, exactly once each.
	for i, want := range bodies {
		if got[i] != want {
			t.Fatalf("row %d = %q, want %q (full: %v)", i, got[i], want, got)
		}
	}
}

// TestQueryLogPageSinglePage checks that a page whose limit covers the whole result returns no resume
// cursor (a short page terminates paging).
func TestQueryLogPageSinglePage(t *testing.T) {
	base := int64(1_700_000_000_000_000_000)
	bodies := []string{"x", "y", "z"}

	db, err := OpenInMemory()
	if err != nil {
		t.Fatalf("OpenInMemory: %v", err)
	}
	defer db.Close()
	if _, err := db.IngestOTLPLogs(makeLogsRequest(t, "checkout", bodies)); err != nil {
		t.Fatalf("IngestOTLPLogs: %v", err)
	}

	pg, err := db.QueryLogPage(context.Background(), LogQuery{
		Service: "checkout",
		Limit:   10, // exceeds the 3 rows ⇒ short page ⇒ no next cursor
		Start:   base - int64(time.Hour),
		End:     base + int64(time.Hour),
	}, nil)
	if err != nil {
		t.Fatalf("QueryLogPage: %v", err)
	}
	if len(pg.Entries) != len(bodies) {
		t.Fatalf("entries = %d, want %d", len(pg.Entries), len(bodies))
	}
	if pg.Next.HasMore() {
		t.Fatalf("short page returned a next cursor %q, want none", string(pg.Next))
	}
	if pg.Stats.RowsReturned != uint64(len(bodies)) {
		t.Fatalf("Stats.RowsReturned = %d, want %d", pg.Stats.RowsReturned, len(bodies))
	}
}
