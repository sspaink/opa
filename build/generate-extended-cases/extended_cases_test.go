package cases

import (
	"testing"

	"github.com/open-policy-agent/opa/v1/ast"
)

func TestLoadExtended(t *testing.T) {
	// If a test case fails to create an IR plan an error will be returned
	// Seems unnecessary to check each individual test if the plan was generated correctly
	_, err := LoadIrExtendedTestCases()
	if err != nil {
		t.Fatal(err)
	}
}

func TestLoadIrExtendedFiltered(t *testing.T) {
	c, err := ast.LoadCapabilitiesFile("testdata/test-capabilities.json")
	if err != nil {
		t.Fatal(err)
	}

	_, err = LoadIrExtendedTestCasesFiltered(CapabilitiesFilter(c))
	if err != nil {
		t.Fatal(err)
	}
}
