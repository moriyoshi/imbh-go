package imbhgo

// results.go — the Go-side Arrow→struct decoder (binding plan: typed results as a decode over the
// existing zero-copy Arrow path, not a second transport). Typed query methods run the same Rows
// stream and decode each batch's columns into Go structs, tolerating IMBH's column encodings
// (Dictionary(Utf8) for service/resource/scope, Timestamp for time, FixedSizeBinary for ids).

import (
	"context"
	"strconv"
	"strings"

	"github.com/apache/arrow-go/v18/arrow"
	"github.com/apache/arrow-go/v18/arrow/array"
)

// LogEntry is a decoded log record (a curated subset of IMBH's logs columns).
type LogEntry struct {
	Time         int64  // event time, unix nanoseconds
	Service      string // service.name (may be "")
	Severity     uint8  // OTLP severity number
	SeverityText string
	Body         string
	Attributes   string // attributes as a JSON object string
	TraceID      []byte // 16 bytes, or nil
	SpanID       []byte // 8 bytes, or nil
}

// QueryLogsTyped runs a typed log query and decodes the result rows into []LogEntry. Convenience over
// QueryLogs for callers that want Go structs rather than raw Arrow batches. Prefer QueryLogs (+ manual
// Arrow) for very large results, since this materializes all rows.
func (db *DB) QueryLogsTyped(ctx context.Context, q LogQuery) ([]LogEntry, error) {
	rows, err := db.QueryLogs(ctx, q)
	if err != nil {
		return nil, err
	}
	return decodeLogEntries(rows)
}

// decodeLogEntries decodes rows of IMBH's logs projection into []LogEntry. Shared by the typed log
// query and the LogQL line path (a bare LogQL selector returns the same log columns).
func decodeLogEntries(rows *Rows) ([]LogEntry, error) {
	defer rows.Close()
	var out []LogEntry
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
			out = append(out, LogEntry{
				Time:         int64At(cols["time"], i),
				Service:      stringAt(cols["service"], i),
				Severity:     uint8(int64At(cols["severity_number"], i)),
				SeverityText: stringAt(cols["severity_text"], i),
				Body:         stringAt(cols["body"], i),
				Attributes:   stringAt(cols["attributes"], i),
				TraceID:      bytesAt(cols["trace_id"], i),
				SpanID:       bytesAt(cols["span_id"], i),
			})
		}
		rec.Release()
	}
	return out, nil
}

// columnsByName maps each column name to its Arrow array (nil-safe lookups downstream).
func columnsByName(rec arrow.RecordBatch) map[string]arrow.Array {
	schema := rec.Schema()
	m := make(map[string]arrow.Array, schema.NumFields())
	for i := 0; i < schema.NumFields(); i++ {
		m[schema.Field(i).Name] = rec.Column(i)
	}
	return m
}

// stringAt reads a string from column `col` at row `i`, unwrapping Dictionary(Utf8) encodings. Returns
// "" for a nil column or null value.
//
// IMPORTANT: arrow-go's String.Value(i) returns a string that *aliases* the Arrow value buffer
// (zero-copy, via unsafe). Since the caller Release()s the batch after decoding — freeing the
// IMBH-side buffers — every returned string must be copied out with strings.Clone, or it becomes a
// dangling use-after-free.
func stringAt(col arrow.Array, i int) string {
	if col == nil || col.IsNull(i) {
		return ""
	}
	switch a := col.(type) {
	case *array.String:
		return strings.Clone(a.Value(i))
	case *array.LargeString:
		return strings.Clone(a.Value(i))
	case *array.StringView:
		return strings.Clone(a.Value(i))
	case *array.Dictionary:
		if vals, ok := a.Dictionary().(*array.String); ok {
			return strings.Clone(vals.Value(a.GetValueIndex(i)))
		}
	}
	return ""
}

// stringFromArray reads element `j` of a plain string array, handling both the compact (`*array.String`)
// and view (`*array.StringView`) encodings arrow-go can present over the C Data Interface. The returned
// string is copied out with strings.Clone since view/value bytes alias the (soon-Released) Arrow buffer.
func stringFromArray(arr arrow.Array, j int) string {
	if arr == nil || arr.IsNull(j) {
		return ""
	}
	switch a := arr.(type) {
	case *array.String:
		return strings.Clone(a.Value(j))
	case *array.LargeString:
		return strings.Clone(a.Value(j))
	case *array.StringView:
		return strings.Clone(a.Value(j))
	}
	return ""
}

// mapStringAt reads a Map<Utf8*,Utf8*> column at row `i` into a map[string]string. Returns an empty
// (non-nil) map for a nil column or null value.
func mapStringAt(col arrow.Array, i int) map[string]string {
	out := map[string]string{}
	m, ok := col.(*array.Map)
	if !ok || col == nil || col.IsNull(i) {
		return out
	}
	offsets := m.Offsets()
	start, end := int(offsets[i]), int(offsets[i+1])
	keys := m.Keys()
	items := m.Items()
	for j := start; j < end; j++ {
		out[stringFromArray(keys, j)] = stringFromArray(items, j)
	}
	return out
}

// stringListAt reads a List<Utf8*> column at row `i` into a []string. Returns nil for a nil column,
// null value, or empty list.
func stringListAt(col arrow.Array, i int) []string {
	l, ok := col.(*array.List)
	if !ok || col == nil || col.IsNull(i) {
		return nil
	}
	offsets := l.Offsets()
	start, end := int(offsets[i]), int(offsets[i+1])
	if end <= start {
		return nil
	}
	vals := l.ListValues()
	out := make([]string, 0, end-start)
	for j := start; j < end; j++ {
		out = append(out, stringFromArray(vals, j))
	}
	return out
}

// int64At reads an integer/timestamp column as int64. Returns 0 for a nil column or null value.
func int64At(col arrow.Array, i int) int64 {
	if col == nil || col.IsNull(i) {
		return 0
	}
	switch a := col.(type) {
	case *array.Int64:
		return a.Value(i)
	case *array.Timestamp:
		return int64(a.Value(i))
	case *array.Uint8:
		return int64(a.Value(i))
	case *array.Uint32:
		return int64(a.Value(i))
	case *array.Uint64:
		return int64(a.Value(i))
	}
	return 0
}

// boolAt reads a boolean column at row `i`. Returns false for a nil column or null value.
func boolAt(col arrow.Array, i int) bool {
	if col == nil || col.IsNull(i) {
		return false
	}
	if a, ok := col.(*array.Boolean); ok {
		return a.Value(i)
	}
	return false
}

// bytesAt reads a binary column (e.g. FixedSizeBinary ids) as a copy. Returns nil for a nil column or
// null value.
func bytesAt(col arrow.Array, i int) []byte {
	if col == nil || col.IsNull(i) {
		return nil
	}
	switch a := col.(type) {
	case *array.FixedSizeBinary:
		return append([]byte(nil), a.Value(i)...)
	case *array.Binary:
		return append([]byte(nil), a.Value(i)...)
	}
	return nil
}

// float64At reads a floating/integer column as float64. Returns 0 for a nil column or null value.
func float64At(col arrow.Array, i int) float64 {
	if col == nil || col.IsNull(i) {
		return 0
	}
	switch a := col.(type) {
	case *array.Float64:
		return a.Value(i)
	case *array.Float32:
		return float64(a.Value(i))
	case *array.Int64:
		return float64(a.Value(i))
	case *array.Uint64:
		return float64(a.Value(i))
	}
	return 0
}

// --- Metric range (Matrix) -------------------------------------------------------------------------

// Point is one (time, value) sample. T is unix nanoseconds.
type Point struct {
	T int64
	V float64
}

// Series is one metric time series: its label set and its samples over time.
type Series struct {
	Labels map[string]string
	Points []Point
}

// Matrix is the result of a metric range query — one series per distinct group-by label set.
type Matrix struct {
	Series []Series
}

// labelsAt reads the `g0..gN` group columns for row `i` into a label map (keyed by the query's GroupBy
// names, in order) and a stable key string for grouping.
func labelsAt(cols map[string]arrow.Array, keys []string, i int) (map[string]string, string) {
	labels := make(map[string]string, len(keys))
	var b strings.Builder
	for gi, key := range keys {
		v := stringAt(cols["g"+strconv.Itoa(gi)], i)
		labels[key] = v
		b.WriteString(v)
		b.WriteByte(0)
	}
	return labels, b.String()
}

// QueryMetricsTyped runs a metric range query and decodes it into a Matrix (rows grouped into series by
// the GroupBy label set). Convenience over QueryMetrics.
func (db *DB) QueryMetricsTyped(ctx context.Context, q MetricQuery) (Matrix, error) {
	rows, err := db.QueryMetrics(ctx, q)
	if err != nil {
		return Matrix{}, err
	}
	defer rows.Close()
	var m Matrix
	index := map[string]int{} // label-key → series index
	for {
		rec, ok, err := rows.Next()
		if err != nil {
			return Matrix{}, err
		}
		if !ok {
			break
		}
		cols := columnsByName(rec)
		n := int(rec.NumRows())
		for i := 0; i < n; i++ {
			labels, key := labelsAt(cols, q.GroupBy, i)
			si, seen := index[key]
			if !seen {
				si = len(m.Series)
				index[key] = si
				m.Series = append(m.Series, Series{Labels: labels})
			}
			m.Series[si].Points = append(m.Series[si].Points, Point{
				T: int64At(cols["bucket"], i),
				V: float64At(cols["v"], i),
			})
		}
		rec.Release()
	}
	return m, nil
}

// --- Span (RED) metrics ----------------------------------------------------------------------------

// SpanMetricPoint is one bucket of RED span metrics for a label set.
type SpanMetricPoint struct {
	Bucket int64 // bucket start, unix nanoseconds
	Labels map[string]string
	Calls  uint64
	Errors uint64
	P50    float64 // latency percentiles, nanoseconds
	P95    float64
	P99    float64
}

// QuerySpanMetricsTyped runs a span (RED) metrics query and decodes the rows into []SpanMetricPoint.
func (db *DB) QuerySpanMetricsTyped(ctx context.Context, q SpanMetricsQuery) ([]SpanMetricPoint, error) {
	rows, err := db.QuerySpanMetrics(ctx, q)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []SpanMetricPoint
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
			labels, _ := labelsAt(cols, q.GroupBy, i)
			out = append(out, SpanMetricPoint{
				Bucket: int64At(cols["bucket"], i),
				Labels: labels,
				Calls:  uint64(int64At(cols["calls"], i)),
				Errors: uint64(int64At(cols["errors"], i)),
				P50:    float64At(cols["p50"], i),
				P95:    float64At(cols["p95"], i),
				P99:    float64At(cols["p99"], i),
			})
		}
		rec.Release()
	}
	return out, nil
}
