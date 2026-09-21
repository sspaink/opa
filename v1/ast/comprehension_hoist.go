// Copyright 2025 The OPA Authors.  All rights reserved.
// Use of this source code is governed by an Apache2
// license that can be found in the LICENSE file.

package ast

import (
	"github.com/open-policy-agent/opa/internal/debug"
)

// sideEffectBuiltins are built-in functions whose value is in being called, not
// in what they return. Hoisting them changes how many times they fire, so they
// are never treated as loop invariants.
var sideEffectBuiltins = map[string]struct{}{
	Trace.Name:            {},
	Print.Name:            {},
	InternalPrint.Name:    {},
	InternalTestCase.Name: {},
}

// hoistComprehensionInvariants moves loop-invariant expressions to the front of
// the comprehension bodies found under node. An expression is loop invariant if
// every variable it reads is already bound outside the comprehension, so the
// value it produces is the same on every iteration of the comprehension's
// generators. Evaluating it first means evaluating it once per comprehension
// instead of once per iteration:
//
//	p[x] = y if {
//		foo[x]
//		y = {v | bar[u] = v; f(x) = u}   # f(x) is called len(bar) times
//	}
//
//	p[x] = y if {
//		foo[x]
//		y = {v | f(x) = u; bar[u] = v}   # f(x) is called once, bar[u] is a lookup
//	}
//
// Hoisting stops at the comprehension boundary on purpose. Lifting the
// expression into the enclosing body would be visible to callers: an undefined
// f(x) yields an empty comprehension today, but would make the whole rule
// undefined once the call is a sibling of the comprehension.
//
// Candidates are restricted to expressions with at most one solution so that
// the element order of array comprehensions is preserved. Nothing moves ahead
// of a call whose evaluation is observable, so the number of times a
// side-effecting or non-deterministic built-in fires cannot change.
//
// An expression that moves ahead of a generator is evaluated even when that
// generator turns out to have no solutions, so under strict built-in errors it
// could raise an error the statement order as written never reached. Every body
// that is rewritten is therefore recorded in sourceOrder as it was written, and
// the evaluator falls back to that copy when strict built-in errors are on.
//
// Bodies of the comprehensions in indexed are left alone. Indexing evaluates a
// comprehension once in total rather than once per binding of the enclosing
// body, so it beats hoisting, and hoisting would undo it: indexing requires a
// body that is safe on its own, which an expression closing over the enclosing
// body is not.
func hoistComprehensionInvariants(
	dbg debug.Debug,
	builtins map[string]*Builtin,
	arity func(Ref) int,
	globals VarSet,
	node Body,
	indexed map[Value]struct{},
	sourceOrder map[Value]Body,
) uint64 {
	if !ContainsComprehensions(node) {
		return 0
	}

	h := invariantHoister{
		dbg:         dbg,
		builtins:    builtins,
		arity:       arity,
		indexed:     indexed,
		sourceOrder: sourceOrder,
	}
	h.walkBody(node, globals.Copy())

	return h.hoisted
}

// indexedComprehensions keys the comprehension index by the comprehension
// itself, which is what the hoister has on hand when it reaches one.
func indexedComprehensions(indices map[*Term]*ComprehensionIndex) map[Value]struct{} {
	if len(indices) == 0 {
		return nil
	}

	indexed := make(map[Value]struct{}, len(indices))
	for term := range indices {
		indexed[term.Value] = struct{}{}
	}

	return indexed
}

type invariantHoister struct {
	dbg         debug.Debug
	builtins    map[string]*Builtin
	arity       func(Ref) int
	indexed     map[Value]struct{}
	sourceOrder map[Value]Body
	hoisted     uint64
}

// walkBody threads the set of bound variables through body and hoists the
// invariants of every comprehension reachable from it. safe is mutated.
func (h *invariantHoister) walkBody(body Body, safe VarSet) {
	last := -1
	for i, expr := range body {
		if ContainsComprehensions(expr) {
			last = i
		}
	}

	if last < 0 {
		return
	}

	vis := varVisitorPool.Get()
	defer varVisitorPool.Put(vis)

	output := VarSet{}

	for i, expr := range body[:last+1] {
		h.walkExpr(expr, safe)
		if i < last {
			safe.Update(outputVarsForExpr(expr, h.arity, safe, output, vis.Clear()))
		}
	}
}

// walkExpr descends into the closures of expr, hoisting inside each one. The
// generic visitor is not used for the descent because every closure kind needs
// a different view of what is bound on entry.
func (h *invariantHoister) walkExpr(expr *Expr, safe VarSet) {
	for _, w := range expr.With {
		h.walkTerm(w.Value, safe)
	}

	switch terms := expr.Terms.(type) {
	case *Every:
		h.walkTerm(terms.Domain, safe)
		inner := safe.Copy()
		inner.Update(terms.KeyValueVars())
		h.walkBody(terms.Body, inner)
	case *SomeDecl:
		// Nothing to descend into: the symbols are declarations, and a `some x
		// in xs` domain has already been rewritten into an ordinary expression.
	case *Not:
		h.walkBody(terms.Body, safe.Copy())
	case *LogicalAnd:
		h.walkBody(terms.Lhs, safe.Copy())
		h.walkBody(terms.Rhs, safe.Copy())
	case *LogicalOr:
		h.walkBody(terms.Lhs, safe.Copy())
		h.walkBody(terms.Rhs, safe.Copy())
	case *Term:
		h.walkTerm(terms, safe)
	case []*Term:
		for _, t := range terms {
			h.walkTerm(t, safe)
		}
	}
}

func (h *invariantHoister) walkTerm(term *Term, safe VarSet) {
	WalkClosures(term, func(x any) bool {
		switch x := x.(type) {
		case *ArrayComprehension:
			x.Body = h.hoist(x.Body, safe, x)
			h.walkComprehension(x.Body, safe, x.Term)
		case *SetComprehension:
			x.Body = h.hoist(x.Body, safe, x)
			h.walkComprehension(x.Body, safe, x.Term)
		case *ObjectComprehension:
			x.Body = h.hoist(x.Body, safe, x)
			h.walkComprehension(x.Body, safe, x.Key, x.Value)
		case *Every:
			h.walkTerm(x.Domain, safe)
			inner := safe.Copy()
			inner.Update(x.KeyValueVars())
			h.walkBody(x.Body, inner)
		case *Not:
			h.walkBody(x.Body, safe.Copy())
		case *LogicalAnd:
			h.walkBody(x.Lhs, safe.Copy())
			h.walkBody(x.Rhs, safe.Copy())
		case *LogicalOr:
			h.walkBody(x.Lhs, safe.Copy())
			h.walkBody(x.Rhs, safe.Copy())
		}
		return true
	})
}

// walkComprehension recurses into an already-hoisted comprehension body and the
// head terms it produces, both of which may hold further comprehensions.
func (h *invariantHoister) walkComprehension(body Body, safe VarSet, head ...*Term) {
	inner := safe.Copy()
	h.walkBody(body, inner)
	for _, t := range head {
		h.walkTerm(t, inner)
	}
}

// hoist returns body with its loop-invariant expressions moved to the front.
// Expressions keep their relative order, so the result is the minimal
// reordering that puts every invariant ahead of every generator. Safety is
// preserved: an invariant is safe given outer plus the invariants already
// picked up, all of which precede it in the result, and the expressions left
// behind are still preceded by everything that preceded them before.
func (h *invariantHoister) hoist(body Body, outer VarSet, comprehension Value) Body {
	if len(body) < 2 {
		return body
	}

	if _, ok := h.indexed[comprehension]; ok {
		return body
	}

	vis := varVisitorPool.Get()
	defer varVisitorPool.Put(vis)

	invariant := make([]*Expr, 0, len(body))
	rest := make([]*Expr, 0, len(body))
	var moved uint64
	generator, blocked := false, false

	// Growing the safe set as expressions are picked up lets a hoisted call
	// carry the expression consuming its result along with it, which is what
	// turns an iterated reference into a lookup.
	safe := outer.Copy()
	output := VarSet{}

	bodyVars := bodyVarsOnce(body, h.arity)

	for _, expr := range body {
		switch {
		case blocked:
			rest = append(rest, expr)

		case h.impure(expr):
			// Nothing may move ahead of a call whose evaluation is observable:
			// the expression that moved could be undefined, and then the call
			// would never happen at all.
			blocked = true
			rest = append(rest, expr)

		case h.generates(expr, safe):
			// Expressions with more than one solution must stay put: making
			// one the outermost generator would reorder the elements of an
			// array comprehension. They are also the reason the rest of the
			// body runs repeatedly, so until one has been seen there is
			// nothing to hoist out of the way of.
			generator = true
			rest = append(rest, expr)

		case !h.isInvariant(expr, safe, bodyVars):
			rest = append(rest, expr)

		default:
			if generator {
				moved++
			}
			invariant = append(invariant, expr)
			safe.Update(outputVarsForExpr(expr, h.arity, safe, output, vis.Clear()))
		}
	}

	if moved == 0 {
		return body
	}

	h.hoisted += moved
	h.dbg.Printf("%s: hoisted %d loop-invariant expression(s) to the front of the comprehension body", body[0].Location, moved)

	// The reordering is only sound when built-in errors make an expression
	// undefined. Keep the body as written so the evaluator can fall back to it
	// under strict built-in errors. The copy is needed because NewBody
	// renumbers the expressions it is handed.
	h.sourceOrder[comprehension] = body.Copy()

	return NewBody(append(invariant, rest...)...)
}

// bodyVarsOnce returns an accessor for the variables of body, computed on first
// use. Only expressions that contain a closure need them.
func bodyVarsOnce(body Body, arity func(Ref) int) func() VarSet {
	var vars VarSet
	return func() VarSet {
		if vars == nil {
			vis := varVisitorPool.Get().WithParams(SafetyCheckVisitorParamsWithArity(arity))
			vis.WalkBody(body)
			vars = vis.Vars().Copy()
			varVisitorPool.Put(vis)
		}
		return vars
	}
}

// generates reports whether expr can yield more than one solution when safe is
// already bound. References with unbound positions iterate over a document, and
// relations yield one solution per tuple; everything else is defined at most
// once.
func (h *invariantHoister) generates(expr *Expr, safe VarSet) bool {
	if expr.Negated {
		return false
	}

	if operator := expr.Operator(); operator != nil {
		if bi, ok := h.builtins[operator.String()]; ok && bi.Relation {
			return true
		}
	}

	return outputVarsForTerms(expr, safe, nil).DiffCount(safe) > 0
}

// isInvariant reports whether expr can be evaluated with only safe bound, and
// so belongs ahead of the expressions in the comprehension body that iterate.
func (h *invariantHoister) isInvariant(expr *Expr, safe VarSet, bodyVars func() VarSet) bool {
	if _, ok := expr.Terms.(*SomeDecl); ok {
		// Declarations bind nothing at eval time, so moving them buys nothing
		// and only makes the rewritten body harder to read.
		return false
	}

	vis := varVisitorPool.Get()
	defer varVisitorPool.Put(vis)

	outputs := outputVarsForExpr(expr, h.arity, safe, VarSet{}, vis)

	// Every variable the expression reads has to be bound already, otherwise
	// the expression is not invariant (and would be unsafe this early in the
	// comprehension body).
	vis = vis.Clear().WithParams(SafetyCheckVisitorParamsWithArity(h.arity))
	vis.Walk(expr)
	if vis.Vars().Diff(outputs).DiffCount(safe) > 0 {
		return false
	}

	// SafetyCheckVisitorParams skips closures, so comprehensions nested in the
	// expression are checked separately for the variables they close over.
	vis = vis.Clear().WithParams(VarVisitorParams{})
	unsafeVarsInClosures(expr, vis)
	if len(vis.Vars()) == 0 {
		return true
	}

	return vis.Vars().Intersect(bodyVars()).Diff(outputs).DiffCount(safe) == 0
}

// impure reports whether expr calls a built-in function whose result depends on
// something other than its arguments, or whose evaluation is observable.
func (h *invariantHoister) impure(expr *Expr) bool {
	impure := false

	NewGenericVisitor(func(x any) bool {
		if impure {
			return true
		}
		switch x := x.(type) {
		case *Expr:
			if operator := x.Operator(); operator != nil {
				impure = h.impureOperator(operator)
			}
		case Call:
			if operator, ok := x[0].Value.(Ref); ok {
				impure = h.impureOperator(operator)
			}
		}
		return impure
	}).Walk(expr)

	return impure
}

func (h *invariantHoister) impureOperator(operator Ref) bool {
	if _, ok := sideEffectBuiltins[operator.String()]; ok {
		return true
	}

	bi, ok := h.builtins[operator.String()]
	if !ok {
		// Rules and functions defined in policy. Their results are memoized by
		// the evaluator, so calling one fewer time is not observable.
		return false
	}

	return bi.Nondeterministic
}
