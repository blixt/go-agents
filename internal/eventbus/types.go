package eventbus

import "time"

type Event struct {
	ID        string         `json:"id"`
	Stream    string         `json:"stream"`
	ScopeType string         `json:"scope_type"`
	ScopeID   string         `json:"scope_id"`
	Subject   string         `json:"subject"`
	Body      string         `json:"body"`
	Metadata  map[string]any `json:"metadata,omitempty"`
	Payload   map[string]any `json:"payload,omitempty"`
	CreatedAt time.Time      `json:"created_at"`
	Read      bool           `json:"read"`
	ReadBy    []string       `json:"read_by,omitempty"`
}

type EventSummary struct {
	ID        string    `json:"id"`
	Stream    string    `json:"stream"`
	Subject   string    `json:"subject"`
	CreatedAt time.Time `json:"created_at"`
	Read      bool      `json:"read"`
}

type EventInput struct {
	Stream    string
	ScopeType string
	ScopeID   string
	Subject   string
	Body      string
	Metadata  map[string]any
	Payload   map[string]any
	SourceID  string // if set, event is born "already read" by this ID
}

type ListOptions struct {
	Reader    string
	Limit     int
	Order     string
	ScopeType string
	ScopeID   string
}
