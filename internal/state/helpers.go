package state

import "encoding/json"

func decodeJSONMap(v string) map[string]any {
	if v == "" {
		return nil
	}
	var out map[string]any
	if err := json.Unmarshal([]byte(v), &out); err != nil {
		return nil
	}
	return out
}

func nullString(v string) any {
	if v == "" {
		return nil
	}
	return v
}
