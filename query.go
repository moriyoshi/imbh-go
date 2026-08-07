package imbhgo

// query.go — typed observability queries (binding plan M1). These are native Go query structs marshalled
// to JSON and mapped, Rust-side, onto IMBH's own typed builders (LogQuery / MetricQuery), then run via
// its `*_batches` APIs and streamed back zero-copy — the same Rows/Arrow path as SQL. Only the
// Arrow-shaped typed queries are exposed here; typed-struct results (LogPage, Matrix, histogram
// quantiles, a single Trace) are a separate, later surface.
//
// Note on memory: unlike Query(sql) (which streams lazily), these use IMBH's eager `*_batches` collect,
// so they materialize the result before streaming it out. Prefer them for bounded queries (a limited
// page, a fixed-step metric range); use SQL for unbounded scans.

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"

	"github.com/moriyoshi/sable"
)

// Op ids shared with rust/src/lib.rs.
const (
	opQueryLogs        uint32 = 7
	opQueryMetrics     uint32 = 8
	opQuerySpanMetrics uint32 = 9
)

// LogQuery is an endpoint-shaped log query (mirrors IMBH's LogQuery builder; a curated subset). The
// zero value matches all logs. Times are Unix nanoseconds; 0 means unset.
type LogQuery struct {
	Service  string            `json:"service,omitempty"`  // exact service.name match
	Match    string            `json:"match,omitempty"`    // full-text match on the log body
	AttrEq   map[string]string `json:"attr_eq,omitempty"`  // attribute equality filters (AND)
	Start    int64             `json:"start,omitempty"`    // time range start (unix nanos, inclusive)
	End      int64             `json:"end,omitempty"`      // time range end (unix nanos)
	Limit    int               `json:"limit,omitempty"`    // max rows (0 = engine default)
	Backward bool              `json:"backward,omitempty"` // newest-first (default is oldest-first)

	// Trace correlation: filter logs down to a single trace or span.
	TraceID string `json:"trace_id,omitempty"` // hex trace id (32 hex chars); correlate logs to a trace
	SpanID  string `json:"span_id,omitempty"`  // hex span id (16 hex chars); correlate to a single span

	// Severity + attribute predicates (all AND-combined with the above).
	SeverityAtLeast int                 `json:"severity_at_least,omitempty"` // minimum OTEL severity number (1-24); 0 = unset
	AttrExists      []string            `json:"attr_exists,omitempty"`       // keys that must be present
	AttrMatches     map[string]string   `json:"attr_matches,omitempty"`      // key → full-text term match on that attribute
	AttrIn          map[string][]string `json:"attr_in,omitempty"`           // key → allowed value set
	AttrNotIn       map[string][]string `json:"attr_not_in,omitempty"`       // key → excluded value set
	AttrGt          map[string]float64  `json:"attr_gt,omitempty"`           // key → value must be > n
	AttrGe          map[string]float64  `json:"attr_ge,omitempty"`           // key → value must be >= n
	AttrLt          map[string]float64  `json:"attr_lt,omitempty"`           // key → value must be < n
	AttrLe          map[string]float64  `json:"attr_le,omitempty"`           // key → value must be <= n
	AttrRegex       map[string]string   `json:"attr_regex,omitempty"`        // key → RE2 pattern the value must match

	// Arrival (observed-time) axis — see LogOrder. Orthogonal to Start/End, which bound event time.
	ObservedAfter int64    `json:"observed_after,omitempty"` // keep only records with observed_time > t (unix nanos); 0 = unset
	OrderBy       LogOrder `json:"order_by,omitempty"`       // time axis to ORDER BY; "" = LogOrderTime
}

// LogOrder selects which of a record's two instants a log query orders by, independently of
// LogQuery.Backward (which picks the direction along that axis). A record carries both: Time is when
// the event happened, ObservedTime is when ingest received it. They differ by up to one batch interval
// always, and by more whenever a record's own timestamp is trusted.
//
// LogOrderObservedTime is what a tailer wants. Arrival order is monotone in the order rows became
// visible, so a watermark over it cannot be overtaken by a late-arriving record with an older event
// time — which is precisely how a follow loop on the event clock drops lines. Records with no
// observed_time sort last in either direction, and ObservedAfter never matches them (SQL NULL > t is
// unknown), so they are left out of a follow loop rather than replayed on every poll.
type LogOrder string

const (
	LogOrderTime         LogOrder = "time"          // order by event time (the default)
	LogOrderObservedTime LogOrder = "observed_time" // order by arrival time, NULLs last
)

// QueryLogs runs a typed log query, returning a zero-copy streamed result set (see Rows).
func (db *DB) QueryLogs(ctx context.Context, q LogQuery) (*Rows, error) {
	b, err := json.Marshal(q)
	if err != nil {
		return nil, err
	}
	return db.openStream(ctx, opQueryLogs, b)
}

// opLogCount is a byte-Call op (not a stream): [db id][JSON LogQuery] → 8-byte LE count. Shared with
// rust/src/lib.rs.
const opLogCount uint32 = 33

// CountLogs returns the number of log records matching q — imbh's logs().count(filter), a full
// count(*) over the filter that ignores q.Limit and q.Backward (they bound/order returned rows, not
// the total). It scans without materializing rows, so it is cheaper than draining QueryLogs when you
// only need the tally. Equivalent to SELECT count(*) via Query, but driven by the same typed LogQuery.
func (db *DB) CountLogs(ctx context.Context, q LogQuery) (uint64, error) {
	b, err := json.Marshal(q)
	if err != nil {
		return 0, err
	}
	req := make([]byte, 8+len(b))
	binary.LittleEndian.PutUint64(req[:8], db.id)
	copy(req[8:], b)
	resp, err := sable.CallCtx(ctx, opLogCount, req)
	if err != nil {
		return 0, err
	}
	if len(resp) < 8 {
		return 0, errors.New("imbhgo: short count reply")
	}
	return binary.LittleEndian.Uint64(resp[:8]), nil
}

// MetricQuery is a metric range query over a scalar metric (gauge or sum). Times are Unix nanoseconds;
// Step is the resampling interval in nanoseconds. GroupBy names attribute keys to split series on —
// a record attribute, or the service under either spelling ("service.name" / "service"), which imbh
// 0.3.0 resolves to the built-in service column (earlier versions merged every service into one
// empty-labelled series). The same holds for LogQuery's attribute predicates and SpanMetricsQuery.
type MetricQuery struct {
	Metric  string   `json:"metric"`             // metric name
	Sum     bool     `json:"sum,omitempty"`      // false = gauge (default), true = sum
	Step    int64    `json:"step,omitempty"`     // resample step (nanos)
	Start   int64    `json:"start,omitempty"`    // range start (unix nanos)
	End     int64    `json:"end,omitempty"`      // range end (unix nanos)
	GroupBy []string `json:"group_by,omitempty"` // attribute keys to split series on
}

// QueryMetrics runs a typed metric range query, returning a zero-copy streamed result set.
func (db *DB) QueryMetrics(ctx context.Context, q MetricQuery) (*Rows, error) {
	b, err := json.Marshal(q)
	if err != nil {
		return nil, err
	}
	return db.openStream(ctx, opQueryMetrics, b)
}

// --- Instant metric query ---------------------------------------------------------------------------

const opMetricInstant uint32 = 22

// InstantSample is one series' instant value: the last sample of that series over the query range
// (Vector semantics — exactly one sample per series). Labels is the canonical JSON label-set string;
// Time is unix nanoseconds.
type InstantSample struct {
	Labels string
	Time   int64
	Value  float64
}

// QueryMetricInstant runs an instant metric query (imbh's `metrics().instant`) over the same
// MetricQuery as QueryMetrics, returning one InstantSample per series (the last point in range).
func (db *DB) QueryMetricInstant(ctx context.Context, q MetricQuery) ([]InstantSample, error) {
	b, err := json.Marshal(q)
	if err != nil {
		return nil, err
	}
	rows, err := db.openStream(ctx, opMetricInstant, b)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []InstantSample
	for {
		rec, ok, err := rows.Next()
		if err != nil {
			return nil, err
		}
		if !ok {
			break
		}
		cols := columnsByName(rec)
		n := int(rec.NumRows())
		for i := 0; i < n; i++ {
			out = append(out, InstantSample{
				Labels: stringAt(cols["labels"], i),
				Time:   int64At(cols["timestamp"], i),
				Value:  float64At(cols["value"], i),
			})
		}
		rec.Release()
	}
	return out, nil
}

// SpanMetricsQuery is a span (RED) metrics query: calls / errors / latency percentiles over spans,
// bucketed by Step (nanos) and optionally split by GroupBy attribute keys.
type SpanMetricsQuery struct {
	Service string   `json:"service,omitempty"`
	Name    string   `json:"name,omitempty"`     // span name
	Kind    string   `json:"kind,omitempty"`     // span kind
	Status  string   `json:"status,omitempty"`   // status code filter
	GroupBy []string `json:"group_by,omitempty"` // attribute keys to split on
	Step    int64    `json:"step,omitempty"`     // bucket width (nanos)
	Start   int64    `json:"start,omitempty"`
	End     int64    `json:"end,omitempty"`
}

// QuerySpanMetrics runs a span (RED) metrics query, returning a zero-copy streamed result set with
// columns bucket, [group labels], calls, errors, p50, p95, p99.
func (db *DB) QuerySpanMetrics(ctx context.Context, q SpanMetricsQuery) (*Rows, error) {
	b, err := json.Marshal(q)
	if err != nil {
		return nil, err
	}
	return db.openStream(ctx, opQuerySpanMetrics, b)
}

// --- Raw metric samples -----------------------------------------------------------------------------

const opMetricPoints uint32 = 14

// MetricPointsQuery selects raw (unaggregated) metric samples — the counterpart to MetricQuery, which
// resamples into a range. Kind is "gauge" (default), "sum", or "histogram". Times are Unix nanoseconds.
type MetricPointsQuery struct {
	Metric  string            `json:"metric"`
	Kind    string            `json:"kind,omitempty"`
	Filters map[string]string `json:"filters,omitempty"` // attribute equality filters (AND)
	Start   int64             `json:"start,omitempty"`
	End     int64             `json:"end,omitempty"`
	Limit   int               `json:"limit,omitempty"`
}

// QueryMetricPoints returns raw metric samples as zero-copy Arrow rows. Columns: point_time, metric,
// service, attributes, temporality, is_monotonic, then value (scalar kinds) or explicit_bounds +
// bucket_counts (histogram).
func (db *DB) QueryMetricPoints(ctx context.Context, q MetricPointsQuery) (*Rows, error) {
	b, err := json.Marshal(q)
	if err != nil {
		return nil, err
	}
	return db.openStream(ctx, opMetricPoints, b)
}

// MetricPoint is one decoded raw metric sample (scalar kinds: gauge / sum).
type MetricPoint struct {
	Time       int64 // unix nanoseconds
	Metric     string
	Service    string
	Attributes string // JSON object string
	Value      float64
}

// QueryMetricPointsTyped decodes raw scalar (gauge/sum) samples into []MetricPoint. For histogram
// metrics use QueryMetricPoints and read the bucket columns directly.
func (db *DB) QueryMetricPointsTyped(ctx context.Context, q MetricPointsQuery) ([]MetricPoint, error) {
	rows, err := db.QueryMetricPoints(ctx, q)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []MetricPoint
	for {
		rec, ok, err := rows.Next()
		if err != nil {
			return nil, err
		}
		if !ok {
			break
		}
		cols := columnsByName(rec)
		n := int(rec.NumRows())
		for i := 0; i < n; i++ {
			out = append(out, MetricPoint{
				Time:       int64At(cols["point_time"], i),
				Metric:     stringAt(cols["metric"], i),
				Service:    stringAt(cols["service"], i),
				Attributes: stringAt(cols["attributes"], i),
				Value:      float64At(cols["value"], i),
			})
		}
		rec.Release()
	}
	return out, nil
}
