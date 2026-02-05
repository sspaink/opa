package main

import (
	"fmt"

	"github.com/open-policy-agent/opa/v1/test/cases"
)

func main() {
	err := cases.GenerateComplianceTests()
	fmt.Println(err)
}
