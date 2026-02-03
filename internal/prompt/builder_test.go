package prompt

import "testing"

func TestBuilderOrdering(t *testing.T) {
	b := NewBuilder()
	b.Add(Block{ID: "low", Priority: 1, Content: "low"})
	b.Add(Block{ID: "high", Priority: 10, Content: "high"})
	b.Add(Block{ID: "mid", Priority: 5, Content: "mid"})

	got := b.Build()
	expected := "high\n\nmid\n\nlow"
	if got != expected {
		t.Fatalf("unexpected build: %q", got)
	}
}

func TestStateDiff(t *testing.T) {
	before := State{Messages: []string{"a"}, Updates: []string{"u1"}}
	after := State{Messages: []string{"a", "b"}, Updates: []string{"u1", "u2"}}
	diff := Diff(before, after)
	if len(diff.Messages) != 1 || diff.Messages[0] != "b" {
		t.Fatalf("unexpected message diff")
	}
	if len(diff.Updates) != 1 || diff.Updates[0] != "u2" {
		t.Fatalf("unexpected update diff")
	}
}
