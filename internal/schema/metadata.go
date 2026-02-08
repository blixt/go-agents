package schema

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
