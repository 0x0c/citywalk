// Package predicate implements CW-0004 Unit 2: a saved predicate is stored as its abstract syntax
// tree, alongside the source text it was parsed from. The tree is what a Compile call and, later, an
// evaluator run; the source is documentation.
package predicate

import (
	"fmt"

	"github.com/google/cel-go/cel"
	"github.com/google/cel-go/common/types"
	"github.com/google/cel-go/common/types/ref"

	"github.com/0x0c/citywalk/internal/audience/registry"
)

// semverFunctionName is the CEL function name predicate authors call to compare a semver-typed
// attribute correctly: semver(app_version) >= semver("2.10.0"). Declaring semver attributes as CEL
// strings and requiring this explicit wrap — rather than a distinct CEL type with overloaded
// operators — keeps the type declarations and the SQL compiler simple, at the cost of authors
// writing semver(...) rather than a bare comparison; validateSemVerUsage in predicate.go is what
// turns a forgotten wrap into a save-time rejection instead of a silently wrong lexical comparison.
const semverFunctionName = "semver"

// BuildEnv constructs the CEL environment a predicate for reg is parsed, checked, and evaluated
// against: one variable per registered attribute, plus the semver() conversion function.
func BuildEnv(reg *registry.Registry) (*cel.Env, error) {
	var opts []cel.EnvOption
	for _, def := range reg.Definitions() {
		celType, err := registry.CELType(def.Type)
		if err != nil {
			return nil, err
		}
		opts = append(opts, cel.Variable(def.Name, celType))
	}
	opts = append(opts, cel.Function(semverFunctionName,
		cel.Overload(semverFunctionName+"_string", []*cel.Type{cel.StringType}, cel.StringType,
			cel.UnaryBinding(semverBinding),
		),
	))

	env, err := cel.NewEnv(opts...)
	if err != nil {
		return nil, fmt.Errorf("predicate: build environment: %w", err)
	}
	return env, nil
}

func semverBinding(val ref.Val) ref.Val {
	s, ok := val.(types.String)
	if !ok {
		return types.MaybeNoSuchOverloadErr(val)
	}
	normalized, err := registry.NormalizeSemVer(string(s))
	if err != nil {
		return types.NewErr("%s", err.Error())
	}
	return types.String(normalized)
}
