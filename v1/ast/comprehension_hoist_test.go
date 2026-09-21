package ast

import (
	"testing"

	"github.com/open-policy-agent/opa/v1/metrics"
)

func TestHoistComprehensionInvariants(t *testing.T) {
	t.Parallel()

	tests := []struct {
		note   string
		module string
		exp    string
	}{
		{
			note: "call that only reads the enclosing body is hoisted",
			module: `package test

				f(x) := x

				p[x] := y if {
					data.foo[x]
					y = {v | data.bar[u] = v; f(x) = u}
				}`,
			exp: `package test

				f(__local0__) := __local0__ if { true }

				p[x] := y if {
					data.foo[x]
					y = {v | data.test.f(x, __local1__); __local1__ = u; data.bar[u] = v}
				}`,
		},
		{
			note: "call that reads a variable the comprehension binds stays put",
			module: `package test

				f(x) := x

				p := y if {
					y = {v | data.bar[u] = v; f(u) = 1}
				}`,
			exp: `package test

				f(__local0__) := __local0__ if { true }

				p := y if {
					y = {v | __local1__ = 1; data.bar[u] = v; data.test.f(u, __local1__)}
				}`,
		},
		{
			note: "body without a generator is left alone",
			module: `package test

				p := y if {
					x := input.x
					y := {v | v := input.v; upper(x) == "A"}
				}`,
			exp: `package test

				p := __local2__ if {
					__local0__ = input.x
					__local2__ = {__local1__ |
						__local1__ = input.v
						upper(__local0__, __local3__)
						__local3__ = "A"}
				}`,
		},
		{
			note: "generator stays put so array comprehension order is preserved",
			module: `package test

				p := y if {
					y := [[a, b] | xs := data.groups.g; a := xs[_]; b := data.ys[_]]
				}`,
			exp: `package test

				p := __local3__ if {
					__local3__ = [[__local1__, __local2__] |
						__local0__ = data.groups.g
						__local1__ = __local0__[_]
						__local2__ = data.ys[_]]
				}`,
		},
		{
			note: "negation is hoisted",
			module: `package test

				p := y if {
					x := input.x
					y := {v | v := data.bar[_]; not data.deny[x]}
				}`,
			exp: `package test

				p := __local2__ if {
					__local0__ = input.x
					__local2__ = {__local1__ | not data.deny[__local0__]; __local1__ = data.bar[_]}
				}`,
		},
		{
			note: "non-deterministic built-in stays put",
			module: `package test

				p := y if {
					x := input.x
					y := {v | v := data.bar[_]; time.now_ns() > x}
				}`,
			exp: `package test

				p := __local2__ if {
					__local0__ = input.x
					__local2__ = {__local1__ |
						__local1__ = data.bar[_]
						time.now_ns(__local3__)
						gt(__local3__, __local0__)}
				}`,
		},
		{
			note: "nothing moves ahead of a non-deterministic built-in",
			module: `package test

				p := y if {
					x := input.x
					y := {v | time.now_ns() > 0; v := data.bar[_]; upper(x) == v}
				}`,
			exp: `package test

				p := __local2__ if {
					__local0__ = input.x
					__local2__ = {__local1__ |
						time.now_ns(__local3__)
						gt(__local3__, 0)
						__local1__ = data.bar[_]
						upper(__local0__, __local4__)
						__local4__ = __local1__}
				}`,
		},
		{
			note: "trace stays put",
			module: `package test

				p := y if {
					x := input.x
					y := {v | v := data.bar[_]; trace(x)}
				}`,
			exp: `package test

				p := __local2__ if {
					__local0__ = input.x
					__local2__ = {__local1__ | __local1__ = data.bar[_]; trace(__local0__)}
				}`,
		},
		{
			note: "walk stays put",
			module: `package test

				p := y if {
					x := input.x
					y := {v | v := data.bar[_]; walk(x, [_, "a"])}
				}`,
			exp: `package test

				p := __local2__ if {
					__local0__ = input.x
					__local2__ = {__local1__ | __local1__ = data.bar[_]; walk(__local0__, [_, "a"])}
				}`,
		},
		{
			note: "indexed comprehension is left alone",
			module: `package test

				p contains x if {
					y = input[x]
					ys = [y | y = input[z]; z = x]
				}`,
			exp: `package test

				p contains x if {
					y = input[x]
					ys = [y | y = input[z]; z = x]
				}`,
		},
		{
			note: "nested comprehension closing over a comprehension variable stays put",
			module: `package test

				p := y if {
					y := {v | v := data.bar[u]; t := {1 | data.baz[u]}; t == {1}}
				}`,
			exp: `package test

				p := __local2__ if {
					__local2__ = {__local0__ |
						__local1__ = {1}
						__local0__ = data.bar[u]
						__local1__ = {1 | data.baz[u]}}
				}`,
		},
		{
			note: "hoisting happens inside every bodies",
			module: `package test

				p if {
					x := input.x
					every k in data.bar {
						y := {v | v := data.baz[_]; upper(x) == v}
						k == y
					}
				}`,
			exp: `package test

				p if {
					__local0__ = input.x
					__local5__ = data.bar
					every __local1__, __local2__ in __local5__ {
						__local4__ = {__local3__ |
							upper(__local0__, __local6__)
							__local6__ = __local3__
							__local3__ = data.baz[_]}
						__local2__ = __local4__
					}
				}`,
		},
		{
			note: "object comprehension",
			module: `package test

				f(x) := x

				p := y if {
					x := input.x
					y := {k: v | v := data.bar[k]; f(x) == "a"}
				}`,
			exp: `package test

				f(__local0__) := __local0__ if { true }

				p := __local3__ if {
					__local1__ = input.x
					__local3__ = {k: __local2__ |
						data.test.f(__local1__, __local4__)
						__local4__ = "a"
						__local2__ = data.bar[k]}
				}`,
		},
	}

	for _, tc := range tests {
		t.Run(tc.note, func(t *testing.T) {
			t.Parallel()

			c := MustCompileModules(map[string]string{"test.rego": tc.module})
			if exp, act := MustParseModule(tc.exp), c.Modules["test.rego"]; !exp.Equal(act) {
				t.Fatalf("Expected:\n\n%v\n\nGot:\n\n%v", exp, act)
			}
		})
	}
}

func TestHoistComprehensionInvariantsMetric(t *testing.T) {
	t.Parallel()

	m := metrics.New()
	c := NewCompiler().WithMetrics(m)
	c.Compile(map[string]*Module{"test.rego": MustParseModule(`package test

f(x) := x

p[x] := y if {
	data.foo[x]
	y = {v | data.bar[u] = v; f(x) = u}
}`)})

	if c.Failed() {
		t.Fatal(c.Errors)
	}

	if exp, act := uint64(2), m.Counter(compileStageComprehensionHoist).Value().(uint64); exp != act {
		t.Fatalf("expected %d hoisted expressions, got %d", exp, act)
	}
}

func TestHoistComprehensionInvariantsInQuery(t *testing.T) {
	t.Parallel()

	c := MustCompileModules(map[string]string{"test.rego": `package test

f(x) := x`})

	body, err := c.QueryCompiler().Compile(MustParseBody(`x = input.x; y = {v | data.bar[u] = v; data.test.f(x) = u}`))
	if err != nil {
		t.Fatal(err)
	}

	exp := MustParseBody(`x = input.x; y = {v | data.test.f(x, __localq0__); __localq0__ = u; data.bar[u] = v}`)
	if !exp.Equal(body) {
		t.Fatalf("Expected:\n\n%v\n\nGot:\n\n%v", exp, body)
	}
}
