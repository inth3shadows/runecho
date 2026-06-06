package parser

// Precision corpus for the JS/TS/JSX shallow (regex) parser.
//
// PURPOSE: Document *current, honest* output for cases that sit at the edges
// of what a line-regex approach can correctly classify.  This corpus is
// evidence for the future AST go/no-go decision (issue #15).  Each case
// carries a KNOWN: comment where the regex over- or under-captures; those
// cases assert the imperfect output so that future refactors know what they
// are changing.
//
// SCOPE: Tests that expose a panic are fixed (a panic is never acceptable);
// all other imperfections are left as-is and documented.

import (
	"testing"
)

func TestJSParser_Precision(t *testing.T) {
	p := NewJSParser()

	tests := []struct {
		name      string
		source    string
		wantFuncs []string
		wantClass []string
		wantExp   []string
		note      string // KNOWN: annotation — empty when behavior is correct
	}{
		// ------------------------------------------------------------------ //
		// Nested functions inside callbacks
		// ------------------------------------------------------------------ //
		{
			name: "nested_function_inside_array_forEach",
			source: `
function outer() {
	[1, 2, 3].forEach(function inner(x) {
		return x * 2;
	});
}
`,
			// funcDeclRegex requires (?:^|\s) before the "function" keyword.
			// In "forEach(function inner(x)", the character before "function"
			// is "(" — not whitespace — so "inner" is NOT captured.  "outer" is
			// captured (it follows a newline).
			// Correct behavior: callback-argument named functions are silently
			// missed, not over-promoted.  The under-capture cost is an FN for
			// indexing (inner is not recorded); the alternative (no anchor) would
			// add noise from every named callback.
			wantFuncs: []string{"outer"},
			wantClass: []string{},
			wantExp:   []string{},
			note:      "KNOWN: named function in callback argument not captured (character before 'function' is '(', not whitespace)",
		},
		{
			name: "nested_arrow_inside_map",
			source: `
const transform = (items) => items.map(x => x * 2);
const process = items => items.filter(y => y > 0);
`,
			// KNOWN: arrowFuncRegex requires "const|let|var <name> = ... =>".
			// The inner "x => x*2" and "y => y>0" do not start with a
			// declaration keyword, so they are correctly NOT captured.
			// "transform" IS captured (const decl + arrow); "process" IS
			// captured (const decl + single-param arrow without parens).
			wantFuncs: []string{"process", "transform"},
			wantClass: []string{},
			wantExp:   []string{},
			note:      "",
		},
		{
			name: "nested_function_inside_conditional",
			source: `
function main() {
	if (flag) {
		function helper() { return 1; }
	}
}
`,
			// KNOWN: "helper" is nested inside an if-block but the line-regex
			// has no scope awareness — it is promoted to top-level Functions.
			wantFuncs: []string{"helper", "main"},
			wantClass: []string{},
			wantExp:   []string{},
			note:      "KNOWN: nested function inside conditional promoted to top-level",
		},
		{
			name: "callback_returning_named_function",
			source: `
setTimeout(function tick() {
	refresh();
}, 1000);
`,
			// funcDeclRegex requires (?:^|\s) before "function".  In
			// "setTimeout(function tick()", the "(" before "function" is not
			// whitespace, so "tick" is NOT captured.  Consistent with the
			// forEach case: neither over-promotes callback-argument functions.
			wantFuncs: []string{},
			wantClass: []string{},
			wantExp:   []string{},
			note:      "KNOWN: named callback function not captured (leading '(' is not whitespace)",
		},

		// ------------------------------------------------------------------ //
		// Factory-returned functions
		// ------------------------------------------------------------------ //
		{
			name: "factory_returning_arrow",
			source: `
function makeAdder(n) {
	return (x) => x + n;
}
`,
			// The returned arrow is anonymous (no name after "=>"), so it is
			// not captured.  Only "makeAdder" appears.  Correct behavior.
			wantFuncs: []string{"makeAdder"},
			wantClass: []string{},
			wantExp:   []string{},
			note:      "",
		},
		{
			name: "factory_returning_named_function_expression",
			source: `
function makeCounter() {
	const increment = function() { return 1; };
	return increment;
}
`,
			// KNOWN: funcExprRegex captures "const increment = function()" even
			// though "increment" is a local variable inside makeCounter, not a
			// top-level definition.
			wantFuncs: []string{"increment", "makeCounter"},
			wantClass: []string{},
			wantExp:   []string{},
			note:      "KNOWN: local function expression inside factory promoted to top-level",
		},

		// ------------------------------------------------------------------ //
		// Class expressions
		// ------------------------------------------------------------------ //
		{
			name: "class_expression_named",
			source: `
const Foo = class FooInternal {
	method() {}
};
`,
			// classDeclRegex matches "class <Name>" regardless of whether it is
			// a declaration or expression, so "FooInternal" is captured.
			// "Foo" (the variable binding) is NOT captured because classDeclRegex
			// only looks for the "class" keyword, not the LHS of the assignment.
			// This is a mild under-capture for the variable name.
			wantFuncs: []string{},
			wantClass: []string{"FooInternal"},
			wantExp:   []string{},
			note:      "KNOWN: class-expression binding name (Foo) not captured; only the internal class name (FooInternal) is",
		},
		{
			name: "class_expression_anonymous",
			source: `
const Widget = class {
	render() {}
};
`,
			// Anonymous class expression: the "class" keyword is not followed by
			// an identifier (it is followed by "{"), so classDeclRegex yields
			// nothing.  "Widget" is not captured either (no class keyword before it).
			// Under-capture: Widget is effectively a class but appears in neither
			// Functions nor Classes.
			wantFuncs: []string{},
			wantClass: []string{},
			wantExp:   []string{},
			note:      "KNOWN: anonymous class expression binding (Widget) not captured",
		},

		// ------------------------------------------------------------------ //
		// JSX function components (.jsx/.tsx extension parity)
		// ------------------------------------------------------------------ //
		{
			name: "jsx_default_export_function_component",
			source: `
import React from 'react';

export default function App() {
	return (
		<div className="app">
			<h1>Hello</h1>
		</div>
	);
}
`,
			// funcDeclRegex captures "App" (whitespace before "function").
			// exportDeclRegex does NOT match "export default function App" because
			// "default" is not in its keyword list (const|let|var|function|class|
			// async function) — "default function" as a combined form is not handled.
			// exportDefaultRegex matches "export default function App()" and
			// captures "function" as the identifier (the first \w+ after "default").
			// KNOWN: over-capture — "function" (a keyword) appears in Exports
			// instead of "App".  The regex cannot distinguish "export default Foo"
			// (plain ident) from "export default function Foo()" (declaration).
			wantFuncs: []string{"App"},
			wantClass: []string{},
			wantExp:   []string{"function"},
			note:      "KNOWN: 'export default function App()' captures keyword 'function' as export name, not 'App'",
		},
		{
			name: "jsx_arrow_component",
			source: `
import React from 'react';

const Button = ({ label, onClick }) => (
	<button onClick={onClick}>{label}</button>
);

export { Button };
`,
			// arrowFuncRegex: "(?:const|let|var)\s+(\w+)\s*=\s*(?:async\s+)?(?:\([^)]*\)|[\w]+)\s*=>"
			// In "const Button = ({ label, onClick }) =>":
			//   \([^)]*\) matches "({ label, onClick }" — the pattern uses [^)]*
			//   which matches everything up to the first ")", giving "{ label, onClick ".
			//   The outer ")" then closes the group, and \s*=> matches " =>".
			// So "Button" IS captured in Functions — the destructuring does not
			// prevent the match because [^)]* greedily stops at the first ")".
			// Correct behavior: arrow components with simple destructuring are indexed.
			wantFuncs: []string{"Button"},
			wantClass: []string{},
			wantExp:   []string{"Button"},
			note:      "",
		},
		{
			name: "jsx_arrow_component_simple_params",
			source: `
import React from 'react';

const Icon = (props) => <span>{props.name}</span>;

export { Icon };
`,
			// "(props)" matches \([^)]*\) so "Icon" IS captured as a function.
			wantFuncs: []string{"Icon"},
			wantClass: []string{},
			wantExp:   []string{"Icon"},
			note:      "",
		},
		{
			name: "tsx_typed_arrow_component",
			source: `
import React from 'react';

interface Props {
	title: string;
}

const Header: React.FC<Props> = ({ title }) => (
	<header><h1>{title}</h1></header>
);

export default Header;
`,
			// The LHS is "const Header: React.FC<Props> = ..."; arrowFuncRegex
			// looks for "const <word> =" but the colon annotation between the
			// name and "=" breaks the match pattern
			// ("(?:const|let|var)\s+(\w+)\s*=").  The ": React.FC<Props>" is
			// between name and "=", so the regex does NOT match.
			//
			// KNOWN: typed arrow component under-captured — "Header" not in Functions.
			// exportDefaultRegex would capture the plain "export default Header"
			// but "Header" is the identifier so it IS in Exports.
			wantFuncs: []string{},
			wantClass: []string{},
			wantExp:   []string{"Header"},
			note:      "KNOWN: TypeScript-annotated arrow component (const Name: Type = ...) not captured in Functions",
		},

		// ------------------------------------------------------------------ //
		// Export wrappers and re-exports
		// ------------------------------------------------------------------ //
		{
			name: "export_named_group",
			source: `
function alpha() {}
function beta() {}

export { alpha, beta };
`,
			wantFuncs: []string{"alpha", "beta"},
			wantClass: []string{},
			wantExp:   []string{"alpha", "beta"},
			note:      "",
		},
		{
			name: "export_named_with_alias",
			source: `
function internal() {}

export { internal as public };
`,
			// "export { internal as public }" — the handler takes the part
			// before " as " which is "internal".  Correct behavior: the
			// exported name at the use-site is "public" but we record the
			// local name "internal".  This is a deliberate choice (we index
			// local definitions, not the re-exported alias).
			wantFuncs: []string{"internal"},
			wantClass: []string{},
			wantExp:   []string{"internal"},
			note:      "KNOWN: aliased export records pre-alias local name, not the exported alias",
		},
		{
			name: "reexport_from_module_named",
			source: `
export { foo, bar } from './other';
`,
			// exportNamedRegex captures { foo, bar } regardless of whether
			// a "from" clause is present.  "foo" and "bar" appear in Exports
			// even though they are re-exported from another module and no local
			// definition exists.
			// KNOWN: re-export from adds names to Exports without a corresponding
			// local definition in Functions/Classes.
			wantFuncs: []string{},
			wantClass: []string{},
			wantExp:   []string{"bar", "foo"},
			note:      "KNOWN: re-export from './other' adds to Exports with no local definition",
		},
		{
			name: "reexport_star_from_module",
			source: `
export * from './utils';
`,
			// "export *" has no identifier to capture.  exportNamedRegex,
			// exportDeclRegex, and exportDefaultRegex all fail to match.
			// Correct: we cannot enumerate the symbols re-exported by "*".
			wantFuncs: []string{},
			wantClass: []string{},
			wantExp:   []string{},
			note:      "KNOWN: export * from is silently ignored — re-exported symbols not enumerable by regex",
		},
		{
			name: "reexport_star_as_namespace",
			source: `
export * as utils from './utils';
`,
			// "export * as utils" — exportDeclRegex pattern is
			// "export\s+(?:const|let|var|function|class|async\s+function)\s+(\w+)";
			// "* as" does not match any of those keywords.
			// exportNamedRegex requires "{...}" — not present here.
			// KNOWN: "export * as utils" under-captured — "utils" does not appear
			// in Exports even though it is a valid named re-export.
			wantFuncs: []string{},
			wantClass: []string{},
			wantExp:   []string{},
			note:      "KNOWN: 'export * as name from' not captured in Exports",
		},
		{
			name: "export_default_arrow_function",
			source: `
export default () => {
	return 42;
};
`,
			// "export default" followed by an anonymous arrow — exportDefaultRegex
			// requires an identifier after "default".  "() =>" has no name.
			// Not captured.  Under-capture: the module's default export is
			// not recorded.
			wantFuncs: []string{},
			wantClass: []string{},
			wantExp:   []string{},
			note:      "KNOWN: anonymous default export arrow not captured in Exports",
		},
		{
			name: "export_default_object_literal",
			source: `
export default {
	foo: 1,
	bar: 2,
};
`,
			// "export default {" — the token after "default" is "{", not an
			// identifier.  Not captured.
			wantFuncs: []string{},
			wantClass: []string{},
			wantExp:   []string{},
			note:      "KNOWN: 'export default { ... }' object literal not captured in Exports",
		},

		// ------------------------------------------------------------------ //
		// Class declarations with export forms
		// ------------------------------------------------------------------ //
		{
			name: "export_class_declaration",
			source: `
export class Service {
	constructor() {}
}
`,
			// classDeclRegex: "(?:^|\s)(?:export\s+(?:default\s+)?)?class\s+(\w+)"
			// Matches "export class Service" — "Service" captured in Classes.
			// exportDeclRegex: "export\s+(?:const|...|class|...)\s+(\w+)"
			// Also matches — "Service" captured in Exports too.
			wantFuncs: []string{},
			wantClass: []string{"Service"},
			wantExp:   []string{"Service"},
			note:      "",
		},
		{
			name: "export_default_class",
			source: `
export default class Controller {
	handle() {}
}
`,
			// classDeclRegex captures "Controller" (handles "export default class Name").
			// exportDeclRegex does NOT match ("default" is not in its keyword list).
			// exportDefaultRegex matches "export default class" and captures "class"
			// as the identifier (same over-capture pattern as "export default function").
			// KNOWN: over-capture — "class" (a keyword) appears in Exports instead
			// of nothing or "Controller".
			wantFuncs: []string{},
			wantClass: []string{"Controller"},
			wantExp:   []string{"class"},
			note:      "KNOWN: 'export default class Name' captures keyword 'class' as export name, not 'Controller'",
		},

		// ------------------------------------------------------------------ //
		// JSX file with mixed content (end-to-end parity check)
		// ------------------------------------------------------------------ //
		{
			name: "jsx_mixed_component_file",
			source: `
import React, { useState } from 'react';
import PropTypes from 'prop-types';

const MAX_ITEMS = 10;

function useCounter(initial) {
	const [count, setCount] = useState(initial);
	return { count, setCount };
}

class ErrorBoundary extends React.Component {
	componentDidCatch(error) {
		console.error(error);
	}
}

export default function App() {
	const { count } = useCounter(0);
	return <div>{count}</div>;
}

export { ErrorBoundary };
`,
			// "useCounter" is top-level function declaration — captured.
			// "App" is also captured by funcDeclRegex (whitespace before "function").
			// "ErrorBoundary" is a class — captured.
			// Exports: exportNamedRegex fires for "export { ErrorBoundary }";
			// exportDefaultRegex fires for "export default function App()" and
			// captures "function" (keyword) not "App" — same over-capture as the
			// standalone jsx_default_export_function_component case.
			// KNOWN: "export default function App()" contributes "function" to Exports.
			wantFuncs: []string{"App", "useCounter"},
			wantClass: []string{"ErrorBoundary"},
			wantExp:   []string{"ErrorBoundary", "function"},
			note:      "KNOWN: 'export default function App()' contributes 'function' (keyword) to Exports, not 'App'",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fs, err := p.Parse(tt.source)
			if err != nil {
				t.Fatalf("Parse returned unexpected error: %v", err)
			}

			if !equalStringSlices(fs.Functions, tt.wantFuncs) {
				t.Errorf("Functions:\n  got  %v\n  want %v\n  note: %s", fs.Functions, tt.wantFuncs, tt.note)
			}
			if !equalStringSlices(fs.Classes, tt.wantClass) {
				t.Errorf("Classes:\n  got  %v\n  want %v\n  note: %s", fs.Classes, tt.wantClass, tt.note)
			}
			if !equalStringSlices(fs.Exports, tt.wantExp) {
				t.Errorf("Exports:\n  got  %v\n  want %v\n  note: %s", fs.Exports, tt.wantExp, tt.note)
			}
		})
	}
}

// TestJSParser_SupportsExtension_JSX verifies the fix for issue #5.
func TestJSParser_SupportsExtension_JSX(t *testing.T) {
	p := NewJSParser()

	cases := []struct {
		ext  string
		want bool
	}{
		{".js", true},
		{".ts", true},
		{".jsx", true},
		{".tsx", true},
		{".gs", true},
		{".py", false},
		{".go", false},
	}

	for _, tc := range cases {
		got := p.SupportsExtension(tc.ext)
		if got != tc.want {
			t.Errorf("SupportsExtension(%q) = %v, want %v", tc.ext, got, tc.want)
		}
	}
}
