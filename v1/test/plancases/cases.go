// Copyright 2026 The OPA Authors.  All rights reserved.
// Use of this source code is governed by an Apache2
// license that can be found in the LICENSE file.

// Package plancases contains test cases for the plan a policy compiles to.
//
// A case is a directory holding case.yaml -- the modules to compile and the
// entrypoints to plan them for -- and plan.json, the plan the case expects.
// Expected plans are generated rather than authored: regenerate one with
// go test ./internal/planner -update-expected-plans, and read the diff, as
// nothing else checks that a change to it was intended. Both sides of a
// comparison are passed through Canonicalize first, whose documentation lists
// what a case therefore does not assert.
package plancases

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/open-policy-agent/opa/v1/util"
)

// Case is a single plan test case.
type Case struct {
	Dir          string   `json:"-"            yaml:"-"`           // directory the case was loaded from
	ExpectedPath string   `json:"-"            yaml:"-"`           // path of the expected plan
	Note         string   `json:"note"         yaml:"note"`        // globally unique identifier for this case
	Modules      []string `json:"modules"      yaml:"modules"`     // policies to compile, named test-0.rego, test-1.rego, ...
	Entrypoints  []string `json:"entrypoints"  yaml:"entrypoints"` // entrypoints to plan, e.g. test/p
}

// ModuleName returns the name given to the i-th module of a case.
func ModuleName(i int) string {
	return fmt.Sprintf("test-%d.rego", i)
}

// ExpectedName is the name of the file holding a case's expected plan.
const ExpectedName = "plan.json"

// Expected returns the contents of the case's expected plan.
func (c Case) Expected() ([]byte, error) {
	return os.ReadFile(c.ExpectedPath)
}

// Load returns the cases under path, one per directory containing a case.yaml.
func Load(path string) ([]Case, error) {
	var result []Case

	err := filepath.Walk(path, func(path string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() || info.Name() != "case.yaml" {
			return err
		}

		bs, err := os.ReadFile(path)
		if err != nil {
			return fmt.Errorf("%s: %w", path, err)
		}

		var c Case
		if err := util.Unmarshal(bs, &c); err != nil {
			return fmt.Errorf("%s: %w", path, err)
		}

		c.Dir = filepath.Dir(path)
		c.ExpectedPath = filepath.Join(c.Dir, ExpectedName)
		result = append(result, c)
		return nil
	})

	return result, err
}

// MustLoad returns the cases under path or panics if an error occurs.
func MustLoad(path string) []Case {
	result, err := Load(path)
	if err != nil {
		panic(err)
	}
	return result
}
