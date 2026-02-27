package toolresult

import (
	"strings"
	"testing"
)

func TestRenderExecResultEnvelope(t *testing.T) {
	xml := Render("exec", map[string]any{"hello": "123"})
	if !strings.Contains(xml, "<exec_result>") {
		t.Fatalf("expected exec_result root, got %q", xml)
	}
	if !strings.Contains(xml, "<variable>$result1</variable>") {
		t.Fatalf("expected result variable, got %q", xml)
	}
	if !strings.Contains(xml, "<value>") || !strings.Contains(xml, "</value>") {
		t.Fatalf("expected value block, got %q", xml)
	}
	if !strings.Contains(xml, "\"hello\": \"123\"") {
		t.Fatalf("expected formatted payload in value block, got %q", xml)
	}
}
