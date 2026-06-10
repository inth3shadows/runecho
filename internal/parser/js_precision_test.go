package parser

// Precision corpus for the JS/TS/JSX parser.
//
// PURPOSE: Pin the parser's output on cases that sit at the edges of symbol
// classification. The parser is now AST-backed (tree-sitter, issue #19): it
// captures top-level functions/classes plus class methods (qualified as
// Class.method), at the same "top-level + methods" altitude as the Go parser —
// nested closures inside function bodies are deliberately NOT captured.
// Imports/exports are still extracted by regex. Cases here assert the AST
// output; several previously documented regex over/under-captures (KNOWN:
// notes) are now resolved and noted as such.
//
// Note: cases call Parse() (no extension), which uses the JavaScript grammar.
// The JS grammar also parses JSX. TypeScript-only syntax is covered separately
// via ParseExt(".ts"/".tsx").

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
			// AST: "helper" is nested inside main's body. The JS/TS parser uses a
			// top-level altitude (top-level decls + class methods, no function-body
			// recursion — see js.go), so helper is correctly NOT captured. The old
			// regex wrongly promoted it.
			wantFuncs: []string{"main"},
			wantClass: []string{},
			wantExp:   []string{},
			note:      "",
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
			// AST: "increment" is a local function expression inside makeCounter's
			// body; with the top-level altitude it is correctly NOT promoted. The
			// old regex wrongly captured it.
			wantFuncs: []string{"makeCounter"},
			wantClass: []string{},
			wantExp:   []string{},
			note:      "",
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
			// AST: a class expression bound to a variable is recorded under the
			// binding name "Foo" (how callers reference it), with its method
			// qualified as "Foo.method". The internal class name (FooInternal) is
			// not used.
			wantFuncs: []string{"Foo.method"},
			wantClass: []string{"Foo"},
			wantExp:   []string{},
			note:      "",
		},
		{
			name: "class_expression_anonymous",
			source: `
const Widget = class {
	render() {}
};
`,
			// AST: an anonymous class expression bound to "Widget" is recorded
			// under the binding name, with its method as "Widget.render". The old
			// regex captured neither.
			wantFuncs: []string{"Widget.render"},
			wantClass: []string{"Widget"},
			wantExp:   []string{},
			note:      "",
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
			// exportDefaultRegex now uses alternation to capture the identifier
			// after "function", so "App" lands in Exports correctly.
			wantFuncs: []string{"App"},
			wantClass: []string{},
			wantExp:   []string{"App"},
			note:      "",
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
			// AST: "Service" class captured; its constructor is a method, recorded
			// qualified as "Service.constructor" (parity with Python's __init__).
			// "Service" is also in Exports via the regex export pass.
			wantFuncs: []string{"Service.constructor"},
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
			// AST: "Controller" class captured; its method "handle" recorded
			// qualified as "Controller.handle". "Controller" also in Exports.
			wantFuncs: []string{"Controller.handle"},
			wantClass: []string{"Controller"},
			wantExp:   []string{"Controller"},
			note:      "",
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
			// AST: top-level functions "useCounter" and "App"; class
			// "ErrorBoundary" with its method qualified as
			// "ErrorBoundary.componentDidCatch". Exports from the regex pass.
			wantFuncs: []string{"App", "ErrorBoundary.componentDidCatch", "useCounter"},
			wantClass: []string{"ErrorBoundary"},
			wantExp:   []string{"App", "ErrorBoundary"},
			note:      "",
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
