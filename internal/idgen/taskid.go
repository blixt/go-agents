package idgen

import (
	"database/sql"
	"fmt"
	"regexp"
)

var customIDPattern = regexp.MustCompile(`^[a-z]([a-z0-9-]*[a-z0-9])?$`)

// ValidateCustomID checks that id is a valid user-provided task ID.
// Rules: lowercase letters, digits, and dashes; must start with a letter and
// end with a letter or digit; max 64 characters.
func ValidateCustomID(id string) error {
	if len(id) > 64 {
		return fmt.Errorf("custom id too long (max 64 characters)")
	}
	if !customIDPattern.MatchString(id) {
		return fmt.Errorf("custom id %q is invalid: must match %s", id, customIDPattern.String())
	}
	return nil
}

// TaskID generates a human-readable task ID like "agent-1", "planner-2".
// It queries the database for the highest existing sequence number with the
// given prefix and returns prefix-(max+1).
func TaskID(db *sql.DB, prefix string) string {
	var maxN sql.NullInt64
	// SUBSTR offset is 1-based: skip prefix + dash
	offset := len(prefix) + 2
	err := db.QueryRow(
		`SELECT MAX(CAST(SUBSTR(id, ?) AS INTEGER)) FROM tasks WHERE id LIKE ?`,
		offset, prefix+"-%",
	).Scan(&maxN)
	if err != nil || !maxN.Valid {
		return fmt.Sprintf("%s-%d", prefix, 1)
	}
	return fmt.Sprintf("%s-%d", prefix, maxN.Int64+1)
}
