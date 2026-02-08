package idgen

import (
	"database/sql"
	"fmt"
)

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
