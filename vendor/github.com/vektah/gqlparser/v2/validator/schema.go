package validator

import (
	"sort"
	"strconv"
	"strings"

	. "github.com/vektah/gqlparser/v2/ast" //nolint:staticcheck // bad, yeah
	"github.com/vektah/gqlparser/v2/gqlerror"
	"github.com/vektah/gqlparser/v2/parser"
)

func LoadSchema(inputs ...*Source) (*Schema, error) {
	sd, err := parser.ParseSchemas(inputs...)
	if err != nil {
		return nil, gqlerror.WrapIfUnwrapped(err)
	}
	return ValidateSchemaDocument(sd)
}

func ValidateSchemaDocument(sd *SchemaDocument) (*Schema, error) {
	schema := Schema{
		Types:         map[string]*Definition{},
		Directives:    map[string]*DirectiveDefinition{},
		PossibleTypes: map[string][]*Definition{},
		Implements:    map[string][]*Definition{},
	}

	for i, def := range sd.Definitions {
		if schema.Types[def.Name] != nil {
			return nil, gqlerror.ErrorPosf(def.Position, "Cannot redeclare type %s.", def.Name)
		}
		schema.Types[def.Name] = sd.Definitions[i]
	}

	defs := append(DefinitionList{}, sd.Definitions...)

	for _, ext := range sd.Extensions {
		def := schema.Types[ext.Name]
		if def == nil {
			schema.Types[ext.Name] = &Definition{
				Kind:     ext.Kind,
				Name:     ext.Name,
				Position: ext.Position,
			}
			def = schema.Types[ext.Name]
			defs = append(defs, def)
		}

		if def.Kind != ext.Kind {
			return nil, gqlerror.ErrorPosf(ext.Position, "Cannot extend type %s because the base type is a %s, not %s.", ext.Name, def.Kind, ext.Kind)
		}

		def.Directives = append(def.Directives, ext.Directives...)
		def.Interfaces = append(def.Interfaces, ext.Interfaces...)
		def.Fields = append(def.Fields, ext.Fields...)
		def.Types = append(def.Types, ext.Types...)
		def.EnumValues = append(def.EnumValues, ext.EnumValues...)
	}

	for _, def := range defs {
		switch def.Kind {
		case Union:
			for _, t := range def.Types {
				schema.AddPossibleType(def.Name, schema.Types[t])
				schema.AddImplements(t, def)
			}
		case InputObject, Object:
			for _, intf := range def.Interfaces {
				schema.AddPossibleType(intf, def)
				schema.AddImplements(def.Name, schema.Types[intf])
			}
			schema.AddPossibleType(def.Name, def)
		case Interface:
			for _, intf := range def.Interfaces {
				schema.AddPossibleType(intf, def)
				schema.AddImplements(def.Name, schema.Types[intf])
			}
		}
	}

	for i, dir := range sd.Directives {
		if schema.Directives[dir.Name] != nil {
			// While the spec says SDL must not (§3.5) explicitly define builtin
			// scalars, it may (§3.13) define builtin directives. Here we check for
			// that, and reject doubly-defined directives otherwise.
			switch dir.Name {
			case "include", "skip", "deprecated", "specifiedBy", "defer", "oneOf": // the builtins
				// In principle here we might want to validate that the
				// directives are the same. But they might not be, if the
				// server has an older spec than we do. (Plus, validating this
				// is a lot of work.) So we just keep the first one we saw.
				// That's an arbitrary choice, but in theory the only way it
				// fails is if the server is using features newer than this
				// version of gqlparser, in which case they're in trouble
				// anyway.
			default:
				return nil, gqlerror.ErrorPosf(dir.Position, "Cannot redeclare directive %s.", dir.Name)
			}
		}
		schema.Directives[dir.Name] = sd.Directives[i]
	}

	if len(sd.Schema) > 1 {
		return nil, gqlerror.ErrorPosf(sd.Schema[1].Position, "Cannot have multiple schema entry points, consider schema extensions instead.")
	}

	if len(sd.Schema) == 1 {
		schema.Description = sd.Schema[0].Description
		for _, entrypoint := range sd.Schema[0].OperationTypes {
			def := schema.Types[entrypoint.Type]
			if def == nil {
				return nil, gqlerror.ErrorPosf(entrypoint.Position, "Schema root %s refers to a type %s that does not exist.", entrypoint.Operation, entrypoint.Type)
			}
			switch entrypoint.Operation {
			case Query:
				schema.Query = def
			case Mutation:
				schema.Mutation = def
			case Subscription:
				schema.Subscription = def
			}
		}
		if err := validateDirectives(&schema, sd.Schema[0].Directives, LocationSchema, nil); err != nil {
			return nil, err
		}
		schema.SchemaDirectives = append(schema.SchemaDirectives, sd.Schema[0].Directives...)
	}

	for _, ext := range sd.SchemaExtension {
		for _, entrypoint := range ext.OperationTypes {
			def := schema.Types[entrypoint.Type]
			if def == nil {
				return nil, gqlerror.ErrorPosf(entrypoint.Position, "Schema root %s refers to a type %s that does not exist.", entrypoint.Operation, entrypoint.Type)
			}
			switch entrypoint.Operation {
			case Query:
				schema.Query = def
			case Mutation:
				schema.Mutation = def
			case Subscription:
				schema.Subscription = def
			}
		}
		if err := validateDirectives(&schema, ext.Directives, LocationSchema, nil); err != nil {
			return nil, err
		}
		schema.SchemaDirectives = append(schema.SchemaDirectives, ext.Directives...)
	}

	if err := validateTypeDefinitions(&schema); err != nil {
		return nil, err
	}

	if err := validateDirectiveDefinitions(&schema); err != nil {
		return nil, err
	}

	// Inferred root operation type names should be performed only when a `schema` directive is
	// **not** provided, when it is, `Mutation` and `Subscription` becomes valid types and are not
	// assigned as a root operation on the schema.
	if len(sd.Schema) == 0 {
		if schema.Query == nil && schema.Types["Query"] != nil {
			schema.Query = schema.Types["Query"]
		}

		if schema.Mutation == nil && schema.Types["Mutation"] != nil {
			schema.Mutation = schema.Types["Mutation"]
		}

		if schema.Subscription == nil && schema.Types["Subscription"] != nil {
			schema.Subscription = schema.Types["Subscription"]
		}
	}

	if schema.Query != nil {
		schema.Query.Fields = append(
			schema.Query.Fields,
			&FieldDefinition{
				Name: "__schema",
				Type: NonNullNamedType("__Schema", nil),
			},
			&FieldDefinition{
				Name: "__type",
				Type: NamedType("__Type", nil),
				Arguments: ArgumentDefinitionList{
					{Name: "name", Type: NonNullNamedType("String", nil)},
				},
			},
		)
	}

	return &schema, nil
}

func validateTypeDefinitions(schema *Schema) *gqlerror.Error {
	types := make([]string, 0, len(schema.Types))
	for typ := range schema.Types {
		types = append(types, typ)
	}
	sort.Strings(types)
	for _, typ := range types {
		err := validateDefinition(schema, schema.Types[typ])
		if err != nil {
			return err
		}
	}
	return nil
}

func validateDirectiveDefinitions(schema *Schema) *gqlerror.Error {
	directives := make([]string, 0, len(schema.Directives))
	for directive := range schema.Directives {
		directives = append(directives, directive)
	}
	sort.Strings(directives)
	for _, directive := range directives {
		err := validateDirective(schema, schema.Directives[directive])
		if err != nil {
			return err
		}
	}
	return nil
}

func validateDirective(schema *Schema, def *DirectiveDefinition) *gqlerror.Error {
	if err := validateName(def.Position, def.Name); err != nil {
		// now, GraphQL spec doesn't have reserved directive name
		return err
	}

	return validateArgs(schema, def.Arguments, def)
}

func validateDefinition(schema *Schema, def *Definition) *gqlerror.Error {
	for _, field := range def.Fields {
		if err := validateName(field.Position, field.Name); err != nil {
			// now, GraphQL spec doesn't have reserved field name
			return err
		}
		if err := validateTypeRef(schema, field.Type); err != nil {
			return err
		}
		if err := validateArgs(schema, field.Arguments, nil); err != nil {
			return err
		}
		wantDirLocation := LocationFieldDefinition
		if def.Kind == InputObject {
			wantDirLocation = LocationInputFieldDefinition
		}
		if err := validateDirectives(schema, field.Directives, wantDirLocation, nil); err != nil {
			return err
		}
	}

	for _, typ := range def.Types {
		typDef := schema.Types[typ]
		if typDef == nil {
			return gqlerror.ErrorPosf(def.Position, "Undefined type %s.", strconv.Quote(typ))
		}
		if !isValidKind(typDef.Kind, Object) {
			return gqlerror.ErrorPosf(def.Position, "%s type %s must be %s.", def.Kind, strconv.Quote(typ), kindList(Object))
		}
	}

	for _, intf := range def.Interfaces {
		if err := validateImplements(schema, def, intf); err != nil {
			return err
		}
	}

	switch def.Kind {
	case Object, Interface:
		if len(def.Fields) == 0 {
			return gqlerror.ErrorPosf(def.Position, "%s %s: must define one or more fields.", def.Kind, def.Name)
		}
		for _, field := range def.Fields {
			if typ, ok := schema.Types[field.Type.Name()]; ok {
				if !isValidKind(typ.Kind, Scalar, Object, Interface, Union, Enum) {
					return gqlerror.ErrorPosf(field.Position, "%s %s: field must be one of %s.", def.Kind, def.Name, kindList(Scalar, Object, Interface, Union, Enum))
				}
			}
		}
	case Enum:
		if len(def.EnumValues) == 0 {
			return gqlerror.ErrorPosf(def.Position, "%s %s: must define one or more unique enum values.", def.Kind, def.Name)
		}
		for _, value := range def.EnumValues {
			for _, nonEnum := range [3]string{"true", "false", "null"} {
				if value.Name == nonEnum {
					return gqlerror.ErrorPosf(def.Position, "%s %s: non-enum value %s.", def.Kind, def.Name, value.Name)
				}
			}
			if err := validateDirectives(schema, value.Directives, LocationEnumValue, nil); err != nil {
				return err
			}
		}
	case InputObject:
		if len(def.Fields) == 0 {
			return gqlerror.ErrorPosf(def.Position, "%s %s: must define one or more input fields.", def.Kind, def.Name)
		}
		for _, field := range def.Fields {
			if typ, ok := schema.Types[field.Type.Name()]; ok {
				if !isValidKind(typ.Kind, Scalar, Enum, InputObject) {
					return gqlerror.ErrorPosf(field.Position, "%s %s: field must be one of %s.", typ.Kind, field.Name, kindList(Scalar, Enum, InputObject))
				}
			}
		}
	}

	for idx, field1 := range def.Fields {
		for _, field2 := range def.Fields[idx+1:] {
			if field1.Name == field2.Name {
				return gqlerror.ErrorPosf(field2.Position, "Field %s.%s can only be defined once.", def.Name, field2.Name)
			}
		}
	}

	if !def.BuiltIn {
		// GraphQL spec has reserved type names a lot!
		err := validateName(def.Position, def.Name)
		if err != nil {
			return err
		}
	}

	return validateDirectives(schema, def.Directives, DirectiveLocation(def.Kind), nil)
}

func validateTypeRef(schema *Schema, typ *Type) *gqlerror.Error {
	if schema.Types[typ.Name()] == nil {
		return gqlerror.ErrorPosf(typ.Position, "Undefined type %s.", typ.Name())
	}
	return nil
}

func validateArgs(schema *Schema, args ArgumentDefinitionList, currentDirective *DirectiveDefinition) *gqlerror.Error {
	for _, arg := range args {
		if err := validateName(arg.Position, arg.Name); err != nil {
			// now, GraphQL spec doesn't have reserved argument name
			return err
		}
		if err := validateTypeRef(schema, arg.Type); err != nil {
			return err
		}
		def := schema.Types[arg.Type.Name()]
		if !def.IsInputType() {
			return gqlerror.ErrorPosf(
				arg.Position,
				"cannot use %s as argument %s because %s is not a valid input type",
				arg.Type.String(),
				arg.Name,
				def.Kind,
			)
		}
		if err := validateDirectives(schema, arg.Directives, LocationArgumentDefinition, currentDirective); err != nil {
			return err
		}
	}
	return nil
}

func validateDirectives(schema *Schema, dirs DirectiveList, location DirectiveLocation, currentDirective *DirectiveDefinition) *gqlerror.Error {
	for _, dir := range dirs {
		if err := validateName(dir.Position, dir.Name); err != nil {
			// now, GraphQL spec doesn't have reserved directive name
			return err
		}
		if currentDirective != nil && dir.Name == currentDirective.Name {
			return gqlerror.ErrorPosf(dir.Position, "Directive %s cannot refer to itself.", currentDirective.Name)
		}
		dirDefinition := schema.Directives[dir.Name]
		if dirDefinition == nil {
			return gqlerror.ErrorPosf(dir.Position, "Undefined directive %s.", dir.Name)
		}
		validKind := false
		for _, dirLocation := range dirDefinition.Locations {
			if dirLocation == location {
				validKind = true
				break
			}
		}
		if !validKind {
			return gqlerror.ErrorPosf(dir.Position, "Directive %s is not applicable on %s.", dir.Name, location)
		}
		for _, arg := range dir.Arguments {
			if dirDefinition.Arguments.ForName(arg.Name) == nil {
				return gqlerror.ErrorPosf(arg.Position, "Undefined argument %s for directive %s.", arg.Name, dir.Name)
			}
		}
		for _, schemaArg := range dirDefinition.Arguments {
			if schemaArg.Type.NonNull && schemaArg.DefaultValue == nil {
				if arg := dir.Arguments.ForName(schemaArg.Name); arg == nil || arg.Value.Kind == NullValue {
					return gqlerror.ErrorPosf(dir.Position, "Argument %s for directive %s cannot be null.", schemaArg.Name, dir.Name)
				}
			}
		}
		dir.Definition = schema.Directives[dir.Name]
	}
	return nil
}

func validateImplements(schema *Schema, def *Definition, intfName string) *gqlerror.Error {
	// see validation rules at the bottom of
	// https://spec.graphql.org/October2021/#sec-Objects
	intf := schema.Types[intfName]
	if intf == nil {
		return gqlerror.ErrorPosf(def.Position, "Undefined type %s.", strconv.Quote(intfName))
	}
	if intf.Kind != Interface {
		return gqlerror.ErrorPosf(def.Position, "%s is a non interface type %s.", strconv.Quote(intfName), intf.Kind)
	}
	for _, requiredField := range intf.Fields {
		foundField := def.Fields.ForName(requiredField.Name)
		if foundField == nil {
			return gqlerror.ErrorPosf(def.Position,
				`For %s to implement %s it must have a field called %s.`,
				def.Name, intf.Name, requiredField.Name,
			)
		}

		if !isCovariant(schema, requiredField.Type, foundField.Type) {
			return gqlerror.ErrorPosf(foundField.Position,
				`For %s to implement %s the field %s must have type %s.`,
				def.Name, intf.Name, requiredField.Name, requiredField.Type.String(),
			)
		}

		for _, requiredArg := range requiredField.Arguments {
			foundArg := foundField.Arguments.ForName(requiredArg.Name)
			if foundArg == nil {
				return gqlerror.ErrorPosf(foundField.Position,
					`For %s to implement %s the field %s must have the same arguments but it is missing %s.`,
					def.Name, intf.Name, requiredField.Name, requiredArg.Name,
				)
			}

			if !requiredArg.Type.IsCompatible(foundArg.Type) {
				return gqlerror.ErrorPosf(foundArg.Position,
					`For %s to implement %s the field %s must have the same arguments but %s has the wrong type.`,
					def.Name, intf.Name, requiredField.Name, requiredArg.Name,
				)
			}
		}
		for _, foundArgs := range foundField.Arguments {
			if requiredField.Arguments.ForName(foundArgs.Name) == nil && foundArgs.Type.NonNull && foundArgs.DefaultValue == nil {
				return gqlerror.ErrorPosf(foundArgs.Position,
					`For %s to implement %s any additional arguments on %s must be optional or have a default value but %s is required.`,
					def.Name, intf.Name, foundField.Name, foundArgs.Name,
				)
			}
		}
	}
	return validateTypeImplementsAncestors(schema, def, intfName)
}

// validateTypeImplementsAncestors
// https://github.com/graphql/graphql-js/blob/47bd8c8897c72d3efc17ecb1599a95cee6bac5e8/src/type/validate.ts#L428
func validateTypeImplementsAncestors(schema *Schema, def *Definition, intfName string) *gqlerror.Error {
	intf := schema.Types[intfName]
	if intf == nil {
		return gqlerror.ErrorPosf(def.Position, "Undefined type %s.", strconv.Quote(intfName))
	}
	for _, transitive := range intf.Interfaces {
		if !containsString(def.Interfaces, transitive) {
			if transitive == def.Name {
				return gqlerror.ErrorPosf(def.Position,
					`Type %s cannot implement %s because it would create a circular reference.`,
					def.Name, intfName,
				)
			}
			return gqlerror.ErrorPosf(def.Position,
				`Type %s must implement %s because it is implemented by %s.`,
				def.Name, transitive, intfName,
			)
		}
	}
	return nil
}

func containsString(slice []string, want string) bool {
	for _, str := range slice {
		if want == str {
			return true
		}
	}
	return false
}

func isCovariant(schema *Schema, required *Type, actual *Type) bool {
	if required.NonNull && !actual.NonNull {
		return false
	}

	if required.NamedType != "" {
		if required.NamedType == actual.NamedType {
			return true
		}
		for _, pt := range schema.PossibleTypes[required.NamedType] {
			if pt.Name == actual.NamedType {
				return true
			}
		}
		return false
	}

	if required.Elem != nil && actual.Elem == nil {
		return false
	}

	return isCovariant(schema, required.Elem, actual.Elem)
}

func validateName(pos *Position, name string) *gqlerror.Error {
	if strings.HasPrefix(name, "__") {
		return gqlerror.ErrorPosf(pos, `Name "%s" must not begin with "__", which is reserved by GraphQL introspection.`, name)
	}
	return nil
}

func isValidKind(kind DefinitionKind, valid ...DefinitionKind) bool {
	for _, k := range valid {
		if kind == k {
			return true
		}
	}
	return false
}

func kindList(kinds ...DefinitionKind) string {
	s := make([]string, len(kinds))
	for i, k := range kinds {
		s[i] = string(k)
	}
	return strings.Join(s, ", ")
}
