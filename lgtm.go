package imbhgo

// lgtm.go — LGTM-stack query languages (PromQL / LogQL / TraceQL). The query text is parsed and
// executed by IMBH's imbh-lgtm layer, and the evaluated result is mapped to Arrow and streamed back
// on the same zero-copy Rows path as SQL and typed queries. PromQL/LogQL yield labeled series
// (columns labels|timestamp|value); TraceQL yields trace_id|span_id matches.

import (
	"context"
	"encoding/json"
	"sort"
	"strings"
)

// Op ids shared with rust/src/lib.rs.
const (
	opPromQL  uint32 = 10
	opLogQL   uint32 = 11
	opTraceQL uint32 = 12
)

type lgtmReq struct {
	Query string `json:"query"`
	Start int64  `json:"start"`
	End   int64  `json:"end"`
	Step  int64  `json:"step"`
	Limit int64  `json:"limit,omitempty"`
}

func (db *DB) lgtmStream(ctx context.Context, op uint32, query string, start, end, step int64) (*Rows, error) {
	return db.lgtmStreamLimit(ctx, op, query, start, end, step, 0)
}

func (db *DB) lgtmStreamLimit(ctx context.Context, op uint32, query string, start, end, step, limit int64) (*Rows, error) {
	payload, err := json.Marshal(lgtmReq{Query: query, Start: start, End: end, Step: step, Limit: limit})
	if err != nil {
		return nil, err
	}
	return db.openStream(ctx, op, payload)
}

// --- PromQL ----------------------------------------------------------------------------------------

// QueryPromQL evaluates a PromQL query over [start, end] at the given step (all unix nanoseconds),
// returning the result as zero-copy Arrow rows (columns labels | timestamp | value).
func (db *DB) QueryPromQL(ctx context.Context, query string, start, end, step int64) (*Rows, error) {
	return db.lgtmStream(ctx, opPromQL, query, start, end, step)
}

// QueryPromQLSeries evaluates a PromQL query and decodes the result into labeled series.
func (db *DB) QueryPromQLSeries(ctx context.Context, query string, start, end, step int64) ([]Series, error) {
	rows, err := db.QueryPromQL(ctx, query, start, end, step)
	if err != nil {
		return nil, err
	}
	return decodeLabeledSeries(rows)
}

// --- LogQL -----------------------------------------------------------------------------------------

// QueryLogQL evaluates a LogQL query over [start, end] at step, returning zero-copy Arrow rows. LogQL
// has two result shapes (as in Loki): a range aggregation (e.g. count_over_time, rate) yields labeled
// series (labels|timestamp|value), while a bare selector yields log lines (the logs projection). See
// QueryLogQLSeries and QueryLogQLLines for the decoded forms.
func (db *DB) QueryLogQL(ctx context.Context, query string, start, end, step int64) (*Rows, error) {
	return db.lgtmStream(ctx, opLogQL, query, start, end, step)
}

// QueryLogQLLines evaluates a bare LogQL selector (e.g. `{service="checkout"} |= "error"`) and decodes
// the matching log lines. This is LogQL's `streams` result shape, as opposed to the `matrix` shape a
// range aggregation produces (see QueryLogQLSeries). limit caps the lines returned (0 = engine default).
func (db *DB) QueryLogQLLines(ctx context.Context, query string, start, end int64, limit int) ([]LogEntry, error) {
	rows, err := db.lgtmStreamLimit(ctx, opLogQL, query, start, end, 0, int64(limit))
	if err != nil {
		return nil, err
	}
	return decodeLogEntries(rows)
}

// QueryLogQLSeries evaluates a LogQL range aggregation and decodes it into labeled series.
func (db *DB) QueryLogQLSeries(ctx context.Context, query string, start, end, step int64) ([]Series, error) {
	rows, err := db.QueryLogQL(ctx, query, start, end, step)
	if err != nil {
		return nil, err
	}
	return decodeLabeledSeries(rows)
}

// --- TraceQL ---------------------------------------------------------------------------------------

// TraceMatch is a TraceQL match: a trace and the span ids its spanset selected.
type TraceMatch struct {
	TraceID string
	SpanIDs []string
}

// QueryTraceQL evaluates a TraceQL query over the trace-start window [start, end] (unix nanoseconds),
// returning matches as zero-copy Arrow rows (columns trace_id | span_id).
func (db *DB) QueryTraceQL(ctx context.Context, query string, start, end int64) (*Rows, error) {
	return db.lgtmStream(ctx, opTraceQL, query, start, end, 0)
}

// QueryTraceQLMatches evaluates a TraceQL query and decodes the matches into []TraceMatch.
func (db *DB) QueryTraceQLMatches(ctx context.Context, query string, start, end int64) ([]TraceMatch, error) {
	rows, err := db.QueryTraceQL(ctx, query, start, end)
	if err != nil {
		return nil, err
	}
	return decodeTraceMatches(rows)
}

// --- decoders --------------------------------------------------------------------------------------

// decodeLabeledSeries groups labels|ts|value rows (one sample per row, labels repeated) into series
// keyed by a stable derivation of the Map<Utf8View,Utf8View> label column.
func decodeLabeledSeries(rows *Rows) ([]Series, error) {
	defer rows.Close()
	index := map[string]int{}
	var out []Series
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
			m := mapStringAt(cols["labels"], i)
			key := seriesKey(m)
			si, seen := index[key]
			if !seen {
				si = len(out)
				index[key] = si
				out = append(out, Series{Labels: m})
			}
			out[si].Points = append(out[si].Points, Point{
				T: int64At(cols["ts"], i),
				V: float64At(cols["value"], i),
			})
		}
		rec.Release()
	}
	return out, nil
}

// seriesKey builds a stable grouping key from a label map (keys sorted, joined k\x00v\x00).
func seriesKey(m map[string]string) string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var b strings.Builder
	for _, k := range keys {
		b.WriteString(k)
		b.WriteByte(0)
		b.WriteString(m[k])
		b.WriteByte(0)
	}
	return b.String()
}

// decodeTraceMatches decodes trace_id|span_ids rows (one row per matched trace) into []TraceMatch.
// An empty span_ids list surfaces as a TraceMatch with nil SpanIDs (the empty-spanset case).
func decodeTraceMatches(rows *Rows) ([]TraceMatch, error) {
	defer rows.Close()
	var out []TraceMatch
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
			out = append(out, TraceMatch{
				TraceID: stringAt(cols["trace_id"], i),
				SpanIDs: stringListAt(cols["span_ids"], i),
			})
		}
		rec.Release()
	}
	return out, nil
}

// --- Trace spans (pairs with TraceQL) ---------------------------------------------------------------

const opGetTrace uint32 = 13

type getTraceReq struct {
	TraceID string `json:"trace_id"`
}

// GetTrace fetches one trace's spans as zero-copy Arrow rows. traceID is the 32-char hex id — the same
// form QueryTraceQLMatches returns, so a TraceQL match can be passed straight in.
func (db *DB) GetTrace(ctx context.Context, traceID string) (*Rows, error) {
	payload, err := json.Marshal(getTraceReq{TraceID: traceID})
	if err != nil {
		return nil, err
	}
	return db.openStream(ctx, opGetTrace, payload)
}

// Span is one decoded span of a trace.
type Span struct {
	TraceID       []byte
	SpanID        []byte
	ParentSpanID  []byte
	Name          string
	Kind          string
	StartTime     int64 // unix nanoseconds
	DurationNs    int64
	StatusCode    string
	StatusMessage string
	Service       string
	Attributes    string // JSON object string
}

// GetTraceSpans fetches a trace and decodes its spans into []Span (ordered by start time).
func (db *DB) GetTraceSpans(ctx context.Context, traceID string) ([]Span, error) {
	rows, err := db.GetTrace(ctx, traceID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Span
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
			out = append(out, Span{
				TraceID:       bytesAt(cols["trace_id"], i),
				SpanID:        bytesAt(cols["span_id"], i),
				ParentSpanID:  bytesAt(cols["parent_span_id"], i),
				Name:          stringAt(cols["name"], i),
				Kind:          stringAt(cols["kind"], i),
				StartTime:     int64At(cols["start_time"], i),
				DurationNs:    int64At(cols["duration_ns"], i),
				StatusCode:    stringAt(cols["status_code"], i),
				StatusMessage: stringAt(cols["status_message"], i),
				Service:       stringAt(cols["service"], i),
				Attributes:    stringAt(cols["attributes"], i),
			})
		}
		rec.Release()
	}
	return out, nil
}
