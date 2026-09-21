// Copyright 2024 The OPA Authors.  All rights reserved.
// Use of this source code is governed by an Apache2
// license that can be found in the LICENSE file.

package topdown

import (
	"fmt"
	"testing"

	"github.com/gobwas/glob"

	"github.com/open-policy-agent/opa/v1/ast"
	"github.com/open-policy-agent/opa/v1/metrics"
	"github.com/open-policy-agent/opa/v1/topdown/cache"
)

func TestGlobBuiltinCache(t *testing.T) {
	t.Parallel()

	ctx := BuiltinContext{}
	iter := func(*ast.Term) error { return nil }

	// A novel glob pattern is cached.
	glob1 := "foo/*"
	operands := []*ast.Term{
		ast.NewTerm(ast.String(glob1)),
		ast.NullTerm(),
		ast.NewTerm(ast.String("foo/bar")),
	}
	err := builtinGlobMatch(ctx, operands, iter)
	if err != nil {
		t.Fatalf("Unexpected error: %v", err)
	}

	// the glob id will have a trailing '-' rune.
	if _, ok := globCache[glob1+"-"]; !ok {
		t.Fatalf("Expected glob to be cached: %v", glob1)
	}

	// Fill up the cache.
	for i := range regexCacheMaxSize - 1 {
		operands := []*ast.Term{
			ast.NewTerm(ast.String(fmt.Sprintf("foo/%d/*", i))),
			ast.NullTerm(),
			ast.NewTerm(ast.String(fmt.Sprintf("foo/%d/bar", i))),
		}
		err := builtinGlobMatch(ctx, operands, iter)
		if err != nil {
			t.Fatalf("Unexpected error: %v", err)
		}
	}

	if len(globCache) != regexCacheMaxSize {
		t.Fatal("Expected cache to be full")
	}

	// A new glob pattern is cached and a random pattern is evicted.
	glob2 := "bar/*"
	operands = []*ast.Term{
		ast.NewTerm(ast.String(glob2)),
		ast.NullTerm(),
		ast.NewTerm(ast.String("bar/baz")),
	}
	err = builtinGlobMatch(ctx, operands, iter)
	if err != nil {
		t.Fatalf("Unexpected error: %v", err)
	}

	if len(globCache) != regexCacheMaxSize {
		t.Fatalf("Expected cache be capped at %d, was %d", regexCacheMaxSize, len(globCache))
	}

	if _, ok := globCache[glob2+"-"]; !ok {
		t.Fatalf("Expected glob to be cached: %v", glob2)
	}
}

// TestGlobMatchCompileError pins the patterns glob.match rejects, and the
// message it rejects them with: both are visible to policy authors, and both
// have changed with a gobwas/glob upgrade before. Note that a malformed
// pattern is not cached, so this test does not disturb the cache assertions
// above.
func TestGlobMatchCompileError(t *testing.T) {
	t.Parallel()

	tests := []struct {
		note    string
		pattern string
		wantErr string
	}{
		{
			note:    "unclosed pattern-alternatives list",
			pattern: "{a,b",
			wantErr: "glob: syntax error at 4: unclosed `{`",
		},
		{
			note:    "trailing backslash",
			pattern: `a\`,
			wantErr: "glob: syntax error at 2: trailing backslash",
		},
		{
			note:    "lone backslash",
			pattern: `\`,
			wantErr: "glob: syntax error at 1: trailing backslash",
		},
		{
			note:    "empty character-list",
			pattern: "[]",
			wantErr: "glob: syntax error at 2: could not parse range",
		},
		{
			note:    "unclosed character-list",
			pattern: "[abc",
			wantErr: "glob: syntax error at 4: unexpected end of input",
		},
		{
			note:    "reversed character-range",
			pattern: "[c-a]",
			wantErr: "glob: syntax error at 5: range hi character is less than lo",
		},
		{
			note:    "invalid UTF-8",
			pattern: "a\xffb",
			wantErr: "glob: syntax error at 1: invalid UTF-8 sequence",
		},
	}

	for _, tc := range tests {
		t.Run(tc.note, func(t *testing.T) {
			t.Parallel()

			operands := []*ast.Term{
				ast.NewTerm(ast.String(tc.pattern)),
				ast.NullTerm(),
				ast.NewTerm(ast.String("abc")),
			}

			err := builtinGlobMatch(BuiltinContext{}, operands, func(*ast.Term) error { return nil })
			if err == nil {
				t.Fatalf("Expected error for pattern %q", tc.pattern)
			}

			if err.Error() != tc.wantErr {
				t.Errorf("Expected error %q but got %q", tc.wantErr, err.Error())
			}
		})
	}
}

func TestGlobBuiltinInterQueryValueCache(t *testing.T) {
	t.Parallel()

	ip := []byte(`{"inter_query_builtin_value_cache": {"max_num_entries": 10, "named": {"glob": {"max_num_entries": 10}}}}`)
	config, err := cache.ParseCachingConfig(ip)
	if err != nil {
		t.Fatalf("parse caching config: %v", err)
	}
	interQueryValueCache := cache.NewInterQueryValueCache(t.Context(), config)

	ctx := BuiltinContext{InterQueryBuiltinValueCache: interQueryValueCache}
	iter := func(*ast.Term) error { return nil }

	// A novel glob pattern is cached.
	glob1 := "foo/*"
	operands := []*ast.Term{
		ast.NewTerm(ast.String(glob1)),
		ast.NullTerm(),
		ast.NewTerm(ast.String("foo/bar")),
	}
	err = builtinGlobMatch(ctx, operands, iter)
	if err != nil {
		t.Fatalf("Unexpected error: %v", err)
	}

	// the glob id will have a trailing '-' rune.
	if _, ok := ctx.InterQueryBuiltinValueCache.GetCache(globCacheName).Get(ast.StringTerm(glob1 + "-").Value); !ok {
		t.Fatalf("Expected glob to be cached: %v", glob1)
	}

	// Fill up the cache.
	for i := range 9 {
		operands := []*ast.Term{
			ast.NewTerm(ast.String(fmt.Sprintf("foo/%d/*", i))),
			ast.NullTerm(),
			ast.NewTerm(ast.String(fmt.Sprintf("foo/%d/bar", i))),
		}
		err := builtinGlobMatch(ctx, operands, iter)
		if err != nil {
			t.Fatalf("Unexpected error: %v", err)
		}
	}

	// A new glob pattern is cached and a random pattern is evicted.
	glob2 := "bar/*"
	operands = []*ast.Term{
		ast.NewTerm(ast.String(glob2)),
		ast.NullTerm(),
		ast.NewTerm(ast.String("bar/baz")),
	}
	err = builtinGlobMatch(ctx, operands, iter)
	if err != nil {
		t.Fatalf("Unexpected error: %v", err)
	}

	if _, ok := ctx.InterQueryBuiltinValueCache.GetCache(globCacheName).Get(ast.StringTerm(glob2 + "-").Value); !ok {
		t.Fatalf("Expected glob to be cached: %v", glob2)
	}
}

func TestGlobBuiltinInterQueryValueCacheTypeMismatch(t *testing.T) {
	t.Parallel()

	ip := []byte(`{"inter_query_builtin_value_cache": {"max_num_entries": 10, "named": {"glob": {"max_num_entries": 10}}}}`)
	config, err := cache.ParseCachingConfig(ip)
	if err != nil {
		t.Fatalf("parse caching config: %v", err)
	}
	interQueryValueCache := cache.NewInterQueryValueCache(t.Context(), config)

	ctx := BuiltinContext{InterQueryBuiltinValueCache: interQueryValueCache}
	iter := func(*ast.Term) error { return nil }

	key := "foo.*"

	operands := []*ast.Term{
		ast.NewTerm(ast.String(key)),
		ast.NullTerm(),
		ast.NewTerm(ast.String("foo/bar")),
	}
	err = builtinGlobMatch(ctx, operands, iter)
	if err != nil {
		t.Fatalf("Unexpected error: %v", err)
	}

	c := ctx.InterQueryBuiltinValueCache.GetCache(globCacheName)

	// the glob id will have a trailing '-' rune.
	if _, ok := c.Get(ast.StringTerm(key + "-").Value); !ok {
		t.Fatalf("Expected glob to be cached: %v", key)
	}

	// poison the cache entry
	c.Insert(ast.StringTerm(key+"-").Value, "bar")

	err = builtinGlobMatch(ctx, operands, iter)
	if err != nil {
		t.Fatalf("Unexpected error: %v", err)
	}

	// verify the entry was replaced rather than left poisoned
	value, ok := c.Get(ast.StringTerm(key + "-").Value)
	if !ok {
		t.Fatal("Expected key \"foo.*-\" in cache")
	}

	if _, ok := value.(*glob.Pattern); !ok {
		t.Fatalf("Expected *glob.Pattern but got %T", value)
	}
}

func TestGlobAndRegexInterQueryValueCachesAreSeparate(t *testing.T) {
	t.Parallel()

	config, err := cache.ParseCachingConfig(nil)
	if err != nil {
		t.Fatalf("parse caching config: %v", err)
	}

	ctx := BuiltinContext{
		InterQueryBuiltinValueCache: cache.NewInterQueryValueCache(t.Context(), config),
		Metrics:                     metrics.New(),
	}
	iter := func(*ast.Term) error { return nil }

	// A glob pattern with no delimiters gets the id "a-", which collides with a
	// regex whose pattern happens to be that same string.
	globOperands := []*ast.Term{
		ast.NewTerm(ast.String("a")),
		ast.NullTerm(),
		ast.NewTerm(ast.String("a")),
	}
	regexOperands := []*ast.Term{
		ast.NewTerm(ast.String("a-")),
		ast.NewTerm(ast.String("xa-b")),
	}

	for range 2 {
		if err := builtinGlobMatch(ctx, globOperands, iter); err != nil {
			t.Fatalf("Unexpected error: %v", err)
		}
		if err := builtinRegexMatch(ctx, regexOperands, iter); err != nil {
			t.Fatalf("Unexpected error: %v", err)
		}
	}

	// Each builtin must have served the second call from its own cache; when
	// they shared a keyspace the collision left one of them recompiling forever.
	if n := ctx.Metrics.Counter(globInterQueryValueCacheHits).Value(); n != uint64(1) {
		t.Errorf("Expected 1 glob cache hit, got %v", n)
	}
	if n := ctx.Metrics.Counter(regexInterQueryValueCacheHits).Value(); n != uint64(1) {
		t.Errorf("Expected 1 regex cache hit, got %v", n)
	}
}
