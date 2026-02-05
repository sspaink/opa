package cases

import (
	"embed"
	"fmt"
	"io/fs"

	"github.com/open-policy-agent/opa/v1/util"
)

//go:embed testdata/v1/*
var testcases embed.FS

type ExtendedTestCase struct {
	TestCase
	EntryPoints    []string    `json:"entrypoints"`
	Plan           interface{} `json:"plan"`
	WantPlanResult interface{} `json:"want_plan_result"`
}

type ExtendedSet struct {
	Cases []*ExtendedTestCase `json:"cases"`
}

// output JSON files
// With generated plan and expected output included
func GenerateComplianceTests() error {
	err := fs.WalkDir(testcases, ".", func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}

		if d.IsDir() {
			return nil
		}

		f, err := testcases.ReadFile(path)
		if err != nil {
			return err
		}

		var x ExtendedSet
		if err := util.Unmarshal(f, &x); err != nil {
			return fmt.Errorf("%s: %w", path, err)
		}

		for _, tc := range x.Cases {

			tc.

			// Generate Entrypoint

			// Generate Plan

			// Generate WantPlanResults

		}

		return nil
	})

	return err
}

//func loadTests() ([]Test, error) {
//	var tests []Test
//	err := fs.WalkDir(testcases, ".", func(path string, d fs.DirEntry, err error) error {
//		if err != nil {
//			return err
//		}
//
//		if d.IsDir() {
//			return nil
//		}
//
//		f, err := testcases.ReadFile(path)
//		if err != nil {
//			return err
//		}
//
//		var x Test
//		if err := util.Unmarshal(f, &x); err != nil {
//			return fmt.Errorf("%s: %w", path, err)
//		}
//
//		for i := range x.Cases {
//			x.Cases[i].Filename = path
//			x.filename = path
//		}
//
//		tests = append(tests, x)
//		return nil
//	})
//
//	return tests, err
//}
