// Package segment persists the entity CW-0005's two indexes are computed for: a segment's compiled
// predicate (CW-0004 Unit 2's tree, not its source) and whether it is eligible for CW-0005 Unit 5's
// incremental path.
package segment

import (
	"context"
	"fmt"

	"github.com/google/cel-go/cel"
	celast "github.com/google/cel-go/common/ast"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/0x0c/citywalk/internal/audience/predicate"
	"github.com/0x0c/citywalk/internal/audience/registry"
)

// RefreshMode says whether a segment's membership can be maintained incrementally between batches.
type RefreshMode string

const (
	// RefreshIncremental segments are eligible for CW-0005 Unit 5's incremental path.
	RefreshIncremental RefreshMode = "incremental"
	// RefreshBatchOnly segments are excluded from it: their predicate reads an event aggregate, so
	// membership can change with the passage of time alone, which no attribute write triggers.
	RefreshBatchOnly RefreshMode = "batch_only"
)

// Segment is a saved audience predicate, ready for CW-0005's forward and reverse indexes to track.
type Segment struct {
	ID          string
	Ordinal     int64
	Name        string
	Predicate   *predicate.Predicate
	RefreshMode RefreshMode
}

// Save compiles source against env and reg, classifies its refresh mode by whether it references an
// event-aggregate-sourced attribute, and persists it. A compile error is returned unsaved, per the
// reject-rather-than-warn rule CW-0003 and CW-0004 both hold to.
func Save(ctx context.Context, pool *pgxpool.Pool, env *cel.Env, reg *registry.Registry, name, source string) (*Segment, error) {
	p, err := predicate.Compile(env, reg, source)
	if err != nil {
		return nil, err
	}
	ast, err := predicate.Load(p)
	if err != nil {
		return nil, err
	}

	refMode := RefreshIncremental
	if referencesEventAggregate(ast.NativeRep(), reg) {
		refMode = RefreshBatchOnly
	}

	seg := &Segment{Name: name, Predicate: p, RefreshMode: refMode}
	if err := pool.QueryRow(ctx, `
		INSERT INTO segments (name, predicate_source, predicate_tree, refresh_mode)
		VALUES ($1, $2, $3, $4)
		RETURNING id, ordinal
	`, name, p.Source, p.Tree, string(refMode),
	).Scan(&seg.ID, &seg.Ordinal); err != nil {
		return nil, fmt.Errorf("segment: save %q: %w", name, err)
	}
	return seg, nil
}

// Get loads a segment by ID.
func Get(ctx context.Context, pool *pgxpool.Pool, id string) (*Segment, error) {
	var seg Segment
	var source, refMode string
	var tree []byte
	if err := pool.QueryRow(ctx, `
		SELECT id, ordinal, name, predicate_source, predicate_tree, refresh_mode FROM segments WHERE id = $1
	`, id).Scan(&seg.ID, &seg.Ordinal, &seg.Name, &source, &tree, &refMode); err != nil {
		return nil, fmt.Errorf("segment: get %s: %w", id, err)
	}
	seg.Predicate = &predicate.Predicate{Source: source, Tree: tree}
	seg.RefreshMode = RefreshMode(refMode)
	return &seg, nil
}

// List loads every segment, ordered by id, so callers iterate a stable order.
func List(ctx context.Context, pool *pgxpool.Pool) ([]*Segment, error) {
	rows, err := pool.Query(ctx, `
		SELECT id, ordinal, name, predicate_source, predicate_tree, refresh_mode FROM segments ORDER BY id
	`)
	if err != nil {
		return nil, fmt.Errorf("segment: list: %w", err)
	}
	defer rows.Close()

	var segments []*Segment
	for rows.Next() {
		var seg Segment
		var source, refMode string
		var tree []byte
		if err := rows.Scan(&seg.ID, &seg.Ordinal, &seg.Name, &source, &tree, &refMode); err != nil {
			return nil, fmt.Errorf("segment: list: scan: %w", err)
		}
		seg.Predicate = &predicate.Predicate{Source: source, Tree: tree}
		seg.RefreshMode = RefreshMode(refMode)
		segments = append(segments, &seg)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("segment: list: %w", err)
	}
	return segments, nil
}

// referencesEventAggregate walks a's identifiers and reports whether any of them names a registered
// attribute sourced from an event aggregate — the condition CW-0005 Unit 5 excludes from the
// incremental path, since that attribute's rollup can move with the passage of time alone.
func referencesEventAggregate(a *celast.AST, reg *registry.Registry) bool {
	found := false
	visitor := celast.NewExprVisitor(func(e celast.Expr) {
		if found || e.Kind() != celast.IdentKind {
			return
		}
		def, ok := reg.Lookup(e.AsIdent())
		if ok && def.Source == registry.SourceEventAggregate {
			found = true
		}
	})
	celast.PreOrderVisit(a.Expr(), visitor)
	return found
}
