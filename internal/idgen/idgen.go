package idgen

import "github.com/google/uuid"

// New returns a UUIDv7 identifier string.
// If UUIDv7 generation fails, it falls back to a random UUIDv4.
func New() string {
	id, err := uuid.NewV7()
	if err != nil {
		return uuid.NewString()
	}
	return id.String()
}
