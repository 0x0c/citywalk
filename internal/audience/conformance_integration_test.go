//go:build integration

// Run with: go test -tags=integration ./internal/audience/... with CITYWALK_TEST_POSTGRES_DSN
// pointing at a scratch PostgreSQL database. This is the conformance suite CW-0004 Unit 4 requires
// as a deliverable, not a test detail: it evaluates a corpus of predicates against a fixture
// population through both the row-wise (Go/CEL) and set-wise (compiled SQL) backends and fails on
// any disagreement, catching the cases the two differ on by default — null handling, string
// collation, and timestamp boundaries.
package audience_test

import (
	"context"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/0x0c/citywalk/internal/audience/audiencetest"
	"github.com/0x0c/citywalk/internal/audience/eval"
	"github.com/0x0c/citywalk/internal/audience/predicate"
	"github.com/0x0c/citywalk/internal/audience/sqlcompile"
	"github.com/0x0c/citywalk/internal/platform/postgres"
	"github.com/0x0c/citywalk/migrations"
)

type channelFixture struct {
	name       string
	attributes map[string]any
}

var channelFixtures = []channelFixture{
	{
		name: "jp channel, long walker, premium, has riverside_loop",
		attributes: map[string]any{
			"app_version": "2.10.0", "country": "JP", "total_distance_km": 150.0,
			"is_premium": true, "registered_at": "2025-01-15T00:00:00Z",
			"favorite_routes": []string{"riverside_loop", "harbor_walk"}, "route_screen_views_7d": 5.0,
		},
	},
	{
		name: "us channel, short walker, not premium, older app",
		attributes: map[string]any{
			"app_version": "2.9.0", "country": "US", "total_distance_km": 3.0,
			"is_premium": false, "registered_at": "2026-03-01T00:00:00Z",
			"favorite_routes": []string{"harbor_walk"}, "route_screen_views_7d": 0.0,
		},
	},
	{
		name: "jp channel, boundary distance, no favorite routes",
		attributes: map[string]any{
			"app_version": "2.10.0", "country": "JP", "total_distance_km": 100.0,
			"is_premium": false, "registered_at": "2025-12-31T23:59:59Z",
			"favorite_routes": []string{}, "route_screen_views_7d": 1.0,
		},
	},
	{
		// Exercises the >= boundary exactly: 2.10.0 must match "at least 2.10.0", which is also the
		// case NormalizeSemVer's zero-padding must get right — component count 1 vs 2 vs 3 all
		// present here (major.minor.patch fully specified).
		name: "jp channel, app version exactly at the semver threshold",
		attributes: map[string]any{
			"app_version": "2.10.0", "country": "JP", "total_distance_km": 200.0,
			"is_premium": true, "registered_at": "2024-01-01T00:00:00Z",
			"favorite_routes": []string{"riverside_loop"}, "route_screen_views_7d": 10.0,
		},
	},
	{
		// Exercises a two-component version (no patch), which NormalizeSemVer and
		// semver_normalize must both default to patch 0 identically.
		name: "jp channel, two-component app version",
		attributes: map[string]any{
			"app_version": "3.0", "country": "JP", "total_distance_km": 50.0,
			"is_premium": false, "registered_at": "2025-07-04T12:00:00Z",
			"favorite_routes": []string{}, "route_screen_views_7d": 2.0,
		},
	},
}

var predicateFixtures = []string{
	`country == "JP"`,
	`country != "JP"`,
	`total_distance_km >= 100.0`,
	`total_distance_km < 100.0`,
	`is_premium == true`,
	`country == "JP" && total_distance_km >= 100.0`,
	`country == "JP" || is_premium == true`,
	`!(country == "JP")`,
	`"riverside_loop" in favorite_routes`,
	`semver(app_version) >= semver("2.10.0")`,
	`registered_at < timestamp("2026-01-01T00:00:00Z")`,
}

// TestRowWiseAndSetWiseAgree is CW-0004 Unit 4's conformance suite: every predicate fixture,
// evaluated against every channel fixture, through both backends, must agree — and must agree with
// the row-wise backend run directly in Go, which is the ground truth for this table-driven check.
func TestRowWiseAndSetWiseAgree(t *testing.T) {
	dsn := os.Getenv("CITYWALK_TEST_POSTGRES_DSN")
	if dsn == "" {
		t.Skip("CITYWALK_TEST_POSTGRES_DSN not set")
	}
	ctx := context.Background()
	pool, err := postgres.NewPool(ctx, dsn)
	if err != nil {
		t.Fatalf("NewPool: %v", err)
	}
	t.Cleanup(pool.Close)
	if err := postgres.Migrate(ctx, pool, migrations.FS); err != nil {
		t.Fatalf("Migrate: %v", err)
	}

	env, err := audiencetest.Env()
	if err != nil {
		t.Fatalf("Env: %v", err)
	}
	reg := audiencetest.Registry()
	evaluator := eval.New(env)

	channelIDs := insertChannelFixtures(t, ctx, pool, channelFixtures)
	rowWiseAttrs := rowWiseAttributes(t, channelFixtures)

	for _, source := range predicateFixtures {
		t.Run(source, func(t *testing.T) {
			p, err := predicate.Compile(env, reg, source)
			if err != nil {
				t.Fatalf("Compile(%q): %v", source, err)
			}
			ast, err := predicate.Load(p)
			if err != nil {
				t.Fatalf("Load: %v", err)
			}
			whereSQL, args, err := sqlcompile.CompileToSQL(ast.NativeRep(), reg)
			if err != nil {
				t.Fatalf("CompileToSQL(%q): %v", source, err)
			}

			for i, fx := range channelFixtures {
				rowWise, err := evaluator.Evaluate(p, rowWiseAttrs[i])
				if err != nil {
					t.Fatalf("Evaluate(%q, %s): %v", source, fx.name, err)
				}

				query := fmt.Sprintf("SELECT %s FROM channels WHERE id = $%d", whereSQL, len(args)+1)
				var setWise bool
				if err := pool.QueryRow(ctx, query, append(append([]any{}, args...), channelIDs[i])...).Scan(&setWise); err != nil {
					t.Fatalf("set-wise query(%q, %s): %v\nsql: %s", source, fx.name, err, query)
				}

				if rowWise != setWise {
					t.Errorf("%s: row-wise = %v, set-wise = %v for channel %q — backends disagree", source, rowWise, setWise, fx.name)
				}
			}
		})
	}
}

func insertChannelFixtures(t *testing.T, ctx context.Context, pool *pgxpool.Pool, fixtures []channelFixture) []string {
	t.Helper()
	ids := make([]string, len(fixtures))
	for i, fx := range fixtures {
		attrs := map[string]any{}
		for k, v := range fx.attributes {
			attrs[k] = v
		}
		var id string
		if err := pool.QueryRow(ctx,
			`INSERT INTO channels (attributes) VALUES ($1) RETURNING id`, attrs,
		).Scan(&id); err != nil {
			t.Fatalf("insert channel fixture %q: %v", fx.name, err)
		}
		ids[i] = id
	}
	return ids
}

// rowWiseAttributes converts each fixture's JSON-friendly attribute map (string timestamps, so the
// same literal map works as jsonb for the SQL side) into the Go-typed map the CEL evaluator expects
// (time.Time rather than a string) for registered_at.
func rowWiseAttributes(t *testing.T, fixtures []channelFixture) []map[string]any {
	t.Helper()
	out := make([]map[string]any, len(fixtures))
	for i, fx := range fixtures {
		attrs := map[string]any{}
		for k, v := range fx.attributes {
			attrs[k] = v
		}
		ts, err := time.Parse(time.RFC3339, fx.attributes["registered_at"].(string))
		if err != nil {
			t.Fatalf("parse registered_at fixture for %q: %v", fx.name, err)
		}
		attrs["registered_at"] = ts
		out[i] = attrs
	}
	return out
}
