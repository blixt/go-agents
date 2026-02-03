package prompt

import (
	"context"
	"fmt"
	"strings"

	"github.com/flitsinc/go-agents/internal/eventbus"
)

type StreamPolicy struct {
	UpdateStreams  []string
	CommandStreams []string
	Reader         string
	Limit          int
	Order          string
	Ack            bool
}

type StreamUpdates struct {
	Events []eventbus.Event
}

func CollectUpdates(ctx context.Context, bus *eventbus.Bus, policy StreamPolicy) (StreamUpdates, error) {
	var updates StreamUpdates
	if bus == nil {
		return updates, nil
	}
	limit := policy.Limit
	if limit <= 0 {
		limit = 50
	}

	for _, stream := range policy.UpdateStreams {
		list, err := bus.List(ctx, stream, eventbus.ListOptions{Limit: limit, Order: policy.Order, Reader: policy.Reader})
		if err != nil {
			return updates, err
		}
		ids := make([]string, 0, len(list))
		for _, item := range list {
			ids = append(ids, item.ID)
		}
		events, err := bus.Read(ctx, stream, ids, policy.Reader)
		if err != nil {
			return updates, err
		}
		updates.Events = append(updates.Events, events...)
		if policy.Ack && len(ids) > 0 {
			_ = bus.Ack(ctx, stream, ids, policy.Reader)
		}
	}
	return updates, nil
}

func RenderUpdates(updates StreamUpdates) string {
	if len(updates.Events) == 0 {
		return ""
	}
	var sb strings.Builder
	for _, evt := range updates.Events {
		subject := evt.Subject
		if subject == "" {
			subject = evt.Body
		}
		_, _ = fmt.Fprintf(&sb, "[%s] %s\n", evt.Stream, subject)
	}
	return strings.TrimSpace(sb.String())
}
