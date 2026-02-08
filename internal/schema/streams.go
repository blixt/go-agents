package schema

const (
	StreamTaskInput  = "task_input"
	StreamTaskOutput = "task_output"
	StreamSignals    = "signals"
	StreamErrors     = "errors"
	StreamExternal   = "external"
	StreamHistory    = "history"
)

// AgentStreams are the streams the agent loop monitors for context
// events and that wake awaiting tasks.
var AgentStreams = []string{
	StreamTaskOutput,
	StreamSignals,
	StreamErrors,
	StreamExternal,
	StreamTaskInput,
}

// StreamOrdering returns "fifo" or "lifo" for a given stream.
func StreamOrdering(stream string) string {
	if stream == StreamTaskInput {
		return "fifo"
	}
	return "lifo"
}
