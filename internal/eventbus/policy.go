package eventbus

import "strings"

var defaultOrder = map[string]string{
	"errors":      "lifo",
	"messages":    "fifo",
	"task_input":  "fifo",
	"task_output": "lifo",
	"signals":     "lifo",
	"external":    "lifo",
}

func DefaultOrder(stream string) string {
	stream = strings.TrimSpace(stream)
	if stream == "" {
		return "lifo"
	}
	if order, ok := defaultOrder[stream]; ok {
		return order
	}
	return "lifo"
}
