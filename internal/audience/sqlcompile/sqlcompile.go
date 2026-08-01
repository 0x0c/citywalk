// Package sqlcompile implements CW-0004 Unit 4's set-wise backend: compiling the same tree Unit 3
// evaluates row-wise into a PostgreSQL WHERE clause, against the minimal channels table
// migrations/0003_audience_channels.sql defines. A construct this compiler does not support is
// rejected with a message naming it, per Unit 4 — never silently accepted and left to fail during a
// nightly batch.
package sqlcompile

import (
	"fmt"
	"time"

	celast "github.com/google/cel-go/common/ast"
	"github.com/google/cel-go/common/operators"
	"github.com/google/cel-go/common/types/ref"

	"github.com/0x0c/citywalk/internal/audience/registry"
)

// comparisonSQL maps a CEL comparison operator to its SQL spelling. CEL and SQL agree on every
// symbol here except equality and inequality.
var comparisonSQL = map[string]string{
	operators.Equals:        "=",
	operators.NotEquals:     "<>",
	operators.Less:          "<",
	operators.LessEquals:    "<=",
	operators.Greater:       ">",
	operators.GreaterEquals: ">=",
}

type compiler struct {
	reg  *registry.Registry
	args []any
}

// CompileToSQL compiles a's expression into a PostgreSQL boolean expression suitable for a WHERE
// clause, referencing the channels table's attributes jsonb column, plus the positional arguments
// ($1, $2, ...) the expression's placeholders refer to.
func CompileToSQL(a *celast.AST, reg *registry.Registry) (string, []any, error) {
	c := &compiler{reg: reg}
	sql, err := c.compile(a.Expr())
	if err != nil {
		return "", nil, err
	}
	return sql, c.args, nil
}

func (c *compiler) compile(e celast.Expr) (string, error) {
	switch e.Kind() {
	case celast.CallKind:
		return c.compileCall(e.AsCall())
	case celast.IdentKind:
		return c.compileIdentScalar(e.AsIdent())
	case celast.LiteralKind:
		return c.compileLiteral(e.AsLiteral())
	default:
		return "", fmt.Errorf("sqlcompile: unsupported construct: %s", kindName(e.Kind()))
	}
}

func (c *compiler) compileCall(call celast.CallExpr) (string, error) {
	fn := call.FunctionName()
	args := call.Args()

	switch fn {
	case operators.LogicalAnd, operators.LogicalOr:
		if len(args) != 2 {
			return "", fmt.Errorf("sqlcompile: %s takes 2 arguments, got %d", fn, len(args))
		}
		left, err := c.compile(args[0])
		if err != nil {
			return "", err
		}
		right, err := c.compile(args[1])
		if err != nil {
			return "", err
		}
		joiner := " AND "
		if fn == operators.LogicalOr {
			joiner = " OR "
		}
		return "(" + left + joiner + right + ")", nil

	case operators.LogicalNot:
		if len(args) != 1 {
			return "", fmt.Errorf("sqlcompile: %s takes 1 argument, got %d", fn, len(args))
		}
		inner, err := c.compile(args[0])
		if err != nil {
			return "", err
		}
		return "(NOT " + inner + ")", nil

	case operators.In:
		if len(args) != 2 {
			return "", fmt.Errorf("sqlcompile: %s takes 2 arguments, got %d", fn, len(args))
		}
		return c.compileIn(args[0], args[1])

	case semverFunctionName:
		if len(args) != 1 {
			return "", fmt.Errorf("sqlcompile: %s takes 1 argument, got %d", fn, len(args))
		}
		return c.compileSemVer(args[0])

	case timestampFunctionName:
		if len(args) != 1 {
			return "", fmt.Errorf("sqlcompile: %s takes 1 argument, got %d", fn, len(args))
		}
		return c.compileTimestamp(args[0])

	default:
		if sqlOp, ok := comparisonSQL[fn]; ok {
			if len(args) != 2 {
				return "", fmt.Errorf("sqlcompile: %s takes 2 arguments, got %d", fn, len(args))
			}
			left, err := c.compile(args[0])
			if err != nil {
				return "", err
			}
			right, err := c.compile(args[1])
			if err != nil {
				return "", err
			}
			return "(" + left + " " + sqlOp + " " + right + ")", nil
		}
		return "", fmt.Errorf("sqlcompile: unsupported construct: function %q", fn)
	}
}

// semverFunctionName mirrors predicate.semverFunctionName; duplicated as a string constant rather
// than imported to avoid a package cycle (predicate imports registry, sqlcompile imports registry —
// neither needs to import the other, and the function name is part of the predicate language's
// public surface, not an implementation detail either package owns exclusively).
const semverFunctionName = "semver"

// timestampFunctionName is CEL's standard library conversion function, timestamp(string) ->
// timestamp. sqlcompile supports it only over a string literal — the only shape a valid predicate
// can use it in, since a channel's own timestamp-typed attributes are already declared as CEL
// TimestampType and have no overload accepting a further timestamp(...) wrap.
const timestampFunctionName = "timestamp"

func (c *compiler) compileTimestamp(argExpr celast.Expr) (string, error) {
	if argExpr.Kind() != celast.LiteralKind {
		return "", fmt.Errorf("sqlcompile: unsupported construct: timestamp() over a non-literal argument")
	}
	s, ok := argExpr.AsLiteral().Value().(string)
	if !ok {
		return "", fmt.Errorf("sqlcompile: timestamp() argument must be a string literal")
	}
	t, err := time.Parse(time.RFC3339, s)
	if err != nil {
		return "", fmt.Errorf("sqlcompile: timestamp() argument %q is not RFC 3339: %w", s, err)
	}
	return c.placeholder(t), nil
}

func (c *compiler) compileSemVer(argExpr celast.Expr) (string, error) {
	// A literal argument is normalized in Go at compile time, so the SQL never has to reproduce
	// NormalizeSemVer's edge-case handling for a value that is already known.
	if argExpr.Kind() == celast.LiteralKind {
		s, ok := argExpr.AsLiteral().Value().(string)
		if !ok {
			return "", fmt.Errorf("sqlcompile: semver() argument must be a string literal or attribute")
		}
		normalized, err := registry.NormalizeSemVer(s)
		if err != nil {
			return "", fmt.Errorf("sqlcompile: %w", err)
		}
		return c.placeholder(normalized), nil
	}

	inner, err := c.compile(argExpr)
	if err != nil {
		return "", err
	}
	return "semver_normalize(" + inner + ")", nil
}

func (c *compiler) compileIn(needleExpr, haystackExpr celast.Expr) (string, error) {
	needleSQL, err := c.compile(needleExpr)
	if err != nil {
		return "", err
	}

	switch haystackExpr.Kind() {
	case celast.ListKind:
		elems, err := literalStringList(haystackExpr.AsList())
		if err != nil {
			return "", err
		}
		return needleSQL + " = ANY(" + c.placeholder(elems) + ")", nil

	case celast.IdentKind:
		name := haystackExpr.AsIdent()
		def, ok := c.reg.Lookup(name)
		if !ok {
			return "", fmt.Errorf("sqlcompile: unknown attribute %q", name)
		}
		if def.Type != registry.TypeStringSet {
			return "", fmt.Errorf("sqlcompile: %q is not a string_set attribute, cannot be an 'in' haystack", name)
		}
		return "(attributes->" + c.placeholder(name) + ") ? (" + needleSQL + ")", nil

	default:
		return "", fmt.Errorf("sqlcompile: unsupported construct: 'in' over %s", kindName(haystackExpr.Kind()))
	}
}

func literalStringList(list celast.ListExpr) ([]string, error) {
	elems := list.Elements()
	out := make([]string, 0, len(elems))
	for _, elem := range elems {
		if elem.Kind() != celast.LiteralKind {
			return nil, fmt.Errorf("sqlcompile: unsupported construct: non-literal list element")
		}
		s, ok := elem.AsLiteral().Value().(string)
		if !ok {
			return nil, fmt.Errorf("sqlcompile: unsupported construct: non-string list element")
		}
		out = append(out, s)
	}
	return out, nil
}

// compileIdentScalar compiles a bare attribute reference into a scalar SQL expression: the jsonb
// attributes column read by key and cast to the attribute's registered type, or — for an
// event-aggregate-sourced attribute (CW-0004 Unit 5) — a correlated subquery summing CW-0009's
// targeting_rollup instead, since that attribute's value was never written into channels.attributes
// at all. A string_set attribute has no scalar form — it is only meaningful as an 'in' haystack — so
// referencing one directly is a rejection naming the construct.
func (c *compiler) compileIdentScalar(name string) (string, error) {
	def, ok := c.reg.Lookup(name)
	if !ok {
		return "", fmt.Errorf("sqlcompile: unknown attribute %q", name)
	}

	if def.Source == registry.SourceEventAggregate {
		return c.compileEventAggregate(def)
	}

	column := "(attributes->>" + c.placeholder(name) + ")"
	switch def.Type {
	case registry.TypeString, registry.TypeSemVer:
		return column, nil
	case registry.TypeNumber:
		return column + "::double precision", nil
	case registry.TypeBool:
		return column + "::boolean", nil
	case registry.TypeTimestamp:
		return column + "::timestamptz", nil
	case registry.TypeStringSet:
		return "", fmt.Errorf("sqlcompile: unsupported construct: %q is a string_set attribute used outside 'in'", name)
	default:
		return "", fmt.Errorf("sqlcompile: unknown attribute type %q for %q", def.Type, name)
	}
}

// compileEventAggregate compiles def (Source == SourceEventAggregate) into a correlated subquery
// over targeting_rollup, summing def.AggregateEventName's daily counts across the trailing
// def.AggregateWindowDays days. It assumes the enclosing query aliases the channels table as "c" —
// the one place sqlcompile's output is coupled to its caller's query shape, matching
// batch.recomputeSegment's `FROM channel_ordinals co JOIN channels c ON c.id = co.channel_id`.
//
// AggregateGranularity is always "day" today (the only granularity CW-0009's rollup carries), and
// AggregateWindowDays is always a whole number of days, so there is no predicate-level way to ask
// for finer precision than the rollup stores — the type-checker rejection CW-0004 Unit 5 describes
// only gets something to reject once a second, finer granularity exists to be confused with this
// one.
func (c *compiler) compileEventAggregate(def registry.Definition) (string, error) {
	// pgx binds a Go int as bigint by default, and Postgres has no date - bigint operator (only
	// date - integer), so the window-days placeholder needs an explicit cast.
	return fmt.Sprintf(
		"(SELECT COALESCE(sum(count), 0)::double precision FROM targeting_rollup WHERE channel_id = c.id AND event_name = %s AND day > current_date - %s::int)",
		c.placeholder(def.AggregateEventName), c.placeholder(def.AggregateWindowDays),
	), nil
}

func (c *compiler) compileLiteral(v ref.Val) (string, error) {
	switch value := v.Value().(type) {
	case bool, float64, string, time.Time:
		return c.placeholder(value), nil
	case int64:
		return c.placeholder(value), nil
	case uint64:
		return c.placeholder(value), nil
	default:
		return "", fmt.Errorf("sqlcompile: unsupported construct: literal of type %T", value)
	}
}

func (c *compiler) placeholder(arg any) string {
	c.args = append(c.args, arg)
	return fmt.Sprintf("$%d", len(c.args))
}

func kindName(k celast.ExprKind) string {
	switch k {
	case celast.CallKind:
		return "call"
	case celast.ComprehensionKind:
		return "comprehension"
	case celast.IdentKind:
		return "identifier"
	case celast.ListKind:
		return "list"
	case celast.LiteralKind:
		return "literal"
	case celast.MapKind:
		return "map"
	case celast.SelectKind:
		return "field selection"
	case celast.StructKind:
		return "struct"
	default:
		return "unknown"
	}
}
