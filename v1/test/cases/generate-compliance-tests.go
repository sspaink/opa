package generate_compliance_tests

import "embed"

//go:embed testdata/v1/*
var testcases embed.FS

