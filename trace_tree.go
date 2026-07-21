package imbhgo

// trace_tree.go — pure-Go assembly of a flat []Span (as returned by DB.GetTraceSpans) into a
// parent→child forest keyed on ParentSpanID. No FFI op and no Rust change: this is a plain
// reshaping of already-decoded spans, so it needs neither a DB nor a running runtime for the
// core AssembleTrace path.

import (
	"context"
	"sort"
)

// TraceNode is one span in an assembled trace tree. It embeds the decoded Span and holds the
// spans whose ParentSpanID names this span's SpanID.
type TraceNode struct {
	Span
	Children []*TraceNode
}

// AssembleTrace rebuilds the parent→child forest from a flat span slice and returns the roots.
//
// A span becomes a root when its ParentSpanID is empty OR when its parent's SpanID is not present
// in the input (an orphan is surfaced as a root so no span is ever dropped). Every other span is
// attached under the node whose SpanID equals its ParentSpanID. Spans are keyed by string(SpanID);
// on a duplicate SpanID the first occurrence wins and later ones are discarded.
//
// The input slice is not mutated: nodes are built from copies of the Span values. Children of each
// node, and the returned roots, are sorted by StartTime then by string(SpanID) so the output is
// stable regardless of input order. An empty or nil input returns nil.
func AssembleTrace(spans []Span) []*TraceNode {
	if len(spans) == 0 {
		return nil
	}

	// Index unique spans by SpanID (first occurrence wins).
	byID := make(map[string]*TraceNode, len(spans))
	order := make([]*TraceNode, 0, len(spans))
	for i := range spans {
		key := string(spans[i].SpanID)
		if _, dup := byID[key]; dup {
			continue
		}
		node := &TraceNode{Span: spans[i]}
		byID[key] = node
		order = append(order, node)
	}

	// Link children to parents; collect roots (empty or absent parent).
	var roots []*TraceNode
	for _, node := range order {
		pid := string(node.ParentSpanID)
		if len(node.ParentSpanID) == 0 {
			roots = append(roots, node)
			continue
		}
		if parent, ok := byID[pid]; ok {
			parent.Children = append(parent.Children, node)
		} else {
			roots = append(roots, node)
		}
	}

	sortNodes(roots)
	for _, node := range order {
		sortNodes(node.Children)
	}
	return roots
}

// sortNodes orders nodes by StartTime then string(SpanID) for a deterministic, input-order-agnostic
// result.
func sortNodes(nodes []*TraceNode) {
	sort.Slice(nodes, func(i, j int) bool {
		if nodes[i].StartTime != nodes[j].StartTime {
			return nodes[i].StartTime < nodes[j].StartTime
		}
		return string(nodes[i].SpanID) < string(nodes[j].SpanID)
	})
}

// GetTraceForest fetches a trace's spans (via GetTraceSpans) and returns them assembled into a
// parent→child forest. It is named GetTraceForest rather than GetTrace because DB.GetTrace already
// exists in lgtm.go and returns zero-copy Arrow *Rows.
func (db *DB) GetTraceForest(ctx context.Context, traceID string) ([]*TraceNode, error) {
	spans, err := db.GetTraceSpans(ctx, traceID)
	if err != nil {
		return nil, err
	}
	return AssembleTrace(spans), nil
}
