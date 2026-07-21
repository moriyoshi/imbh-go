package imbhgo

// traces_search.go — trace search (imbh's `traces().search`). The query builder yields a flat
// `Vec<TraceSummary>` upstream (no Arrow form), which the Rust handler maps onto one Arrow batch and
// streams back over the shared Rows path; this file marshals the request and decodes the batch into a
// plain Go slice.

import (
	"context"
	"encoding/json"
)

// Op id shared with rust/src/lib.rs.
const opTraceSearch uint32 = 21

// TraceQuery selects traces for SearchTraces. All fields are optional: a field is applied only when
// non-empty/non-zero, so a zero TraceQuery matches everything (up to the server's default limit). The
// JSON tags match the Rust `TraceQueryWire`.
type TraceQuery struct {
	Service       string            `json:"service,omitempty"`
	Name          string            `json:"name,omitempty"`
	Status        string            `json:"status,omitempty"`
	Kind          string            `json:"kind,omitempty"`
	MinDurationNs int64             `json:"min_duration_ns,omitempty"`
	MaxDurationNs int64             `json:"max_duration_ns,omitempty"`
	AttrEq        map[string]string `json:"attr_eq,omitempty"`

	// Attribute predicates (parity with LogQuery; all AND-combined with the above).
	AttrExists  []string            `json:"attr_exists,omitempty"`  // keys that must be present
	AttrMatches map[string]string   `json:"attr_matches,omitempty"` // key → full-text term match on that attribute
	AttrIn      map[string][]string `json:"attr_in,omitempty"`      // key → allowed value set
	AttrNotIn   map[string][]string `json:"attr_not_in,omitempty"`  // key → excluded value set
	AttrGt      map[string]float64  `json:"attr_gt,omitempty"`      // key → value must be > n
	AttrGe      map[string]float64  `json:"attr_ge,omitempty"`      // key → value must be >= n
	AttrLt      map[string]float64  `json:"attr_lt,omitempty"`      // key → value must be < n
	AttrLe      map[string]float64  `json:"attr_le,omitempty"`      // key → value must be <= n
	AttrRegex   map[string]string   `json:"attr_regex,omitempty"`   // key → RE2 pattern the value must match

	Start int64 `json:"start,omitempty"`
	End   int64 `json:"end,omitempty"`
	Limit int64 `json:"limit,omitempty"`
}

// TraceSummary is one matched trace's summary row. RootService/RootName are "" when the root span
// carries none (SQL-NULL upstream). StartTime is unix nanoseconds; DurationNs is the trace's wall
// duration in nanoseconds.
type TraceSummary struct {
	TraceID     string
	RootService string
	RootName    string
	StartTime   int64
	DurationNs  int64
	SpanCount   int64
	Error       bool
}

// SearchTraces returns the trace summaries matching q, most-recent-first per imbh's ordering.
func (db *DB) SearchTraces(ctx context.Context, q TraceQuery) ([]TraceSummary, error) {
	b, err := json.Marshal(q)
	if err != nil {
		return nil, err
	}
	rows, err := db.openStream(ctx, opTraceSearch, b)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []TraceSummary
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
			out = append(out, TraceSummary{
				TraceID:     stringAt(cols["trace_id"], i),
				RootService: stringAt(cols["root_service"], i),
				RootName:    stringAt(cols["root_name"], i),
				StartTime:   int64At(cols["start_time"], i),
				DurationNs:  int64At(cols["duration_ns"], i),
				SpanCount:   int64At(cols["span_count"], i),
				Error:       boolAt(cols["error"], i),
			})
		}
		rec.Release()
	}
	return out, nil
}
