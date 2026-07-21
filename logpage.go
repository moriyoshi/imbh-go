package imbhgo

// logpage.go — cursor-paged log queries with scan statistics (binding plan: LogPage cursor paging +
// the scalar-metadata side-channel). Rows stream zero-copy over the same Arrow path as QueryLogsTyped;
// the page's next-cursor and QueryStats — known only after the query is drained — ride back out-of-band
// through a byte-Call keyed by the query id (mirroring the terminal-error slot in db.go).

import (
	"context"
	"encoding/binary"
	"encoding/json"

	"github.com/moriyoshi/sable"
)

// Op ids shared with rust/src/lib.rs.
const (
	opLogPage     uint32 = 31
	opLogPageMeta uint32 = 32
)

// Cursor is an opaque page-resume token. Obtain it from LogPage.Next and pass it back to QueryLogPage
// to fetch the following page (reusing the same filters/limit/direction). Treat it as opaque — do not
// construct or interpret it. The zero (nil) value requests the first page.
type Cursor []byte

// HasMore reports whether a following page exists (i.e. this cursor points somewhere).
func (c Cursor) HasMore() bool { return len(c) > 0 }

// QueryStats reports what a query scanned. Complete only for a fully drained query (which QueryLogPage
// always does). Mirrors imbh's QueryStats.
type QueryStats struct {
	SegmentsScanned uint64 `json:"segments_scanned"`
	SegmentsPruned  uint64 `json:"segments_pruned"`
	RowsScanned     uint64 `json:"rows_scanned"`
	RowsReturned    uint64 `json:"rows_returned"`
	BytesScanned    uint64 `json:"bytes_scanned"`
	ElapsedNs       uint64 `json:"elapsed_ns"`
	UsedIndex       bool   `json:"used_index"`
}

// LogPage is one page of a paged log query: the decoded rows, a resume cursor (empty when the page was
// short, i.e. no more rows), and the scan statistics.
type LogPage struct {
	Entries []LogEntry
	Next    Cursor
	Stats   QueryStats
}

// logPageReq is the JSON request: the flat log-query fields plus the opaque resume cursor.
type logPageReq struct {
	LogQuery
	After json.RawMessage `json:"after,omitempty"`
}

// QueryLogPage runs a single page of a log query. Pass a nil Cursor for the first page; pass the
// returned LogPage.Next (while HasMore) for each subsequent page, keeping the same LogQuery. Rows are
// materialized into []LogEntry (like QueryLogsTyped); the page's cursor and QueryStats come back
// alongside. (imbh: logs().query → LogPage, LogQuery::after.)
func (db *DB) QueryLogPage(ctx context.Context, q LogQuery, after Cursor) (*LogPage, error) {
	body, err := json.Marshal(logPageReq{LogQuery: q, After: json.RawMessage(after)})
	if err != nil {
		return nil, err
	}
	rows, err := db.openStream(ctx, opLogPage, body)
	if err != nil {
		return nil, err
	}
	entries, decErr := decodeLogEntries(rows) // drains + closes the stream, surfacing any query error
	// Always fetch (and thus clear) the page metadata for this query id, even on a decode error, so the
	// Rust-side PAGE_META slot never leaks.
	meta, metaErr := fetchLogPageMeta(rows.queryID)
	if decErr != nil {
		return nil, decErr
	}
	if metaErr != nil {
		return nil, metaErr
	}
	return &LogPage{Entries: entries, Next: meta.next, Stats: meta.stats}, nil
}

type logPageMeta struct {
	next  Cursor
	stats QueryStats
}

// fetchLogPageMeta retrieves and clears the {next, stats} recorded for a drained paged query.
func fetchLogPageMeta(queryID uint64) (logPageMeta, error) {
	req := make([]byte, 8)
	binary.LittleEndian.PutUint64(req, queryID)
	resp, err := sable.Call(opLogPageMeta, req)
	if err != nil {
		return logPageMeta{}, err
	}
	if len(resp) == 0 {
		return logPageMeta{}, nil // no metadata (e.g. the query errored before it ran)
	}
	var w struct {
		Next  json.RawMessage `json:"next"`
		Stats QueryStats      `json:"stats"`
	}
	if err := json.Unmarshal(resp, &w); err != nil {
		return logPageMeta{}, err
	}
	var next Cursor
	// The cursor is carried verbatim (opaque); a JSON null means "no more pages".
	if len(w.Next) > 0 && string(w.Next) != "null" {
		next = Cursor(w.Next)
	}
	return logPageMeta{next: next, stats: w.Stats}, nil
}
