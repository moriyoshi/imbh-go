package imbhgo

// admin.go — read-only and options-configured opens (binding plan: admin/lifecycle surface). These
// mirror the direct FFI open path in db.go (imbhgo_open); the ops in ops.go ride the byte-Call path.

/*
#include "imbhgo.h"
#include <stdint.h>
*/
import "C"

import (
	"encoding/json"
	"runtime"
	"unsafe"
)

// OpenReadOnly opens an existing on-disk database at path read-only. It takes no writer lock, so it
// coexists with the single writer process and with other readers; queries see the writer's segments
// unioned with its live WAL tail (near-real-time). Every write returns an error.
//
// Rejected if the writer had its WAL disabled (the reader could then get only seal-interval
// freshness, not near-real-time); use OpenWith with AllowStaleReads to accept that. (imbh:
// Db::open_read_only.)
func OpenReadOnly(path string) (*DB, error) {
	b := []byte(path)
	var p *C.uint8_t
	if len(b) > 0 {
		p = (*C.uint8_t)(unsafe.Pointer(&b[0]))
	}
	errID := queryCtr.Add(1)
	id := uint64(C.imbhgo_open_read_only(p, C.size_t(len(b)), C.uint64_t(errID)))
	runtime.KeepAlive(b)
	if id == 0 {
		return nil, openFailure("open read-only database at "+path, errID)
	}
	return &DB{id: id}, nil
}

// DbOptions configures a durable open, mirroring imbh's DbBuilder. The zero value opens with imbh's
// defaults (equivalent to Open). Only set fields take effect; unrecognized string tags are ignored
// (leaving the default). The host-runtime option variants (async ingest, runtime-driven maintenance),
// which need an explicit tokio runtime handle, are intentionally not exposed here.
type DbOptions struct {
	// Path is the on-disk directory (required).
	Path string `json:"path"`
	// ReadOnly opens as a reader (no writer lock; many may coexist with one writer).
	ReadOnly bool `json:"read_only,omitempty"`
	// AllowStaleReads lets a read-only open accept seal-interval freshness when the writer's WAL is
	// off (otherwise such an open is rejected).
	AllowStaleReads bool `json:"allow_stale_reads,omitempty"`
	// MemoryBudgetBytes caps the in-memory buffer (0 = imbh default, 128 MiB).
	MemoryBudgetBytes uint64 `json:"memory_budget_bytes,omitempty"`
	// Compression selects the segment codec: "none", "lz4", or "zstd" (with ZstdLevel). "" = default.
	Compression string `json:"compression,omitempty"`
	// ZstdLevel is the zstd level used when Compression == "zstd".
	ZstdLevel int32 `json:"zstd_level,omitempty"`
	// WalMode selects the write-ahead-log mode: "off", "always", or "interval" (with WalIntervalNs).
	WalMode string `json:"wal_mode,omitempty"`
	// WalIntervalNs is the flush interval when WalMode == "interval".
	WalIntervalNs int64 `json:"wal_interval_ns,omitempty"`
	// RetentionDays drops data older than N days (0 = keep, unless MaxDiskBytes bounds it).
	RetentionDays uint64 `json:"retention_days,omitempty"`
	// MaxDiskBytes bounds on-disk segment bytes (0 = unbounded).
	MaxDiskBytes uint64 `json:"max_disk_bytes,omitempty"`
	// Refresh controls read-only snapshot refresh: "onquery", "manual", or "ttl" (with RefreshTtlNs).
	Refresh string `json:"refresh,omitempty"`
	// RefreshTtlNs is the refresh TTL when Refresh == "ttl".
	RefreshTtlNs int64 `json:"refresh_ttl_ns,omitempty"`
	// MaintenanceBackgroundNs runs background maintenance on an owned OS thread every N ns (0 = manual).
	// This picks *who* runs the scheduler; Flush picks *when* it seals. imbh's default is manual, so
	// with this at 0 nothing seals (and no WAL fsync timer runs) unless you call Flush/Maintain yourself.
	MaintenanceBackgroundNs int64 `json:"maintenance_background_ns,omitempty"`
	// Flush is imbh's flush-policy spec: comma-separated key=value pairs, or the single word "manual"
	// ("off"/"none"/"never" alias it). Keys are interval/every (duration), buffer/bytes (size, or
	// "budget"/"off"), rows (count), wal (size), idle (duration), and tick (duration); the triggers OR
	// together. E.g. "interval=5s,wal=64MiB". Unlike the other string tags, a malformed spec fails the
	// open rather than falling back to a default. "" leaves imbh's own default, which seals on the
	// MaintenanceBackgroundNs tick and at the memory-budget-derived byte threshold. (imbh 0.2.0:
	// DbBuilder::flush.)
	Flush string `json:"flush,omitempty"`
	// PromoteKeys promotes the given attribute keys to dedicated columns. Reserved names (including
	// "service") are dropped by imbh, and "service.name" needs no promotion: since imbh 0.3.0 both
	// spellings resolve to the built-in service column in every group-by and attribute predicate.
	PromoteKeys []string `json:"promote_keys,omitempty"`
}

// OpenWith opens a durable database configured by opts (imbh: Db::builder(path) + setters).
func OpenWith(opts DbOptions) (*DB, error) {
	b, err := json.Marshal(opts)
	if err != nil {
		return nil, err
	}
	var p *C.uint8_t
	if len(b) > 0 {
		p = (*C.uint8_t)(unsafe.Pointer(&b[0]))
	}
	errID := queryCtr.Add(1)
	id := uint64(C.imbhgo_open_opts(p, C.size_t(len(b)), C.uint64_t(errID)))
	runtime.KeepAlive(b)
	if id == 0 {
		return nil, openFailure("open database at "+opts.Path, errID)
	}
	return &DB{id: id}, nil
}
