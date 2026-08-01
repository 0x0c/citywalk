// Package eval implements CW-0004 Unit 3: row-wise evaluation of a compiled Predicate against one
// channel's attributes, with a program cache keyed by the predicate tree's content hash so a
// predicate shared by several messages compiles to an executable CEL program once.
package eval

import (
	"fmt"
	"sync"

	"github.com/google/cel-go/cel"

	"github.com/0x0c/citywalk/internal/audience/predicate"
)

// Evaluator holds the compiled-program cache for one CEL environment. It is safe for concurrent use.
type Evaluator struct {
	env *cel.Env

	mu       sync.RWMutex
	programs map[string]cel.Program
}

// New returns an Evaluator that compiles predicates against env — the same environment
// (registry.BuildEnv result) the predicate was originally checked against.
func New(env *cel.Env) *Evaluator {
	return &Evaluator{env: env, programs: make(map[string]cel.Program)}
}

// Evaluate answers whether p's predicate holds against attributes, a map from attribute name (as
// declared in the registry) to its Go value. Evaluation performs no I/O: attributes must already
// hold every value the predicate can reference (CW-0004 Unit 3).
func (e *Evaluator) Evaluate(p *predicate.Predicate, attributes map[string]any) (bool, error) {
	program, err := e.program(p)
	if err != nil {
		return false, err
	}

	result, _, err := program.Eval(attributes)
	if err != nil {
		return false, fmt.Errorf("eval: evaluate predicate: %w", err)
	}
	matched, ok := result.Value().(bool)
	if !ok {
		return false, fmt.Errorf("eval: predicate evaluated to %T, want bool", result.Value())
	}
	return matched, nil
}

func (e *Evaluator) program(p *predicate.Predicate) (cel.Program, error) {
	e.mu.RLock()
	program, ok := e.programs[p.Hash]
	e.mu.RUnlock()
	if ok {
		return program, nil
	}

	ast, err := predicate.Load(p)
	if err != nil {
		return nil, err
	}
	program, err = e.env.Program(ast)
	if err != nil {
		return nil, fmt.Errorf("eval: plan program: %w", err)
	}

	e.mu.Lock()
	e.programs[p.Hash] = program
	e.mu.Unlock()
	return program, nil
}
