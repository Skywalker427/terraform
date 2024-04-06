// Copyright (c) HashiCorp, Inc.
// SPDX-License-Identifier: BUSL-1.1

package funcs

import (
	"fmt"
	"regexp"
	"strings"

	"github.com/hashicorp/hcl/v2"
	"github.com/hashicorp/hcl/v2/ext/customdecode"
	"github.com/hashicorp/hcl/v2/hclsyntax"
	"github.com/zclconf/go-cty/cty"
	"github.com/zclconf/go-cty/cty/function"
)

// StartsWithFunc constructs a function that checks if a string starts with
// a specific prefix using strings.HasPrefix
var StartsWithFunc = function.New(&function.Spec{
	Params: []function.Parameter{
		{
			Name:         "str",
			Type:         cty.String,
			AllowUnknown: true,
		},
		{
			Name: "prefix",
			Type: cty.String,
		},
	},
	Type:         function.StaticReturnType(cty.Bool),
	RefineResult: refineNotNull,
	Impl: func(args []cty.Value, retType cty.Type) (cty.Value, error) {
		prefix := args[1].AsString()

		if !args[0].IsKnown() {
			// If the unknown value has a known prefix then we might be
			// able to still produce a known result.
			if prefix == "" {
				// The empty string is a prefix of any string.
				return cty.True, nil
			}
			if knownPrefix := args[0].Range().StringPrefix(); knownPrefix != "" {
				if strings.HasPrefix(knownPrefix, prefix) {
					return cty.True, nil
				}
				if len(knownPrefix) >= len(prefix) {
					// If the prefix we're testing is no longer than the known
					// prefix and it didn't match then the full string with
					// that same prefix can't match either.
					return cty.False, nil
				}
			}
			return cty.UnknownVal(cty.Bool), nil
		}

		str := args[0].AsString()

		if strings.HasPrefix(str, prefix) {
			return cty.True, nil
		}

		return cty.False, nil
	},
})

// EndsWithFunc constructs a function that checks if a string ends with
// a specific suffix using strings.HasSuffix
var EndsWithFunc = function.New(&function.Spec{
	Params: []function.Parameter{
		{
			Name: "str",
			Type: cty.String,
		},
		{
			Name: "suffix",
			Type: cty.String,
		},
	},
	Type:         function.StaticReturnType(cty.Bool),
	RefineResult: refineNotNull,
	Impl: func(args []cty.Value, retType cty.Type) (cty.Value, error) {
		str := args[0].AsString()
		suffix := args[1].AsString()

		if strings.HasSuffix(str, suffix) {
			return cty.True, nil
		}

		return cty.False, nil
	},
})

// ReplaceFunc constructs a function that searches a given string for another
// given substring, and replaces each occurence with a given replacement string.
var ReplaceFunc = function.New(&function.Spec{
	Params: []function.Parameter{
		{
			Name: "str",
			Type: cty.String,
		},
		{
			Name: "substr",
			Type: cty.String,
		},
		{
			Name: "replace",
			Type: cty.String,
		},
	},
	Type:         function.StaticReturnType(cty.String),
	RefineResult: refineNotNull,
	Impl: func(args []cty.Value, retType cty.Type) (ret cty.Value, err error) {
		str := args[0].AsString()
		substr := args[1].AsString()
		replace := args[2].AsString()

		// We search/replace using a regexp if the string is surrounded
		// in forward slashes.
		if len(substr) > 1 && substr[0] == '/' && substr[len(substr)-1] == '/' {
			re, err := regexp.Compile(substr[1 : len(substr)-1])
			if err != nil {
				return cty.UnknownVal(cty.String), err
			}

			return cty.StringVal(re.ReplaceAllString(str, replace)), nil
		}

		return cty.StringVal(strings.Replace(str, substr, replace, -1)), nil
	},
})

// StrContainsFunc searches a given string for another given substring,
// if found the function returns true, otherwise returns false.
var StrContainsFunc = function.New(&function.Spec{
	Params: []function.Parameter{
		{
			Name: "str",
			Type: cty.String,
		},
		{
			Name: "substr",
			Type: cty.String,
		},
	},
	Type: function.StaticReturnType(cty.Bool),
	Impl: func(args []cty.Value, retType cty.Type) (ret cty.Value, err error) {
		str := args[0].AsString()
		substr := args[1].AsString()

		if strings.Contains(str, substr) {
			return cty.True, nil
		}

		return cty.False, nil
	},
})

// TemplateStringFunc renders a template presented either as a literal string
// or as a reference to a string from elsewhere.
func MakeTemplateStringFunc(funcsCb func() map[string]function.Function) function.Function {
	return function.New(&function.Spec{
		Params: []function.Parameter{
			{
				Name: "template",
				Type: customdecode.ExpressionClosureType,
			},
			{
				Name: "vars",
				Type: cty.DynamicPseudoType,
			},
		},
		Type:         function.StaticReturnType(cty.String),
		RefineResult: refineNotNull,
		Impl: func(args []cty.Value, retType cty.Type) (cty.Value, error) {
			templateClosure := customdecode.ExpressionClosureFromVal(args[0])
			varsVal := args[1]

			// Our historical experience with the hashicorp/template provider's
			// template_file data source tells us that situations where authors
			// must write a string template that generates a string template
			// cause all sorts of confusion, because the same syntax ends up
			// being evaluated in two different contexts with different variables
			// in scope.
			//
			// As a compromise to try to meet the (relatively unusual) use-cases
			// where dynamic template generation is needed without creating an
			// attractive nuisance for those who would be better off just writing
			// a plain inline template, this function imposes some constraints
			// on how the template argument may be provided and thus allows us
			// to return slightly more helpful error messages.
			//
			// There are only two valid ways to provide the template argument:
			//  - As a literal quoted string, without any unescaped template
			//    interpolation or control sequences:
			//       template("Hello $${name}", { name = "Alex" })
			//  - As a simple, direct reference to some other symbol that's in
			//    scope.
			//       template(local.greeting_template, { name = "Alex" })
			//
			// Those with more unusual desires, such as dynamically generating
			// a template at runtime by trying to concatenate template chunks
			// together, can still do such things by placing the template
			// construction expression in a separate local value and then passing
			// that local value to the template argument.
			switch expr := templateClosure.Expression.(type) {
			case *hclsyntax.ScopeTraversalExpr:
				// A standalone traversal is always acceptable.
			case *hclsyntax.TemplateExpr:
				// A template expression is valid only when it has just a single
				// part and that part is literal.
				invalid := false
				if len(expr.Parts) != 1 {
					invalid = true
				} else if _, ok := expr.Parts[0].(*hclsyntax.LiteralValueExpr); !ok {
					invalid = true
				}
				if invalid {
					return cty.UnknownVal(retType), function.NewArgErrorf(
						0, "invalid template expression: use $${ and %%%%{ to escape template sequences so that they can be parsed by the templatestring function, rather than by the main Terraform parser",
					)
				}
			case *hclsyntax.TemplateWrapExpr:
				// This situation occurs when someone writes an interpolation-only
				// expression as was required in Terraform v0.11 and earlier.
				// Because older versions of Terraform required this and this
				// habit has been sticky for some authors, we'll return a
				// special error message.
				return cty.UnknownVal(retType), function.NewArgErrorf(
					0, "invalid template expression: either use $${ to escape this template sequence for processing by the templatestring function, or write the inner expression alone to use its value as the template to render",
				)
			default:
				// Nothing else is allowed.
				// Someone who really does want to construct a template dynamically
				// can factor out that construction into a local value and refer
				// to it in the templatestring call, but it's not really feasible
				// to explain that clearly in a short error message so we'll deal
				// with that option on the function's documentation page instead,
				// where we can show a full example.
				return cty.UnknownVal(retType), function.NewArgErrorf(
					0, "invalid template expression: must be either a literal string or a simple reference to a single string from elsewhere",
				)
			}

			templateVal, diags := templateClosure.Value()
			if diags.HasErrors() {
				// With the constraints we imposed above the possible errors
				// here are pretty limited: it must be some kind of invalid
				// traversal. As usual HCL diagnostics don't make for very
				// good function errors but we've already filtered out many
				// common reasons for error here, so we should get here pretty
				// rarely.
				return cty.UnknownVal(retType), function.NewArgErrorf(
					0, "invalid template expression: %s",
					diags.Error(),
				)
			}
			if !templateVal.IsKnown() {
				// We'll need to wait until we actually know what the template is.
				return cty.UnknownVal(retType), nil
			}
			if templateVal.Type() != cty.String || templateVal.IsNull() {
				// We're being a little stricter than usual here and requiring
				// exactly a string, rather than just anything that can convert
				// to one. This is because the stringification of a number or
				// boolean value cannot be a useful template (it wouldn't have
				// any template sequences in it) and so far more likely to be
				// a mistake than actually intentional.
				return cty.UnknownVal(retType), function.NewArgErrorf(
					0, "invalid template expression: a string is required",
				)
			}
			templateVal, templateMarks := templateVal.Unmark()
			templateStr := templateVal.AsString()
			expr, diags := hclsyntax.ParseTemplate([]byte(templateStr), "<templatestring argument>", hcl.Pos{Line: 1, Column: 1})
			if diags.HasErrors() {
				return cty.UnknownVal(retType), function.NewArgErrorf(
					0, "invalid template: %s",
					diags.Error(),
				)
			}

			render := makeRenderTemplateFunc(funcsCb)
			retVal, err := render(expr, varsVal)
			if err != nil {
				return cty.UnknownVal(retType), err
			}
			return retVal.WithMarks(templateMarks), nil
		},
	})
}

func makeRenderTemplateFunc(funcsCb func() map[string]function.Function) func(expr hcl.Expression, varsVal cty.Value) (cty.Value, error) {
	return func(expr hcl.Expression, varsVal cty.Value) (cty.Value, error) {
		if varsTy := varsVal.Type(); !(varsTy.IsMapType() || varsTy.IsObjectType()) {
			return cty.DynamicVal, function.NewArgErrorf(1, "invalid vars value: must be a map") // or an object, but we don't strongly distinguish these most of the time
		}

		ctx := &hcl.EvalContext{
			Variables: varsVal.AsValueMap(),
		}

		// We require all of the variables to be valid HCL identifiers, because
		// otherwise there would be no way to refer to them in the template
		// anyway. Rejecting this here gives better feedback to the user
		// than a syntax error somewhere in the template itself.
		for n := range ctx.Variables {
			if !hclsyntax.ValidIdentifier(n) {
				// This error message intentionally doesn't describe _all_ of
				// the different permutations that are technically valid as an
				// HCL identifier, but rather focuses on what we might
				// consider to be an "idiomatic" variable name.
				return cty.DynamicVal, function.NewArgErrorf(1, "invalid template variable name %q: must start with a letter, followed by zero or more letters, digits, and underscores", n)
			}
		}

		// We'll pre-check references in the template here so we can give a
		// more specialized error message than HCL would by default, so it's
		// clearer that this problem is coming from a templatefile call.
		for _, traversal := range expr.Variables() {
			root := traversal.RootName()
			if _, ok := ctx.Variables[root]; !ok {
				return cty.DynamicVal, function.NewArgErrorf(1, "vars map does not contain key %q, referenced at %s", root, traversal[0].SourceRange())
			}
		}

		givenFuncs := funcsCb() // this callback indirection is to avoid chicken/egg problems
		funcs := make(map[string]function.Function, len(givenFuncs))
		for name, fn := range givenFuncs {
			// We stub some functions out to prevent recursive calls.
			switch name {
			case "templatefile", "core::templatefile":
				funcs[name] = function.New(&function.Spec{
					Params: fn.Params(),
					Type: func(args []cty.Value) (cty.Type, error) {
						return cty.NilType, fmt.Errorf("cannot recursively call templatefile from inside another template function")
					},
				})
				continue
			case "templatestring", "core::templatestring":
				// We stub these ones out to prevent recursive calls.
				funcs[name] = function.New(&function.Spec{
					Params: fn.Params(),
					Type: func(args []cty.Value) (cty.Type, error) {
						return cty.NilType, fmt.Errorf("cannot recursively call templatestring from inside another template function")
					},
				})
				continue
			}
			funcs[name] = fn
		}
		ctx.Functions = funcs

		val, diags := expr.Value(ctx)
		if diags.HasErrors() {
			return cty.DynamicVal, diags
		}
		return val, nil
	}
}

// Replace searches a given string for another given substring,
// and replaces all occurences with a given replacement string.
func Replace(str, substr, replace cty.Value) (cty.Value, error) {
	return ReplaceFunc.Call([]cty.Value{str, substr, replace})
}

func StrContains(str, substr cty.Value) (cty.Value, error) {
	return StrContainsFunc.Call([]cty.Value{str, substr})
}
