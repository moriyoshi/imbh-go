package imbhgo

// ops.go — admin / lifecycle passthrough over sable's byte-Call path (binding plan: ops passthrough).
// Each op is [8-byte LE db id][JSON args]; the reply is JSON of a flat result struct, except Export,
// which returns raw Arrow-IPC stream bytes. imbh's own return structs carry no serde, so the Rust glue
// mirrors them field-by-field (rust/src/lib.rs).

import (
	"bytes"
	"encoding/binary"
	"encoding/json"

	"github.com/apache/arrow-go/v18/arrow"
	"github.com/apache/arrow-go/v18/arrow/ipc"
	"github.com/moriyoshi/sable"
)

// Op ids shared with rust/src/lib.rs.
const (
	opStats          uint32 = 23
	opMaintain       uint32 = 24
	opCompact        uint32 = 25
	opSnapshot       uint32 = 26
	opSegments       uint32 = 27
	opSegmentFiles   uint32 = 28
	opDurableThrough uint32 = 29
	opExport         uint32 = 30
)

// Table names one of imbh's storage tables (logs, spans, and the five metric families). The string
// values match imbh's Table::as_str form used on the wire.
type Table string

const (
	TableLogs                Table = "logs"
	TableSpans               Table = "spans"
	TableMetricsGauge        Table = "metrics_gauge"
	TableMetricsSum          Table = "metrics_sum"
	TableMetricsHistogram    Table = "metrics_histogram"
	TableMetricsExpHistogram Table = "metrics_exp_histogram"
	TableMetricsSummary      Table = "metrics_summary"
)

// callOp frames [db id][JSON args] and runs the byte-Call op. args may be nil for the no-arg ops.
func (db *DB) callOp(op uint32, args any) ([]byte, error) {
	var body []byte
	if args != nil {
		var err error
		body, err = json.Marshal(args)
		if err != nil {
			return nil, err
		}
	}
	req := make([]byte, 8+len(body))
	binary.LittleEndian.PutUint64(req[:8], db.id)
	copy(req[8:], body)
	return sable.Call(op, req)
}

// callOpJSON runs an op and unmarshals its JSON reply into out.
func (db *DB) callOpJSON(op uint32, args any, out any) error {
	resp, err := db.callOp(op, args)
	if err != nil {
		return err
	}
	return json.Unmarshal(resp, out)
}

// TableStats is per-table storage accounting within DbStats.
type TableStats struct {
	Table           string `json:"table"`
	SegmentCount    uint64 `json:"segment_count"`
	SegmentRows     uint64 `json:"segment_rows"`
	BufferRows      uint64 `json:"buffer_rows"`
	MinTimeUnixNano *int64 `json:"min_time_unix_nano"`
	MaxTimeUnixNano *int64 `json:"max_time_unix_nano"`
}

// DbStats is a snapshot of the database's storage and ingest counters (imbh: Db::stats).
type DbStats struct {
	Tables           []TableStats `json:"tables"`
	BufferBytes      uint64       `json:"buffer_bytes"`
	WalBytes         uint64       `json:"wal_bytes"`
	DurableLSN       *uint64      `json:"durable_lsn"`
	IngestQueueDepth uint64       `json:"ingest_queue_depth"`
	IngestDropped    uint64       `json:"ingest_dropped"`
	IngestErrors     uint64       `json:"ingest_errors"`
	// IngestRejected counts metric points dropped at ingest because their (series, timestamp) was
	// already accepted. Non-zero only under DbOptions.Duplicates == "reject"; every other policy
	// takes duplicates and resolves them (or fails) at read time. (imbh 0.5.0.)
	IngestRejected uint64 `json:"ingest_rejected"`
}

// Stats returns a snapshot of storage and ingest counters. Works on readers and writers.
func (db *DB) Stats() (DbStats, error) {
	var s DbStats
	err := db.callOpJSON(opStats, nil, &s)
	return s, err
}

// MaintenanceReport summarizes a maintain() pass (imbh: Db::maintain).
type MaintenanceReport struct {
	Sealed          bool   `json:"sealed"`
	SegmentsDropped uint64 `json:"segments_dropped"`
	BytesFreed      uint64 `json:"bytes_freed"`
}

// Maintain runs a maintenance pass (seal + retention enforcement). Writer-only; a read-only handle
// returns an error.
func (db *DB) Maintain() (MaintenanceReport, error) {
	var r MaintenanceReport
	err := db.callOpJSON(opMaintain, nil, &r)
	return r, err
}

// CompactionReport summarizes a compact() pass (imbh: Db::compact).
type CompactionReport struct {
	SegmentsMerged  uint64 `json:"segments_merged"`
	SegmentsCreated uint64 `json:"segments_created"`
}

// Compact merges segments to reduce fragmentation. Writer-only.
func (db *DB) Compact() (CompactionReport, error) {
	var r CompactionReport
	err := db.callOpJSON(opCompact, nil, &r)
	return r, err
}

// SnapshotInfo describes a snapshot written by Snapshot (imbh: Db::snapshot).
type SnapshotInfo struct {
	Dir      string `json:"dir"`
	Segments uint64 `json:"segments"`
}

// Snapshot writes a consistent copy of the database's segments into dir (created if absent).
// Writer-only.
func (db *DB) Snapshot(dir string) (SnapshotInfo, error) {
	var info SnapshotInfo
	err := db.callOpJSON(opSnapshot, map[string]string{"dir": dir}, &info)
	return info, err
}

// SegmentRef identifies one on-disk segment and its covered time range (imbh: Db::segments).
type SegmentRef struct {
	RelativePath    string `json:"relative_path"`
	MinTimeUnixNano int64  `json:"min_time_unix_nano"`
	MaxTimeUnixNano int64  `json:"max_time_unix_nano"`
	Rows            uint64 `json:"rows"`
}

// Segments lists the database's current on-disk segments.
func (db *DB) Segments() ([]SegmentRef, error) {
	var segs []SegmentRef
	err := db.callOpJSON(opSegments, nil, &segs)
	return segs, err
}

// SegmentFiles lists the on-disk file paths backing the given table (imbh: Db::segment_files).
func (db *DB) SegmentFiles(table Table) ([]string, error) {
	var files []string
	err := db.callOpJSON(opSegmentFiles, map[string]string{"table": string(table)}, &files)
	return files, err
}

// DurableThrough returns the highest LSN durably persisted, or (0, false) if nothing is durable yet
// (imbh: Db::durable_through).
func (db *DB) DurableThrough() (uint64, bool, error) {
	var w struct {
		DurableLSN *uint64 `json:"durable_lsn"`
	}
	if err := db.callOpJSON(opDurableThrough, nil, &w); err != nil {
		return 0, false, err
	}
	if w.DurableLSN == nil {
		return 0, false, nil
	}
	return *w.DurableLSN, true, nil
}

// Export returns the given table's rows over [startNs, endNs) as an Arrow-IPC stream (a self-describing
// schema + record batches), buffer unioned with segments ordered by time. Pass startNs == endNs == 0
// for the whole range. Use ExportRecords to decode. (imbh: Db::export.)
func (db *DB) Export(table Table, startNs, endNs int64) ([]byte, error) {
	return db.callOp(opExport, map[string]any{"table": string(table), "start": startNs, "end": endNs})
}

// ExportRecords is Export decoded into Arrow record batches. Each returned batch is Retained and owned
// by the caller — Release() it when done.
func (db *DB) ExportRecords(table Table, startNs, endNs int64) ([]arrow.RecordBatch, error) {
	raw, err := db.Export(table, startNs, endNs)
	if err != nil {
		return nil, err
	}
	r, err := ipc.NewReader(bytes.NewReader(raw))
	if err != nil {
		return nil, err
	}
	defer r.Release()
	var recs []arrow.RecordBatch
	for r.Next() {
		rec := r.Record()
		rec.Retain() // reader reuses/releases on the next Next(); keep our own reference
		recs = append(recs, rec)
	}
	if err := r.Err(); err != nil {
		for _, rec := range recs {
			rec.Release()
		}
		return nil, err
	}
	return recs, nil
}
