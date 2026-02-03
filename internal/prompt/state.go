package prompt

import (
	"encoding/json"
)

type State struct {
	Messages []string `json:"messages"`
	Updates  []string `json:"updates"`
}

func (s State) Snapshot() ([]byte, error) {
	return json.Marshal(s)
}

func Diff(before, after State) State {
	return State{
		Messages: diffStrings(before.Messages, after.Messages),
		Updates:  diffStrings(before.Updates, after.Updates),
	}
}

func diffStrings(before, after []string) []string {
	seen := map[string]struct{}{}
	for _, v := range before {
		seen[v] = struct{}{}
	}
	var out []string
	for _, v := range after {
		if _, ok := seen[v]; ok {
			continue
		}
		out = append(out, v)
	}
	return out
}
