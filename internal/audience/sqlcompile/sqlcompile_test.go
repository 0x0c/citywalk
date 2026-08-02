package sqlcompile_test

import (
	"strings"
	"testing"

	"github.com/0x0c/citywalk/internal/audience/audiencetest"
	"github.com/0x0c/citywalk/internal/audience/predicate"
	"github.com/0x0c/citywalk/internal/audience/registry"
	"github.com/0x0c/citywalk/internal/audience/sqlcompile"
)

func compileToSQL(t *testing.T, source string) (string, []any) {
	t.Helper()
	env, err := audiencetest.Env()
	if err != nil {
		t.Fatalf("Env: %v", err)
	}
	reg := audiencetest.Registry()
	p, err := predicate.Compile(env, reg, source)
	if err != nil {
		t.Fatalf("Compile(%q): %v", source, err)
	}
	ast, err := predicate.Load(p)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	sql, args, err := sqlcompile.CompileToSQL(ast.NativeRep(), reg)
	if err != nil {
		t.Fatalf("CompileToSQL(%q): %v", source, err)
	}
	return sql, args
}

func TestCompileToSQLProducesPlaceholdersNotInlineLiterals(t *testing.T) {
	sql, args := compileToSQL(t, `country == "JP"`)
	// Both the attribute key and the literal travel as arguments — attributes->>$1 for the key,
	// $2 for the comparison value — never inlined into the SQL text.
	if len(args) != 2 || args[0] != "country" || args[1] != "JP" {
		t.Fatalf(`args = %v, want ["country" "JP"]`, args)
	}
	if !strings.Contains(sql, "$1") || !strings.Contains(sql, "$2") {
		t.Errorf("sql = %q, want it to reference $1 and $2", sql)
	}
	if strings.Contains(sql, "JP") || strings.Contains(sql, "country") {
		t.Errorf("sql = %q, want the literal and key kept out of the SQL text (they must travel as arguments)", sql)
	}
}

// TestCompileToSQLCompilesEventAggregateAsACorrelatedSubquery is CW-0004 Unit 5's set-wise half: an
// event-aggregate-sourced attribute must compile to a subquery over targeting_rollup correlated to
// the enclosing query's channel row, not a read of the channels.attributes column every other source
// resolves to — that column never holds this value at all.
func TestCompileToSQLCompilesEventAggregateAsACorrelatedSubquery(t *testing.T) {
	sql, args := compileToSQL(t, `route_screen_views_7d >= 3.0`)

	if !strings.Contains(sql, "targeting_rollup") {
		t.Errorf("sql = %q, want it to reference targeting_rollup", sql)
	}
	if !strings.Contains(sql, "channel_id = c.id") {
		t.Errorf("sql = %q, want a correlated subquery keyed on the enclosing query's channel row (c.id)", sql)
	}
	if strings.Contains(sql, "attributes->>") {
		t.Errorf("sql = %q, want no read of channels.attributes for an event-aggregate attribute", sql)
	}

	found := map[string]bool{}
	for _, a := range args {
		if s, ok := a.(string); ok {
			found[s] = true
		}
	}
	if !found["route_screen_view"] {
		t.Errorf("args = %v, want the registered event name %q among them", args, "route_screen_view")
	}
}

func TestCompileToSQLRejectsUnknownAttribute(t *testing.T) {
	env, err := audiencetest.Env()
	if err != nil {
		t.Fatalf("Env: %v", err)
	}
	// Deliberately compile against a registry that lacks an attribute the SQL compiler must still
	// reject on its own — CompileToSQL should not trust that Compile already checked this name.
	reg := audiencetest.Registry()
	p, err := predicate.Compile(env, reg, `country == "JP"`)
	if err != nil {
		t.Fatalf("Compile: %v", err)
	}
	ast, err := predicate.Load(p)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}

	emptyReg, err := registry.New()
	if err != nil {
		t.Fatalf("registry.New: %v", err)
	}
	if _, _, err := sqlcompile.CompileToSQL(ast.NativeRep(), emptyReg); err == nil {
		t.Fatal("CompileToSQL: got nil error against a registry with no attributes, want an error")
	}
}

func TestCompileToSQLRejectsStringSetOutsideIn(t *testing.T) {
	env, err := audiencetest.Env()
	if err != nil {
		t.Fatalf("Env: %v", err)
	}
	reg := audiencetest.Registry()
	// favorite_routes is a string_set; CEL type-checks equality against a list, but sqlcompile has
	// no scalar form for it and must reject the construct by name rather than emit invalid SQL.
	p, err := predicate.Compile(env, reg, `favorite_routes == favorite_routes`)
	if err != nil {
		t.Fatalf("Compile: %v", err)
	}
	ast, err := predicate.Load(p)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}

	if _, _, err := sqlcompile.CompileToSQL(ast.NativeRep(), reg); err == nil {
		t.Fatal("CompileToSQL: got nil error for a bare string_set reference, want a rejection")
	}
}
