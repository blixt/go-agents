package api

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestNormalizeNilSlicesConvertsNestedValues(t *testing.T) {
	type payloadShape struct {
		Items  []string       `json:"items"`
		Nested map[string]any `json:"nested"`
	}

	input := payloadShape{
		Items: nil,
		Nested: map[string]any{
			"direct": []int(nil),
			"deep": []any{
				map[string]any{"values": []string(nil)},
			},
		},
	}

	normalized := normalizeNilSlices(input)
	raw, err := json.Marshal(normalized)
	if err != nil {
		t.Fatalf("marshal normalized payload: %v", err)
	}
	if strings.Contains(string(raw), ":null") {
		t.Fatalf("expected no null slices after normalization, got %s", raw)
	}

	var decoded map[string]any
	if err := json.Unmarshal(raw, &decoded); err != nil {
		t.Fatalf("decode normalized payload: %v", err)
	}

	items, ok := decoded["items"].([]any)
	if !ok || len(items) != 0 {
		t.Fatalf("expected items to be empty array, got %#v", decoded["items"])
	}
	nested, ok := decoded["nested"].(map[string]any)
	if !ok {
		t.Fatalf("expected nested object, got %#v", decoded["nested"])
	}
	direct, ok := nested["direct"].([]any)
	if !ok || len(direct) != 0 {
		t.Fatalf("expected nested.direct to be empty array, got %#v", nested["direct"])
	}
	deepList, ok := nested["deep"].([]any)
	if !ok || len(deepList) != 1 {
		t.Fatalf("expected nested.deep to contain one entry, got %#v", nested["deep"])
	}
	deepObj, ok := deepList[0].(map[string]any)
	if !ok {
		t.Fatalf("expected nested.deep[0] object, got %#v", deepList[0])
	}
	values, ok := deepObj["values"].([]any)
	if !ok || len(values) != 0 {
		t.Fatalf("expected nested.deep[0].values to be empty array, got %#v", deepObj["values"])
	}
}
