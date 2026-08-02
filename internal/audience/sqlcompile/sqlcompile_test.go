package sqlcompile_test

import (
	"strings"
	"testing"
	"time"

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

// compileToSQLError compiles source as a predicate and returns the error CompileToSQL rejects it
// with. It fails the test if the predicate itself does not compile, so a test asserting a sqlcompile
// rejection can never quietly pass because CEL rejected the source first.
func compileToSQLError(t *testing.T, source string) error {
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
	sql, _, err := sqlcompile.CompileToSQL(ast.NativeRep(), reg)
	if err == nil {
		t.Fatalf("CompileToSQL(%q) = %q, want a rejection", source, sql)
	}
	return err
}

// TestCompileToSQLBindsATimestampLiteralAsATime is CW-0004 Unit 4's timestamp support: the RFC 3339
// text a predicate author writes is parsed in Go and bound as a time.Time, so Postgres compares
// against a real timestamptz rather than re-parsing a string with its own date rules.
func TestCompileToSQLBindsATimestampLiteralAsATime(t *testing.T) {
	sql, args := compileToSQL(t, `registered_at > timestamp("2026-01-05T12:00:00Z")`)

	want := time.Date(2026, 1, 5, 12, 0, 0, 0, time.UTC)
	found := false
	for _, a := range args {
		if got, ok := a.(time.Time); ok {
			if !got.Equal(want) {
				t.Errorf("timestamp argument = %v, want %v", got, want)
			}
			found = true
		}
	}
	if !found {
		t.Errorf("args = %v, want a time.Time among them", args)
	}
	if !strings.Contains(sql, "::timestamptz") {
		t.Errorf("sql = %q, want the attribute side cast to timestamptz", sql)
	}
}

// TestCompileToSQLRejectsTimestampOverANonLiteral holds Unit 4's reject-rather-than-accept rule for
// the one timestamp() shape this compiler has no SQL for: a runtime string argument would need
// Postgres to reproduce Go's RFC 3339 parsing, so it is named as unsupported instead.
func TestCompileToSQLRejectsTimestampOverANonLiteral(t *testing.T) {
	err := compileToSQLError(t, `registered_at > timestamp(country)`)
	if !strings.Contains(err.Error(), "timestamp()") {
		t.Errorf("error = %q, want it to name timestamp()", err)
	}
}

// TestCompileToSQLNormalizesASemVerLiteralButDefersToSQLForAnAttribute is the asymmetry
// compileSemVer exists for: a literal is already known at compile time and is normalized in Go, while
// an attribute's value is only known per row and has to reach the database's semver_normalize.
func TestCompileToSQLNormalizesASemVerLiteralButDefersToSQLForAnAttribute(t *testing.T) {
	sql, args := compileToSQL(t, `semver(app_version) >= semver("1.2.3")`)

	if !strings.Contains(sql, "semver_normalize(") {
		t.Errorf("sql = %q, want the attribute side wrapped in semver_normalize()", sql)
	}
	normalized, err := registry.NormalizeSemVer("1.2.3")
	if err != nil {
		t.Fatalf("NormalizeSemVer: %v", err)
	}
	found := false
	for _, a := range args {
		if a == normalized {
			found = true
		}
		if a == "1.2.3" && normalized != "1.2.3" {
			t.Errorf("args = %v, want the literal normalized in Go, not passed through raw", args)
		}
	}
	if !found {
		t.Errorf("args = %v, want the normalized literal %q among them", args, normalized)
	}
}

func TestCompileToSQLRejectsAnUnparseableSemVerLiteral(t *testing.T) {
	err := compileToSQLError(t, `semver(app_version) >= semver("not a version")`)
	if !strings.Contains(err.Error(), "not a version") {
		t.Errorf("error = %q, want it to quote the value it could not parse", err)
	}
}

// TestCompileToSQLCompilesInOverALiteralListToAnArrayArgument keeps a list membership test as one
// bound array argument rather than an inlined IN (...) list, so a predicate with a hundred values
// still compiles to a single placeholder.
func TestCompileToSQLCompilesInOverALiteralListToAnArrayArgument(t *testing.T) {
	sql, args := compileToSQL(t, `country in ["JP", "US"]`)

	if !strings.Contains(sql, "= ANY(") {
		t.Errorf("sql = %q, want it to use = ANY() over a bound array", sql)
	}
	found := false
	for _, a := range args {
		list, ok := a.([]string)
		if !ok {
			continue
		}
		found = true
		if len(list) != 2 || list[0] != "JP" || list[1] != "US" {
			t.Errorf("list argument = %v, want [JP US] in source order", list)
		}
	}
	if !found {
		t.Errorf("args = %v, want a []string among them", args)
	}
}

// TestCompileToSQLCompilesInOverAStringSetAttributeToJSONBContainment is the other half of
// compileIn: a string_set attribute is a jsonb array in channels.attributes, so membership is the
// jsonb ? operator rather than an array comparison — and the needle, not the key, is what varies.
func TestCompileToSQLCompilesInOverAStringSetAttributeToJSONBContainment(t *testing.T) {
	sql, args := compileToSQL(t, `"kamakura" in favorite_routes`)

	if !strings.Contains(sql, "attributes->") || !strings.Contains(sql, "?") {
		t.Errorf("sql = %q, want a jsonb ? containment test against the attributes column", sql)
	}
	want := map[string]bool{"favorite_routes": false, "kamakura": false}
	for _, a := range args {
		s, ok := a.(string)
		if ok {
			if _, expected := want[s]; expected {
				want[s] = true
			}
		}
	}
	for name, seen := range want {
		if !seen {
			t.Errorf("args = %v, want %q among them", args, name)
		}
	}
}

// TestCompileToSQLRejectsInOverANonStringSetAttribute names the construct rather than emitting SQL
// that would fail at batch time: only a string_set attribute has a jsonb array to test membership in.
func TestCompileToSQLRejectsInOverANonStringSetAttribute(t *testing.T) {
	env, err := audiencetest.Env()
	if err != nil {
		t.Fatalf("Env: %v", err)
	}
	reg := audiencetest.Registry()
	p, err := predicate.Compile(env, reg, `"kamakura" in favorite_routes`)
	if err != nil {
		t.Fatalf("Compile: %v", err)
	}
	ast, err := predicate.Load(p)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}

	// Recompile the same tree against a registry that types favorite_routes as a plain string, the
	// state a registry edit could leave behind: sqlcompile must reject it rather than emit a jsonb
	// containment test against a scalar column.
	retyped, err := registry.New(
		registry.Definition{Name: "favorite_routes", Type: registry.TypeString, Source: registry.SourceTag},
	)
	if err != nil {
		t.Fatalf("registry.New: %v", err)
	}
	if _, _, err := sqlcompile.CompileToSQL(ast.NativeRep(), retyped); err == nil {
		t.Fatal("CompileToSQL: got nil error for an 'in' over a non-string_set attribute, want a rejection")
	}
}

// TestCompileToSQLRejectsANonStringListElement covers literalStringList's element check: the array
// argument compileIn binds is a []string, so a numeric list has no representation here.
func TestCompileToSQLRejectsANonStringListElement(t *testing.T) {
	err := compileToSQLError(t, `total_distance_km in [1.0, 2.0]`)
	if !strings.Contains(err.Error(), "list element") {
		t.Errorf("error = %q, want it to name the offending list element", err)
	}
}

// TestCompileToSQLNamesAnUnsupportedConstructByKind is Unit 4's rejection contract: a construct this
// compiler does not implement is refused with its kind spelled out, so an author reading the error
// knows what to rewrite rather than seeing an opaque failure during a nightly batch.
func TestCompileToSQLNamesAnUnsupportedConstructByKind(t *testing.T) {
	err := compileToSQLError(t, `favorite_routes.exists(r, r == "kamakura")`)
	if !strings.Contains(err.Error(), "comprehension") {
		t.Errorf("error = %q, want it to name the construct kind (comprehension)", err)
	}
}
