// Copyright 2026 The OPA Authors.  All rights reserved.
// Use of this source code is governed by an Apache2
// license that can be found in the LICENSE file.

package planner

import (
	"encoding/json"
	"flag"
	"os"
	"testing"

	"github.com/open-policy-agent/opa/v1/ast"
	"github.com/open-policy-agent/opa/v1/ir"
	"github.com/open-policy-agent/opa/v1/test/plancases"
	"github.com/open-policy-agent/opa/v1/util"
)

var updateExpected = flag.Bool("update-expected-plans", false, "rewrite the expected plans in v1/test/plancases from the current planner")

const casesDir = "../../v1/test/plancases/testdata"

// TestPlanCases plans each case in v1/test/plancases and compares the result to
// the plan the case expects. Both plans are canonicalized first, so a case
// asserts the structure of the plan and not the choices the format leaves open to
// a planner; see plancases.Canonicalize for which those are.
func TestPlanCases(t *testing.T) {
	for _, tc := range plancases.MustLoad(casesDir) {
		t.Run(tc.Note, func(t *testing.T) {
			got := planCase(t, tc)
			plancases.Canonicalize(got)

			bs, err := json.MarshalIndent(got, "", "  ")
			if err != nil {
				t.Fatal(err)
			}
			bs = append(bs, '\n')

			if *updateExpected {
				if err := os.WriteFile(tc.ExpectedPath, bs, 0o644); err != nil {
					t.Fatal(err)
				}
				t.Logf("wrote %s", tc.ExpectedPath)
				return
			}

			expectedBS, err := tc.Expected()
			if err != nil {
				t.Fatalf("%s (run go test ./internal/planner -update-expected-plans to create it)", err)
			}

			var expected ir.Policy
			if err := json.Unmarshal(expectedBS, &expected); err != nil {
				t.Fatalf("%s: %v", tc.ExpectedPath, err)
			}

			// The expected plan is canonicalized too, so that a plan produced
			// elsewhere, with its own numbering, can be compared against it as-is.
			plancases.Canonicalize(&expected)

			expBS, err := json.MarshalIndent(&expected, "", "  ")
			if err != nil {
				t.Fatal(err)
			}

			if string(bs) != string(expBS)+"\n" {
				t.Errorf("plan does not match %s\n\nexpected:\n\n%s\n\ngot:\n\n%s", tc.ExpectedPath, expBS, bs)
			}
		})
	}
}

// planCase compiles a case's modules and plans them for its entrypoints, the way
// v1/compile does when building for the plan target, and returns the plan as the
// planner produced it.
func planCase(t *testing.T, tc plancases.Case) *ir.Policy {
	t.Helper()

	modules := map[string]*ast.Module{}
	for i, module := range tc.Modules {
		name := plancases.ModuleName(i)
		parsed, err := ast.ParseModuleWithOpts(name, module, ast.ParserOptions{RegoVersion: ast.RegoV1})
		if err != nil {
			t.Fatalf("%s: %v", tc.Dir, err)
		}
		modules[name] = parsed
	}

	c := ast.NewCompiler()
	c.Compile(modules)
	if c.Failed() {
		t.Fatalf("%s: %v", tc.Dir, c.Errors)
	}

	resultSym := ast.VarTerm("result")
	queries := make([]QuerySet, len(tc.Entrypoints))

	for i, entrypoint := range tc.Entrypoints {
		ref, err := ast.PtrRef(ast.DefaultRootDocument, entrypoint)
		if err != nil {
			t.Fatalf("%s: entrypoint %q: %v", tc.Dir, entrypoint, err)
		}
		if len(c.GetRules(ref)) == 0 {
			t.Fatalf("%s: entrypoint %q does not refer to a rule", tc.Dir, entrypoint)
		}

		qc := c.QueryCompiler()
		compiled, err := qc.Compile(ast.NewBody(ast.Equality.Expr(resultSym, ast.NewTerm(ref))))
		if err != nil {
			t.Fatalf("%s: entrypoint %q: %v", tc.Dir, entrypoint, err)
		}

		queries[i] = QuerySet{
			Name:          entrypoint,
			Queries:       []ast.Body{compiled},
			RewrittenVars: qc.RewrittenVars(),
		}
	}

	// The compiler copies its input, so the rewritten modules -- the ones the
	// planner needs -- are the compiler's own, not the ones parsed above.
	sortedModules := make([]*ast.Module, 0, len(c.Modules))
	for _, name := range util.KeysSorted(c.Modules) {
		sortedModules = append(sortedModules, c.Modules[name])
	}

	policy, err := New().
		WithQueries(queries).
		WithModules(sortedModules).
		WithBuiltinDecls(ast.BuiltinMap).
		Plan()
	if err != nil {
		t.Fatalf("%s: %v", tc.Dir, err)
	}

	return policy
}
