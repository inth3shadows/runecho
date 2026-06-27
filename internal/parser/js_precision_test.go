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
			// AST: "inner" is a named function expression used as a callback
			// argument. The walker captures top-level declarations and class
			// methods only — it does not recurse function bodies, and bare
			// function_expressions not bound to a variable are intentionally
			// skipped (see js.go) — so "inner" is correctly NOT captured.
			// "outer" is a top-level declaration.
			wantFuncs: []string{"outer"},
			wantClass: []string{},
			wantExp:   []string{},
			note:      "named function in a callback argument is not captured (body-level function_expression)",
		},
		{
			name: "nested_arrow_inside_map",
			source: `
const transform = (items) => items.map(x => x * 2);
const process = items => items.filter(y => y > 0);
`,
			// AST: "transform" and "process" are top-level arrow consts (a
			// variable_declarator bound to an arrow_function). The inner
			// "x => x*2" and "y => y>0" live inside those bodies, which are not
			// recursed, so they are correctly NOT captured.
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
			// AST: "tick" is a named function expression passed as a call
			// argument. Bare function_expressions not bound to a variable are
			// intentionally not captured (see js.go), consistent with the
			// forEach case — neither over-promotes callback-argument functions.
			wantFuncs: []string{},
			wantClass: []string{},
			wantExp:   []string{},
			note:      "named callback function is not captured (body-level function_expression)",
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
			// AST: the returned arrow is anonymous and lives inside makeAdder's
			// body (not a top-level binding, body not recursed), so only
			// "makeAdder" is captured.
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
			// AST captures "App" (a top-level function declaration). Exports are
			// still a regex pass: exportDefaultRegex's alternation captures the
			// identifier after "function", so "App" also lands in Exports.
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
			// AST: "Button" is a top-level arrow const (a variable_declarator
			// bound to an arrow_function), captured regardless of the
			// destructured-object parameter. Exports come from the regex pass
			// ("export { Button }").
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
			// AST: "Icon" is a top-level arrow const; the single parenthesized
			// parameter is irrelevant to capture. Exports come from the regex pass.
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
			// This case calls Parse() (no extension), so it uses the JavaScript
			// grammar, which does not understand the TypeScript annotation in
			// "const Header: React.FC<Props> = ...". The annotated binding does
			// not parse as a clean arrow-const, so "Header" is NOT captured in
			// Functions. Real .tsx parsing is covered via ParseExt(".tsx").
			// Exports still come from the regex pass: "export default Header"
			// records the identifier "Header".
			wantFuncs: []string{},
			wantClass: []string{},
			wantExp:   []string{"Header"},
			note:      "TS-annotated arrow component not captured under the bare JS grammar (use ParseExt for .ts/.tsx)",
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
			// "export { internal as public }" exports "public" — that is the
			// name consumers import. The local function "internal" remains a
			// local definition in Functions; the export records the alias.
			wantFuncs: []string{"internal"},
			wantClass: []string{},
			wantExp:   []string{"public"},
			note:      "",
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
			name: "reexport_type_only_from_module",
			source: `
export type { UserDto, ApiResponse } from './types';
`,
			// TS type-only re-export: the `type` keyword sits between export and the
			// brace (idiomatic under isolatedModules / verbatimModuleSyntax).
			// exportNamedRegex now tolerates the optional `type ` so the re-exported
			// names land in Exports, matching the value form `export { ... } from`.
			// No local definition exists (same as reexport_from_module_named).
			wantFuncs: []string{},
			wantClass: []string{},
			wantExp:   []string{"ApiResponse", "UserDto"},
			note:      "TS export type { ... } from re-export now captured",
		},
		{
			name: "export_type_only_aliased",
			source: `
export type { Internal as Public } from './types';
`,
			// The "as" alias handling applies to type-only re-exports too: the
			// consumer-visible name (Public) is recorded, not the local one.
			wantFuncs: []string{},
			wantClass: []string{},
			wantExp:   []string{"Public"},
			note:      "TS export type { X as Y } records the alias",
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
			// "export * as utils from './utils'" binds the namespace "utils",
			// captured by exportStarAsRegex.
			wantFuncs: []string{},
			wantClass: []string{},
			wantExp:   []string{"utils"},
			note:      "",
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
