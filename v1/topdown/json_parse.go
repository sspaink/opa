// Copyright 2026 The OPA Authors.  All rights reserved.
// Use of this source code is governed by an Apache2
// license that can be found in the LICENSE file.

package topdown

import (
	"errors"
	"unicode/utf8"

	"github.com/open-policy-agent/opa/v1/ast"
	"github.com/open-policy-agent/opa/v1/util"
)

// maxFastJSONDepth bounds the recursion of the fast parser. Deeper documents
// are left to the standard library, which has its own limit and error.
const maxFastJSONDepth = 512

var errFastJSON = errors.New("not handled by fast JSON parser")

// jsonToValue converts JSON to an AST value, avoiding the intermediate Go
// representation where it can. Input the fast path doesn't handle goes through
// the standard decoder, which also produces the errors.
func jsonToValue(bs []byte) (ast.Value, error) {
	if v, ok := fastJSONToValue(bs); ok {
		return v, nil
	}
	var x any
	if err := util.UnmarshalJSON(bs, &x); err != nil {
		return nil, err
	}
	return ast.InterfaceToValue(x)
}

// fastJSONToValue parses the JSON document in bs straight to a Value. It
// reports ok=false for anything it does not handle: invalid documents, strings
// that need unescaping or are not valid UTF-8, and very deep nesting. Callers
// fall back to the standard library in that case, which keeps its errors.
//
// For documents it does handle, the result equals ast.InterfaceToValue applied
// to the document decoded with json.Decoder.UseNumber.
func fastJSONToValue(bs []byte) (ast.Value, bool) {
	p := jsonParser{data: bs}
	p.skipSpace()
	t, err := p.value(0)
	if err != nil {
		return nil, false
	}
	p.skipSpace()
	if p.pos != len(p.data) {
		return nil, false
	}
	return t.Value, true
}

type jsonParser struct {
	data []byte
	pos  int
}

func (p *jsonParser) skipSpace() {
	for p.pos < len(p.data) {
		switch p.data[p.pos] {
		case ' ', '\t', '\r', '\n':
			p.pos++
		default:
			return
		}
	}
}

func (p *jsonParser) value(depth int) (*ast.Term, error) {
	if p.pos >= len(p.data) {
		return nil, errFastJSON
	}
	switch c := p.data[p.pos]; {
	case c == '{':
		return p.object(depth + 1)
	case c == '[':
		return p.array(depth + 1)
	case c == '"':
		s, err := p.str()
		if err != nil {
			return nil, err
		}
		return ast.InternedTerm(s), nil
	case c == '-' || (c >= '0' && c <= '9'):
		return p.number()
	case c == 't':
		return p.literal("true", ast.InternedTerm(true))
	case c == 'f':
		return p.literal("false", ast.InternedTerm(false))
	case c == 'n':
		return p.literal("null", ast.InternedNullTerm)
	}
	return nil, errFastJSON
}

func (p *jsonParser) literal(lit string, t *ast.Term) (*ast.Term, error) {
	if len(p.data)-p.pos < len(lit) || string(p.data[p.pos:p.pos+len(lit)]) != lit {
		return nil, errFastJSON
	}
	p.pos += len(lit)
	return t, nil
}

func (p *jsonParser) object(depth int) (*ast.Term, error) {
	if depth > maxFastJSONDepth {
		return nil, errFastJSON
	}
	p.pos++ // '{'
	obj := ast.NewObjectWithCapacity(4)
	p.skipSpace()
	if p.pos < len(p.data) && p.data[p.pos] == '}' {
		p.pos++
		return ast.NewTerm(obj), nil
	}
	for {
		p.skipSpace()
		if p.pos >= len(p.data) || p.data[p.pos] != '"' {
			return nil, errFastJSON
		}
		k, err := p.str()
		if err != nil {
			return nil, err
		}
		p.skipSpace()
		if p.pos >= len(p.data) || p.data[p.pos] != ':' {
			return nil, errFastJSON
		}
		p.pos++
		p.skipSpace()
		v, err := p.value(depth)
		if err != nil {
			return nil, err
		}
		// Insert keeps the last of any duplicate keys, as encoding/json does.
		obj.Insert(ast.InternedTerm(k), v)
		p.skipSpace()
		if p.pos >= len(p.data) {
			return nil, errFastJSON
		}
		switch p.data[p.pos] {
		case ',':
			p.pos++
		case '}':
			p.pos++
			return ast.NewTerm(obj), nil
		default:
			return nil, errFastJSON
		}
	}
}

func (p *jsonParser) array(depth int) (*ast.Term, error) {
	if depth > maxFastJSONDepth {
		return nil, errFastJSON
	}
	p.pos++ // '['
	p.skipSpace()
	if p.pos < len(p.data) && p.data[p.pos] == ']' {
		p.pos++
		return ast.NewTerm(ast.NewArray()), nil
	}
	var elems []*ast.Term
	for {
		p.skipSpace()
		v, err := p.value(depth)
		if err != nil {
			return nil, err
		}
		elems = append(elems, v)
		p.skipSpace()
		if p.pos >= len(p.data) {
			return nil, errFastJSON
		}
		switch p.data[p.pos] {
		case ',':
			p.pos++
		case ']':
			p.pos++
			return ast.NewTerm(ast.NewArray(elems...)), nil
		default:
			return nil, errFastJSON
		}
	}
}

// str reads a string literal that needs no unescaping and is valid UTF-8.
func (p *jsonParser) str() (string, error) {
	start := p.pos + 1
	for i := start; i < len(p.data); i++ {
		c := p.data[i]
		if c == '"' {
			s := p.data[start:i]
			if !utf8.Valid(s) {
				return "", errFastJSON
			}
			p.pos = i + 1
			return string(s), nil
		}
		if c == '\\' || c < 0x20 {
			return "", errFastJSON
		}
	}
	return "", errFastJSON
}

func (p *jsonParser) number() (*ast.Term, error) {
	start := p.pos
	i := start
	if p.data[i] == '-' {
		i++
	}
	if i >= len(p.data) {
		return nil, errFastJSON
	}
	switch c := p.data[i]; {
	case c == '0':
		i++
	case c >= '1' && c <= '9':
		for i < len(p.data) && isDigit(p.data[i]) {
			i++
		}
	default:
		return nil, errFastJSON
	}
	if i < len(p.data) && p.data[i] == '.' {
		i++
		d := i
		for i < len(p.data) && isDigit(p.data[i]) {
			i++
		}
		if i == d {
			return nil, errFastJSON
		}
	}
	if i < len(p.data) && (p.data[i] == 'e' || p.data[i] == 'E') {
		i++
		if i < len(p.data) && (p.data[i] == '+' || p.data[i] == '-') {
			i++
		}
		d := i
		for i < len(p.data) && isDigit(p.data[i]) {
			i++
		}
		if i == d {
			return nil, errFastJSON
		}
	}
	p.pos = i
	n := string(p.data[start:i])
	if t := ast.InternedIntNumberTermFromString(n); t != nil {
		return t, nil
	}
	num, err := ast.NumberChecked(n)
	if err != nil {
		return nil, err
	}
	return ast.NewTerm(num), nil
}

func isDigit(c byte) bool { return c >= '0' && c <= '9' }
