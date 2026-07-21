package imbhgo

import "testing"

// childNames returns the Name of each child of n, in stored (post-sort) order.
func childNames(n *TraceNode) []string {
	out := make([]string, len(n.Children))
	for i, c := range n.Children {
		out[i] = c.Name
	}
	return out
}

// TestAssembleTraceShape builds a small tree (root → 2 children → 1 grandchild) plus an orphan
// whose parent is absent, then asserts the forest shape, per-node child counts, and that both the
// roots and each node's children come back in the deterministic StartTime/SpanID order regardless
// of the (deliberately shuffled) input order.
func TestAssembleTraceShape(t *testing.T) {
	// Tree:
	//   root (start 100)
	//     ├─ childA (start 200)
	//     │    └─ grandchild (start 300)
	//     └─ childB (start 250)
	//   orphan (start 400, parent "missing" not in set)  -> surfaces as a second root
	spans := []Span{
		{SpanID: []byte("gc"), ParentSpanID: []byte("A"), Name: "grandchild", StartTime: 300},
		{SpanID: []byte("B"), ParentSpanID: []byte("root"), Name: "childB", StartTime: 250},
		{SpanID: []byte("orphan"), ParentSpanID: []byte("missing"), Name: "orphan", StartTime: 400},
		{SpanID: []byte("A"), ParentSpanID: []byte("root"), Name: "childA", StartTime: 200},
		{SpanID: []byte("root"), ParentSpanID: nil, Name: "root", StartTime: 100},
	}

	roots := AssembleTrace(spans)

	// Two roots: the real root (start 100) then the orphan (start 400), in StartTime order.
	if len(roots) != 2 {
		t.Fatalf("want 2 roots, got %d", len(roots))
	}
	if roots[0].Name != "root" || roots[1].Name != "orphan" {
		t.Fatalf("roots not in StartTime order: got %q, %q", roots[0].Name, roots[1].Name)
	}

	// root has childA then childB (StartTime 200 < 250).
	root := roots[0]
	if got := childNames(root); len(got) != 2 || got[0] != "childA" || got[1] != "childB" {
		t.Fatalf("root children want [childA childB], got %v", got)
	}

	// childA has the single grandchild; childB and grandchild are leaves; orphan is a leaf.
	childA := root.Children[0]
	if got := childNames(childA); len(got) != 1 || got[0] != "grandchild" {
		t.Fatalf("childA children want [grandchild], got %v", got)
	}
	if len(root.Children[1].Children) != 0 {
		t.Fatalf("childB should be a leaf, got %d children", len(root.Children[1].Children))
	}
	if len(childA.Children[0].Children) != 0 {
		t.Fatalf("grandchild should be a leaf, got %d children", len(childA.Children[0].Children))
	}
	if len(roots[1].Children) != 0 {
		t.Fatalf("orphan should be a leaf, got %d children", len(roots[1].Children))
	}
}

// TestAssembleTraceEmpty asserts empty and nil inputs return no roots.
func TestAssembleTraceEmpty(t *testing.T) {
	if got := AssembleTrace(nil); got != nil {
		t.Fatalf("AssembleTrace(nil) = %v, want nil", got)
	}
	if got := AssembleTrace([]Span{}); got != nil {
		t.Fatalf("AssembleTrace([]) = %v, want nil", got)
	}
}

// TestAssembleTraceDuplicateFirstWins asserts that on a duplicate SpanID the first occurrence is
// kept and later duplicates are discarded.
func TestAssembleTraceDuplicateFirstWins(t *testing.T) {
	spans := []Span{
		{SpanID: []byte("x"), ParentSpanID: nil, Name: "first", StartTime: 10},
		{SpanID: []byte("x"), ParentSpanID: nil, Name: "second", StartTime: 20},
	}
	roots := AssembleTrace(spans)
	if len(roots) != 1 {
		t.Fatalf("want 1 root after dedup, got %d", len(roots))
	}
	if roots[0].Name != "first" {
		t.Fatalf("duplicate handling: want first occurrence %q, got %q", "first", roots[0].Name)
	}
}

// TestAssembleTraceNoInputMutation asserts AssembleTrace does not reorder the caller's slice.
func TestAssembleTraceNoInputMutation(t *testing.T) {
	spans := []Span{
		{SpanID: []byte("b"), ParentSpanID: nil, Name: "b", StartTime: 2},
		{SpanID: []byte("a"), ParentSpanID: nil, Name: "a", StartTime: 1},
	}
	_ = AssembleTrace(spans)
	if spans[0].Name != "b" || spans[1].Name != "a" {
		t.Fatalf("input slice order was mutated: %q, %q", spans[0].Name, spans[1].Name)
	}
}
