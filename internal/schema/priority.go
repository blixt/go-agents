package schema

import "strings"

// Priority represents a validated event priority level.
type Priority string

const (
	PriorityInterrupt Priority = "interrupt"
	PriorityWake      Priority = "wake"
	PriorityNormal    Priority = "normal"
	PriorityLow       Priority = "low"
)

// ParsePriority validates a raw string. Defaults to PriorityNormal.
func ParsePriority(raw string) Priority {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "interrupt":
		return PriorityInterrupt
	case "wake":
		return PriorityWake
	case "normal":
		return PriorityNormal
	case "low":
		return PriorityLow
	default:
		return PriorityNormal
	}
}

// Rank returns numeric priority (lower = higher).
// interrupt=0, wake=1, normal=2, low=3.
func (p Priority) Rank() int {
	switch p {
	case PriorityInterrupt:
		return 0
	case PriorityWake:
		return 1
	case PriorityNormal:
		return 2
	case PriorityLow:
		return 3
	default:
		return 2
	}
}

// Wakes returns true if this priority should wake an awaiting task.
func (p Priority) Wakes() bool {
	return p == PriorityInterrupt || p == PriorityWake
}
