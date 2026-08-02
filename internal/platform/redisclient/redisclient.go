// Package redisclient wires the hot-lookup and counter store (CW-0010 Unit 4): the reverse
// membership index, the delta-sync caches, and the display-governance counters all share this
// client, since everything it holds is derived or expiring and losing the store costs a rebuild
// rather than data.
package redisclient

import (
	"context"
	"fmt"

	"github.com/redis/go-redis/v9"
)

// New connects to addr and verifies reachability with a PING before returning.
func New(ctx context.Context, addr string) (*redis.Client, error) {
	client := redis.NewClient(&redis.Options{Addr: addr})
	if err := client.Ping(ctx).Err(); err != nil {
		_ = client.Close()
		return nil, fmt.Errorf("redisclient: ping: %w", err)
	}
	return client, nil
}
