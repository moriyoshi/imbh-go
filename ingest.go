package imbhgo

// ingest.go — OTLP ingest + flush over sable's byte Call path (binding plan M2). Requests are
// [8-byte LE db id][OTLP export-request protobuf]; the receipt (or a Rust error) comes back as bytes.

import (
	"encoding/binary"
	"errors"

	"github.com/moriyoshi/sable"
)

// Op ids shared with rust/src/lib.rs.
const (
	opIngestLogs    uint32 = 2
	opIngestTraces  uint32 = 3
	opIngestMetrics uint32 = 4
	opFlush         uint32 = 5
)

// Receipt is the outcome of an ingest call. When Queued is true (async ingest), LSN/Durable carry no
// information yet.
type Receipt struct {
	Accepted uint64
	Rejected uint64
	LSN      uint64
	Durable  bool
	Queued   bool
}

func decodeReceipt(b []byte) (Receipt, error) {
	if len(b) < 26 {
		return Receipt{}, errors.New("imbhgo: short ingest receipt")
	}
	return Receipt{
		Accepted: binary.LittleEndian.Uint64(b[0:8]),
		Rejected: binary.LittleEndian.Uint64(b[8:16]),
		LSN:      binary.LittleEndian.Uint64(b[16:24]),
		Durable:  b[24] != 0,
		Queued:   b[25] != 0,
	}, nil
}

func (db *DB) ingest(op uint32, otlp []byte) (Receipt, error) {
	req := make([]byte, 8+len(otlp))
	binary.LittleEndian.PutUint64(req[:8], db.id)
	copy(req[8:], otlp)
	resp, err := sable.Call(op, req)
	if err != nil {
		return Receipt{}, err
	}
	return decodeReceipt(resp)
}

// IngestOTLPLogs ingests OTLP/HTTP logs export-request protobuf bytes (what a stock OTel exporter
// sends). Data is queryable immediately, before any Flush.
func (db *DB) IngestOTLPLogs(otlp []byte) (Receipt, error) { return db.ingest(opIngestLogs, otlp) }

// IngestOTLPTraces ingests an OTLP traces export-request.
func (db *DB) IngestOTLPTraces(otlp []byte) (Receipt, error) { return db.ingest(opIngestTraces, otlp) }

// IngestOTLPMetrics ingests an OTLP metrics export-request.
func (db *DB) IngestOTLPMetrics(otlp []byte) (Receipt, error) {
	return db.ingest(opIngestMetrics, otlp)
}

// Flush seals the in-memory buffer into an immutable segment. Not required for queryability (queries
// see the buffer union segments); use it to bound memory or to exercise the on-disk read path.
func (db *DB) Flush() error {
	req := make([]byte, 8)
	binary.LittleEndian.PutUint64(req, db.id)
	_, err := sable.Call(opFlush, req)
	return err
}

// --- Backpressure ----------------------------------------------------------------------------------

// ErrBackpressure is returned by the Try* entry points and by Query when the fused runtime is at its
// in-flight cap (see SetMaxInFlight). No work was admitted; the caller should shed load or retry.
var ErrBackpressure = sable.ErrBackpressure

// SetMaxInFlight caps the number of concurrently in-flight *admitted* operations (0 = unbounded, the
// default). The cap is process-global (one fused runtime) and applies to every admission-controlled
// entry point: TryIngestOTLP* and Query — a live Rows holds a slot until Close, so the
// cap also bounds concurrent open result streams. The blocking IngestOTLP* path is never refused (it
// still counts toward the in-flight gauge). Observe rejections via RuntimeStats().Rejected.
func SetMaxInFlight(max uint64) { sable.SetMaxInFlight(max) }

// Stats is a snapshot of the fused runtime's counters.
type Stats = sable.Stats

// RuntimeStats returns a snapshot of runtime counters (InFlight, Rejected, MaxInFlight, …) for
// backpressure tuning and observability.
func RuntimeStats() Stats { return sable.RuntimeStats() }

func (db *DB) tryIngest(op uint32, otlp []byte) (Receipt, error) {
	req := make([]byte, 8+len(otlp))
	binary.LittleEndian.PutUint64(req[:8], db.id)
	copy(req[8:], otlp)
	resp, err := sable.TryCall(op, req) // ErrBackpressure at cap; else a receipt or a handler error
	if err != nil {
		return Receipt{}, err
	}
	return decodeReceipt(resp)
}

// TryIngestOTLPLogs is IngestOTLPLogs with backpressure: at the in-flight cap it returns
// ErrBackpressure immediately without ingesting (nothing admitted), so a producer can shed load or
// retry with backoff instead of piling on unbounded work.
func (db *DB) TryIngestOTLPLogs(otlp []byte) (Receipt, error) {
	return db.tryIngest(opIngestLogs, otlp)
}

// TryIngestOTLPTraces is IngestOTLPTraces with backpressure.
func (db *DB) TryIngestOTLPTraces(otlp []byte) (Receipt, error) {
	return db.tryIngest(opIngestTraces, otlp)
}

// TryIngestOTLPMetrics is IngestOTLPMetrics with backpressure.
func (db *DB) TryIngestOTLPMetrics(otlp []byte) (Receipt, error) {
	return db.tryIngest(opIngestMetrics, otlp)
}
