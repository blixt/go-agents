package schema

import "strings"

const (
	MetaInputTarget  = "input_target"
	MetaNotifyTarget = "notify_target"
	MetaParentID     = "parent_id"
	MetaMode         = "mode"
	MetaPriority     = "priority"
	MetaKind         = "kind"
	MetaAction       = "action"
	MetaSource       = "source"
	MetaTaskID       = "task_id"
	MetaTaskKind     = "task_kind"
	// Delivery controls how an event is routed to consumers.
	MetaDeliveryMode    = "delivery_mode"    // "default" | "opt_in" | "opt_out"
	MetaDeliveryInclude = "delivery_include" // []string, []any, or comma-delimited string
	MetaDeliveryExclude = "delivery_exclude" // []string, []any, or comma-delimited string
)

const (
	DeliveryModeDefault = "default"
	DeliveryModeOptIn   = "opt_in"
	DeliveryModeOptOut  = "opt_out"
)

const (
	ConsumerAgentContext = "agent_context"
)

// GetMetaString extracts a string from a metadata map. Returns "" if missing/not string.
func GetMetaString(meta map[string]any, key string) string {
	if meta == nil {
		return ""
	}
	val, ok := meta[key]
	if !ok {
		return ""
	}
	str, ok := val.(string)
	if !ok {
		return ""
	}
	return str
}

// MetaVisibleToConsumer evaluates generic delivery metadata for a consumer.
// Default visibility is used when no delivery rules apply.
func MetaVisibleToConsumer(meta map[string]any, consumer string, defaultVisible bool) bool {
	name := strings.ToLower(strings.TrimSpace(consumer))
	if name == "" {
		return defaultVisible
	}
	include := metaStringSet(meta, MetaDeliveryInclude)
	exclude := metaStringSet(meta, MetaDeliveryExclude)

	if inMetaSet(include, name) {
		return true
	}
	if inMetaSet(exclude, name) {
		return false
	}

	switch strings.ToLower(strings.TrimSpace(GetMetaString(meta, MetaDeliveryMode))) {
	case DeliveryModeOptIn:
		return false
	case DeliveryModeOptOut:
		return true
	default:
		return defaultVisible
	}
}

func inMetaSet(set map[string]struct{}, consumer string) bool {
	if len(set) == 0 {
		return false
	}
	if _, ok := set[consumer]; ok {
		return true
	}
	_, ok := set["*"]
	return ok
}

func metaStringSet(meta map[string]any, key string) map[string]struct{} {
	if meta == nil {
		return nil
	}
	raw, ok := meta[key]
	if !ok || raw == nil {
		return nil
	}
	out := map[string]struct{}{}
	add := func(values ...string) {
		for _, val := range values {
			for _, part := range strings.Split(val, ",") {
				name := strings.ToLower(strings.TrimSpace(part))
				if name == "" {
					continue
				}
				out[name] = struct{}{}
			}
		}
	}

	switch typed := raw.(type) {
	case string:
		add(typed)
	case []string:
		add(typed...)
	case []any:
		for _, item := range typed {
			if s, ok := item.(string); ok {
				add(s)
			}
		}
	case map[string]bool:
		for name, enabled := range typed {
			if enabled {
				add(name)
			}
		}
	case map[string]any:
		for name, enabled := range typed {
			switch flag := enabled.(type) {
			case bool:
				if flag {
					add(name)
				}
			case string:
				if strings.EqualFold(strings.TrimSpace(flag), "true") || strings.TrimSpace(flag) == "1" {
					add(name)
				}
			}
		}
	}

	if len(out) == 0 {
		return nil
	}
	return out
}
