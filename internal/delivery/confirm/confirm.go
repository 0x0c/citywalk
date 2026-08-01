// Package confirm implements CW-0002 Unit 4's server side: the lightweight endpoint a device calls
// immediately before displaying a campaign that opted into server confirmation. Fail-closed is the
// whole point — a timeout or an error must suppress the display, never allow it — so this package
// never returns true except on an explicit, positive re-check.
package confirm

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Confirm re-checks messageID's eligibility at now: still active, still within its window. A
// campaign-specific check — a fixed budget, a cross-device cap, live stock — is not implemented
// here; this is the generic re-check every server-confirmed campaign needs regardless, and the hook
// a campaign-specific rule would extend. Any error, including "message not found," returns false:
// the caller is never handed an ambiguous result it might mistake for permission to display.
func Confirm(ctx context.Context, pool *pgxpool.Pool, messageID string, now time.Time) (bool, error) {
	var state string
	var windowStart, windowEnd time.Time
	err := pool.QueryRow(ctx,
		`SELECT state, window_start, window_end FROM messages WHERE id = $1`, messageID,
	).Scan(&state, &windowStart, &windowEnd)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return false, fmt.Errorf("confirm: message %s not found", messageID)
		}
		return false, fmt.Errorf("confirm: look up message %s: %w", messageID, err)
	}

	if state != "active" {
		return false, nil
	}
	if now.Before(windowStart) || !now.Before(windowEnd) {
		return false, nil
	}
	return true, nil
}
