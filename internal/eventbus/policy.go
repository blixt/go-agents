package eventbus

import (
	"strings"

	"github.com/flitsinc/go-agents/internal/schema"
)

func DefaultOrder(stream string) string {
	stream = strings.TrimSpace(stream)
	if stream == "" {
		return "lifo"
	}
	return schema.StreamOrdering(stream)
}
