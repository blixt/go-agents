package eventbus

import "testing"

func TestDefaultOrder(t *testing.T) {
	if DefaultOrder("task_input") != "fifo" {
		t.Fatalf("expected fifo for task_input")
	}
	if DefaultOrder("errors") != "lifo" {
		t.Fatalf("expected lifo for errors")
	}
	if DefaultOrder("unknown") != "lifo" {
		t.Fatalf("expected lifo for unknown")
	}
}
