// Copyright 2026 The OPA Authors.  All rights reserved.
// Use of this source code is governed by an Apache2
// license that can be found in the LICENSE file.

package topdown

import (
	"bytes"
	"fmt"
	"strings"
	"testing"

	"github.com/open-policy-agent/opa/v1/ast"
	"github.com/open-policy-agent/opa/v1/util"
)

func stdJSONToValue(bs []byte) (ast.Value, error) {
	var x any
	if err := util.UnmarshalJSON(bs, &x); err != nil {
		return nil, err
	}
	return ast.InterfaceToValue(x)
}

var fastJSONCases = []string{
	`null`, `true`, `false`, `0`, `-0`, `1`, `-1`, `12345678901234567890`, `1.5`, `-1.5e10`, `1E+2`, `1e-2`,
	`1e999999`, `01`, `-`, `1.`, `.5`, `1e`, `+1`, `0x1`, `NaN`, `Infinity`,
	`""`, `"abc"`, `"a\"b"`, `"a\\b"`, `"é"`, `"😀"`, `"\ud800"`, "\"\xff\"", "\"\t\"",
	`[]`, `[ ]`, `[1]`, `[1,2,3]`, `[1,]`, `[,1]`, `[1 2]`, `[`, `]`, `[[[]]]`, `[null,true,"x",{"a":[1]}]`,
	`{}`, `{ }`, `{"a":1}`, `{"a":1,"a":2}`, `{"a":1,}`, `{,}`, `{"a"}`, `{"a":}`, `{a:1}`, `{"a":1 "b":2}`,
	`{"a":{"b":{"c":[1,{"d":null}]}}}`, `{"b":1,"a":2}`,
	``, ` `, "\n", ` 1 `, "\t\r\n{ \"a\" : [ 1 , 2 ] }\n", `1 2`, `{} {}`, `{}x`, "\ufeff{}", `tru`, `nul`, `truee`, `True`,
	`{"iss":"https://example.com","sub":"abc","aud":["x","y"],"exp":1893456000,"nbf":1700000000,"scope":"a b c"}`,
}

func TestFastJSONToValue(t *testing.T) {
	for _, c := range fastJSONCases {
		t.Run(c, func(t *testing.T) {
			want, wantErr := stdJSONToValue([]byte(c))
			got, ok := fastJSONToValue([]byte(c))
			if !ok {
				return // falls back to the standard path, which reproduces the value or error
			}
			if wantErr != nil {
				t.Fatalf("fast parser accepted input the standard path rejects: %v", wantErr)
			}
			if got.Compare(want) != 0 || got.String() != want.String() {
				t.Fatalf("got %v, want %v", got, want)
			}
		})
	}
}

func TestJSONToValueFallbackErrors(t *testing.T) {
	for _, c := range []string{``, `{`, `[1,]`, `{"a":1,}`, `1e999999`, `{} {}`, `"\ud800` + "\xff"} {
		_, wantErr := stdJSONToValue([]byte(c))
		_, gotErr := jsonToValue([]byte(c))
		if (wantErr == nil) != (gotErr == nil) || (wantErr != nil && wantErr.Error() != gotErr.Error()) {
			t.Errorf("%q: got error %v, want %v", c, gotErr, wantErr)
		}
	}
}

func TestFastJSONToValueHandlesCommonInput(t *testing.T) {
	for _, c := range []string{
		`null`, `true`, `1.5`, `"abc"`, `[1,2,3]`, `{"a":{"b":[1,{"c":null}]}}`, `{"a":1,"a":2}`, ` { } `,
	} {
		if _, ok := fastJSONToValue([]byte(c)); !ok {
			t.Errorf("expected fast path for %s", c)
		}
	}
}

func TestFastJSONToValueDepth(t *testing.T) {
	deep := strings.Repeat("[", 600) + strings.Repeat("]", 600)
	if _, ok := fastJSONToValue([]byte(deep)); ok {
		t.Error("expected fallback for deeply nested input")
	}
}

func FuzzFastJSONToValue(f *testing.F) {
	for _, c := range fastJSONCases {
		f.Add([]byte(c))
	}
	f.Fuzz(func(t *testing.T, bs []byte) {
		got, ok := fastJSONToValue(bs)
		if !ok {
			return
		}
		want, err := stdJSONToValue(bs)
		if err != nil {
			t.Fatalf("fast parser accepted %q, standard path rejects: %v", bs, err)
		}
		if got.Compare(want) != 0 || got.String() != want.String() {
			t.Fatalf("%q: got %v, want %v", bs, got, want)
		}
	})
}

func BenchmarkJSONToValue(b *testing.B) {
	payload := []byte(`{"iss":"https://example.com/","sub":"user-1234","aud":["api","web"],"exp":1893456000,"nbf":1700000000,"iat":1700000000,"scope":"read write","email_verified":true,"groups":["a","b","c"],"nested":{"x":1.5,"y":null}}`)
	var big bytes.Buffer
	big.WriteString(`{"items":[`)
	for i := range 500 {
		if i > 0 {
			big.WriteByte(',')
		}
		fmt.Fprintf(&big, `{"id":%d,"name":"user-%d","roles":["a","b"],"attrs":{"x":true}}`, i, i)
	}
	big.WriteString(`]}`)

	for name, body := range map[string][]byte{"jwt_payload": payload, "large": big.Bytes()} {
		b.Run(name+"/std", func(b *testing.B) {
			b.ReportAllocs()
			for b.Loop() {
				if _, err := stdJSONToValue(body); err != nil {
					b.Fatal(err)
				}
			}
		})
		b.Run(name+"/fast", func(b *testing.B) {
			b.ReportAllocs()
			for b.Loop() {
				if _, ok := fastJSONToValue(body); !ok {
					b.Fatal("fallback")
				}
			}
		})
	}
}
