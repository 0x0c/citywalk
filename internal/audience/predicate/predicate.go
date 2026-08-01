package predicate

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"

	"github.com/google/cel-go/cel"
	celast "github.com/google/cel-go/common/ast"
	"github.com/google/cel-go/common/operators"
	exprpb "google.golang.org/genproto/googleapis/api/expr/v1alpha1"
	"google.golang.org/protobuf/encoding/protojson"

	"github.com/0x0c/citywalk/internal/audience/registry"
)

// Predicate is a saved audience condition: its parsed-and-checked tree, kept as the JSON encoding of
// a CEL checked expression, and the source text it was compiled from (CW-0004 Unit 2). The tree is
// what Program and sqlcompile.CompileToSQL run against; the source is kept for display only.
type Predicate struct {
	Source string
	// Tree is the protojson encoding of the CEL CheckedExpr — the serialized abstract syntax tree.
	Tree []byte
	// Hash is the hex-encoded SHA-256 of Tree, used to key the row-wise compilation cache
	// (CW-0004 Unit 3) so a predicate shared by several messages compiles once.
	Hash string
}

// comparisonOperators are the CEL operators validateSemVerUsage checks: every place a wrong
// (lexical) ordering could silently replace the semantic one CW-0004 Unit 1 requires.
var comparisonOperators = map[string]bool{
	operators.Equals:        true,
	operators.NotEquals:     true,
	operators.Less:          true,
	operators.LessEquals:    true,
	operators.Greater:       true,
	operators.GreaterEquals: true,
}

// Compile parses and type-checks source against env, rejects a comparison that touches a
// SemVer-typed attribute directly instead of through semver(...), and returns the resulting
// Predicate. A save that returns an error must not persist the predicate — CW-0003 and CW-0004 both
// reject rather than warn at save time.
func Compile(env *cel.Env, reg *registry.Registry, source string) (*Predicate, error) {
	ast, iss := env.Compile(source)
	if iss != nil && iss.Err() != nil {
		return nil, fmt.Errorf("predicate: compile: %w", iss.Err())
	}

	if err := validateSemVerUsage(ast.NativeRep(), reg); err != nil {
		return nil, err
	}

	checkedExpr, err := cel.AstToCheckedExpr(ast)
	if err != nil {
		return nil, fmt.Errorf("predicate: convert to checked expression: %w", err)
	}
	tree, err := protojson.Marshal(checkedExpr)
	if err != nil {
		return nil, fmt.Errorf("predicate: marshal tree: %w", err)
	}
	sum := sha256.Sum256(tree)

	return &Predicate{
		Source: source,
		Tree:   tree,
		Hash:   hex.EncodeToString(sum[:]),
	}, nil
}

// Load decodes p.Tree back into a checked *cel.Ast, ready for env.Program or sqlcompile.CompileToSQL.
// env must be built (registry.BuildEnv) from the same registry the predicate was compiled against.
func Load(p *Predicate) (*cel.Ast, error) {
	var checkedExpr exprpb.CheckedExpr
	if err := protojson.Unmarshal(p.Tree, &checkedExpr); err != nil {
		return nil, fmt.Errorf("predicate: unmarshal tree: %w", err)
	}
	return cel.CheckedExprToAst(&checkedExpr), nil
}

// validateSemVerUsage rejects any comparison operator whose operand is a bare reference to a
// SemVer-typed attribute — the mistake that would otherwise compile cleanly and then compare
// versions lexically, exactly the failure CW-0004 Unit 1 exists to prevent.
func validateSemVerUsage(a *celast.AST, reg *registry.Registry) error {
	var firstErr error
	visitor := celast.NewExprVisitor(func(e celast.Expr) {
		if firstErr != nil || e.Kind() != celast.CallKind {
			return
		}
		call := e.AsCall()
		if !comparisonOperators[call.FunctionName()] {
			return
		}
		for _, arg := range call.Args() {
			if arg.Kind() != celast.IdentKind {
				continue
			}
			name := arg.AsIdent()
			def, ok := reg.Lookup(name)
			if ok && def.Type == registry.TypeSemVer {
				firstErr = fmt.Errorf(
					"predicate: %q is a semver attribute and must be compared via semver(...), e.g. semver(%s) >= semver(\"1.2.3\")",
					name, name,
				)
				return
			}
		}
	})
	celast.PreOrderVisit(a.Expr(), visitor)
	return firstErr
}
