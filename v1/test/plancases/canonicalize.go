// Copyright 2026 The OPA Authors.  All rights reserved.
// Use of this source code is governed by an Apache2
// license that can be found in the LICENSE file.

package plancases

import (
	"reflect"

	"github.com/open-policy-agent/opa/v1/ir"
)

// Canonicalize rewrites the choices the IR leaves open to a planner, so that two
// plans of the same policy compare equal whatever those choices were: plan-local
// variables are renumbered in order of first appearance (except 0 and 1, the input
// and data documents), the static string table is rebuilt in order of first use
// with its indices remapped, source positions and the file table are dropped, and
// so are the type declarations on required builtins.
//
// Statements, their order, the blocks they sit in, string values, required builtin
// names, and function names and arity are left alone: those are what a case
// asserts. Apply this to both sides of a comparison.
func Canonicalize(policy *ir.Policy) {
	if policy == nil {
		return
	}

	slots := planSlots(reflect.ValueOf(policy))

	locals := map[int]int{int(ir.Input): int(ir.Input), int(ir.Data): int(ir.Data)}
	strings := map[int]int{}
	var stringOrder []int

	// First pass: assign new numbers in order of first appearance.
	for _, s := range slots {
		switch s.kind {
		case slotLocal:
			if _, ok := locals[s.get()]; !ok {
				locals[s.get()] = len(locals) - 2 + int(ir.Unused)
			}
		case slotString:
			if _, ok := strings[s.get()]; !ok {
				strings[s.get()] = len(strings)
				stringOrder = append(stringOrder, s.get())
			}
		}
	}

	// Second pass: rewrite. Done after the first so that a new number colliding
	// with an old one cannot confuse the mapping.
	for _, s := range slots {
		switch s.kind {
		case slotLocal:
			s.set(locals[s.get()])
		case slotString:
			s.set(strings[s.get()])
		case slotLocation:
			s.v.Set(reflect.ValueOf(ir.Location{}))
		}
	}

	if policy.Static != nil {
		reordered := make([]*ir.StringConst, 0, len(stringOrder))
		for _, old := range stringOrder {
			if old < len(policy.Static.Strings) {
				reordered = append(reordered, policy.Static.Strings[old])
			}
		}
		policy.Static.Strings = reordered
		policy.Static.Files = nil // indexed by the positions just dropped

		for _, bi := range policy.Static.BuiltinFuncs {
			bi.Decl = nil
		}
	}
}

const (
	slotLocal = iota
	slotString
	slotLocation
)

// slot is a settable place in a plan holding a number Canonicalize may rewrite.
// For a value held in an interface field, v is the field itself, since a value
// read out of an interface cannot be set in place.
type slot struct {
	kind int
	v    reflect.Value
}

func (s slot) target() reflect.Value {
	if s.v.Kind() == reflect.Interface {
		return s.v.Elem()
	}
	return s.v
}

func (s slot) get() int { return int(s.target().Int()) }

func (s slot) set(n int) {
	s.v.Set(reflect.ValueOf(n).Convert(s.target().Type()))
}

// planSlots returns every rewritable slot reachable from v, in a deterministic
// order: struct fields in declaration order, slices in index order, which is
// stable because the IR holds no maps. Unexported fields are skipped, as they
// belong to the type declarations on builtins and hold no locals.
func planSlots(v reflect.Value) []slot {
	var out []slot
	collectPlanSlots(v, &out)
	return out
}

func collectPlanSlots(v reflect.Value, out *[]slot) {
	switch v.Kind() {
	case reflect.Ptr:
		if !v.IsNil() {
			collectPlanSlots(v.Elem(), out)
		}
	case reflect.Interface:
		if v.IsNil() {
			return
		}
		// A Local or StringIndex stored in an Operand is rewritten by assigning to
		// the interface field; anything else (a Stmt, say) is descended into.
		if v.CanSet() {
			switch v.Elem().Interface().(type) {
			case ir.Local:
				*out = append(*out, slot{slotLocal, v})
				return
			case ir.StringIndex:
				*out = append(*out, slot{slotString, v})
				return
			}
		}
		collectPlanSlots(v.Elem(), out)
	case reflect.Slice, reflect.Array:
		for i := range v.Len() {
			collectPlanSlots(v.Index(i), out)
		}
	case reflect.Struct:
		if v.Type() == reflect.TypeFor[ir.Location]() {
			if v.CanSet() {
				*out = append(*out, slot{slotLocation, v})
			}
			return
		}
		// MakeNumberRefStmt.Index is an index into the string table held as a
		// plain int rather than a StringIndex.
		numberRef := v.Type() == reflect.TypeFor[ir.MakeNumberRefStmt]()
		for i := range v.NumField() {
			field := v.Type().Field(i)
			if !field.IsExported() {
				continue
			}
			if numberRef && field.Name == "Index" && v.Field(i).CanSet() {
				*out = append(*out, slot{slotString, v.Field(i)})
				continue
			}
			collectPlanSlots(v.Field(i), out)
		}
	default:
		if !v.CanSet() {
			return
		}
		switch v.Interface().(type) {
		case ir.Local:
			*out = append(*out, slot{slotLocal, v})
		case ir.StringIndex:
			*out = append(*out, slot{slotString, v})
		}
	}
}
