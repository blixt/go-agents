package eventbus

import "testing"

func TestDefaultOrder(t *testing.T) {
	if DefaultOrder("messages") != "fifo" {
		t.Fatalf("expected fifo for messages")
	}
	if DefaultOrder("errors") != "lifo" {
		t.Fatalf("expected lifo for errors")
	}
	if DefaultOrder("unknown") != "lifo" {
		t.Fatalf("expected lifo for unknown")
	}
}
