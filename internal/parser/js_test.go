package parser

import (
	"testing"
)

func TestJSParser_Parse_Determinism(t *testing.T) {
	source := `
import React from 'react';
import { useState } from 'react';
const axios = require('axios');

function greet(name) {
	return "Hello " + name;
}

async function fetchData() {
	return await fetch('/api/data');
}

const add = (a, b) => a + b;

class User {
	constructor(name) {
		this.name = name;
	}
}

export { greet };
export const API_URL = "http://example.com";
export default User;
`

	parser := NewJSParser()

	// Parse 100 times
	results := make([]FileStructure, 100)
	for i := 0; i < 100; i++ {
		result, err := parser.Parse(source)
		if err != nil {
			t.Fatalf("Parse failed on iteration %d: %v", i, err)
		}
		results[i] = result
	}

	// Verify all results are identical
	first := results[0]
	for i := 1; i < 100; i++ {
		if !equalFileStructure(first, results[i]) {
			t.Errorf("Parse result %d differs from first result", i)
			t.Logf("First: %+v", first)
			t.Logf("Current: %+v", results[i])
		}
	}
}

func TestJSParser_Parse_Sorting(t *testing.T) {
	source := `
function zebra() {}
function alpha() {}
function beta() {}

class Zulu {}
class Alpha {}

export { zebra, alpha };
`

	parser := NewJSParser()
	result, err := parser.Parse(source)
	if err != nil {
		t.Fatalf("Parse failed: %v", err)
	}

	// Verify functions are sorted
	expectedFuncs := []string{"alpha", "beta", "zebra"}
	if !equalStringSlices(result.Functions, expectedFuncs) {
		t.Errorf("Functions not sorted: got %v, want %v", result.Functions, expectedFuncs)
	}

	// Verify classes are sorted
	expectedClasses := []string{"Alpha", "Zulu"}
	if !equalStringSlices(result.Classes, expectedClasses) {
		t.Errorf("Classes not sorted: got %v, want %v", result.Classes, expectedClasses)
	}

	// Verify exports are sorted
	expectedExports := []string{"alpha", "zebra"}
	if !equalStringSlices(result.Exports, expectedExports) {
		t.Errorf("Exports not sorted: got %v, want %v", result.Exports, expectedExports)
	}
}

func TestJSParser_Parse_Deduplication(t *testing.T) {
	source := `
function foo() {}
function foo() {}  // Duplicate

class Bar {}
class Bar {}  // Duplicate

export { foo };
export { foo };  // Duplicate
`

	parser := NewJSParser()
	result, err := parser.Parse(source)
	if err != nil {
		t.Fatalf("Parse failed: %v", err)
	}

	// Should have only one of each
	if len(result.Functions) != 1 || result.Functions[0] != "foo" {
		t.Errorf("Expected single 'foo' function, got %v", result.Functions)
	}

	if len(result.Classes) != 1 || result.Classes[0] != "Bar" {
		t.Errorf("Expected single 'Bar' class, got %v", result.Classes)
	}

	if len(result.Exports) != 1 || result.Exports[0] != "foo" {
		t.Errorf("Expected single 'foo' export, got %v", result.Exports)
	}
}

func TestJSParser_Parse_TypeScript(t *testing.T) {
	source := `
interface User {
	name: string;
	age: number;
}

function processUser(user: User): void {
	console.log(user.name);
}

class Service<T> {
	data: T;
}

export { processUser };
`

	parser := NewJSParser()
	// TypeScript-specific syntax (type annotations, generics) needs the TS
	// grammar, which the generator selects by extension via ParseExt.
	result, err := parser.ParseExt(source, ".ts")
	if err != nil {
		t.Fatalf("Parse failed: %v", err)
	}

	// processUser function captured; interface User and class Service captured
	// as classes (AST now extracts interfaces/type-likes too).
	if len(result.Functions) == 0 {
		t.Errorf("Expected to find processUser function, got %v", result.Functions)
	}

	if len(result.Classes) == 0 {
		t.Errorf("Expected to find Service class, got %v", result.Classes)
	}
}

func TestJSParser_ExportDefault_NamedFunctionAndClass(t *testing.T) {
	cases := []struct {
		name    string
		source  string
		wantExp []string
	}{
		{
			name:    "export default function",
			source:  "export default function Foo() {}",
			wantExp: []string{"Foo"},
		},
		{
			name:    "export default async function",
			source:  "export default async function Bar() {}",
			wantExp: []string{"Bar"},
		},
		{
			name:    "export default class",
			source:  "export default class Baz {}",
			wantExp: []string{"Baz"},
		},
		{
			name:    "export default identifier unchanged",
			source:  "export default MyComponent",
			wantExp: []string{"MyComponent"},
		},
		{
			name:    "export default anonymous function — no export name",
			source:  "export default function() {}",
			wantExp: []string{},
		},
	}

	p := NewJSParser()
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			result, err := p.Parse(tc.source)
			if err != nil {
				t.Fatalf("Parse error: %v", err)
			}
			if !equalStringSlices(result.Exports, tc.wantExp) {
				t.Errorf("Exports = %v, want %v", result.Exports, tc.wantExp)
			}
		})
	}
}

func TestJSParser_ExportDestructure(t *testing.T) {
	cases := []struct {
		name    string
		source  string
		wantExp []string
	}{
		{
			name:    "object destructure",
			source:  "export const { foo, bar } = config;",
			wantExp: []string{"bar", "foo"},
		},
		{
			name:    "array destructure",
			source:  "export const [ first, second ] = arr;",
			wantExp: []string{"first", "second"},
		},
		{
			name:    "renamed object destructure keeps bound names",
			source:  "export const { a: renamedA, b: renamedB } = x;",
			wantExp: []string{"renamedA", "renamedB"},
		},
		{
			name:    "object destructure with defaults",
			source:  "export const { a = 1, b = 2 } = x;",
			wantExp: []string{"a", "b"},
		},
		{
			name:    "array destructure with defaults",
			source:  "export const [ a = 1, b = 2 ] = y;",
			wantExp: []string{"a", "b"},
		},
		{
			name:    "renamed with default keeps bound name",
			source:  "export const { a: renamedA = 1 } = x;",
			wantExp: []string{"renamedA"},
		},
		{
			name:    "export let destructure",
			source:  "export let { foo, bar } = x;",
			wantExp: []string{"bar", "foo"},
		},
		{
			name:    "export var destructure",
			source:  "export var [ a, b ] = y;",
			wantExp: []string{"a", "b"},
		},
		{
			name:    "object rest element binds trailing name",
			source:  "export const { a, ...rest } = x;",
			wantExp: []string{"a", "rest"},
		},
		{
			name:    "array rest element binds trailing name",
			source:  "export const [ first, ...others ] = arr;",
			wantExp: []string{"first", "others"},
		},
		{
			name:    "plain export const still works (no regression)",
			source:  "export const Widget = 1;",
			wantExp: []string{"Widget"},
		},
	}

	p := NewJSParser()
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			result, err := p.Parse(tc.source)
			if err != nil {
				t.Fatalf("Parse error: %v", err)
			}
			if !equalStringSlices(result.Exports, tc.wantExp) {
				t.Errorf("Exports = %v, want %v", result.Exports, tc.wantExp)
			}
		})
	}
}

func TestJSParser_WildcardReexport(t *testing.T) {
	cases := []struct {
		name     string
		source   string
		wantExp  []string
		wantWild []string
	}{
		{
			name:     "bare star re-export recorded as wildcard, not export",
			source:   "export * from './mod';",
			wantExp:  nil,
			wantWild: []string{"./mod"},
		},
		{
			name:     "namespace star re-export still binds ns, no wildcard marker",
			source:   "export * as ns from './m';",
			wantExp:  []string{"ns"},
			wantWild: nil,
		},
		{
			name:     "multiple bare star re-exports from different modules",
			source:   "export * from './a';\nexport * from './b';",
			wantExp:  nil,
			wantWild: []string{"./a", "./b"},
		},
		{
			name:     "duplicate bare star re-export deduplicated",
			source:   "export * from './a';\nexport * from './a';",
			wantExp:  nil,
			wantWild: []string{"./a"},
		},
		{
			name:     "bare and namespace forms coexist independently",
			source:   "export * from './a';\nexport * as ns from './b';",
			wantExp:  []string{"ns"},
			wantWild: []string{"./a"},
		},
		{
			name:     "no star re-export at all",
			source:   "export const Widget = 1;",
			wantExp:  []string{"Widget"},
			wantWild: nil,
		},
	}

	p := NewJSParser()
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			result, err := p.Parse(tc.source)
			if err != nil {
				t.Fatalf("Parse error: %v", err)
			}
			if !equalStringSlices(result.Exports, tc.wantExp) {
				t.Errorf("Exports = %v, want %v", result.Exports, tc.wantExp)
			}
			if !equalStringSlices(result.WildcardReexports, tc.wantWild) {
				t.Errorf("WildcardReexports = %v, want %v", result.WildcardReexports, tc.wantWild)
			}
		})
	}
}

func TestJSParser_MultiNameDeclExport(t *testing.T) {
	cases := []struct {
		name    string
		source  string
		wantExp []string
	}{
		{
			name:    "multi_name_const",
			source:  "export const A = 1, B = 2, C = 3;",
			wantExp: []string{"A", "B", "C"},
		},
		{
			name:    "multi_name_let",
			source:  "export let X = 1, Y = 2;",
			wantExp: []string{"X", "Y"},
		},
		{
			name:    "single_name_const_unaffected",
			source:  "export const Widget = 1;",
			wantExp: []string{"Widget"},
		},
		{
			name:    "initializer_with_internal_commas",
			source:  "export const A = f(1, 2), B = 3;",
			wantExp: []string{"A", "B"},
		},
		{
			name:    "single_name_var_unaffected",
			source:  "export var Count = 0;",
			wantExp: []string{"Count"},
		},
	}

	p := NewJSParser()
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			result, err := p.Parse(tc.source)
			if err != nil {
				t.Fatalf("Parse error: %v", err)
			}
			if !equalStringSlices(result.Exports, tc.wantExp) {
				t.Errorf("Exports = %v, want %v", result.Exports, tc.wantExp)
			}
		})
	}
}

func TestJSParser_SupportsExtension(t *testing.T) {
	parser := NewJSParser()

	tests := []struct {
		ext      string
		expected bool
	}{
		{".js", true},
		{".mjs", true},
		{".cjs", true},
		{".ts", true},
		{".gs", true},
		{".py", false},
		{".go", false},
		{".txt", false},
		{"", false},
	}

	for _, tt := range tests {
		result := parser.SupportsExtension(tt.ext)
		if result != tt.expected {
			t.Errorf("SupportsExtension(%q) = %v, want %v", tt.ext, result, tt.expected)
		}
	}
}

// TestJSParser_ImportsValue asserts on result.Imports directly — prior to the
// AST-based import/export walk (issue #89), no test in this file checked
// Imports' contents at all, only indirectly via the determinism/fuzz checks.
// FileStructure.Imports is a list of module specifiers (paths), not bound
// names, so an aliased/renamed import still just contributes its source path.
func TestJSParser_ImportsValue(t *testing.T) {
	cases := []struct {
		name    string
		source  string
		ext     string
		wantImp []string
	}{
		{
			name:    "default_import",
			source:  "import React from 'react';",
			wantImp: []string{"react"},
		},
		{
			name:    "named_import",
			source:  "import { useState } from 'react';",
			wantImp: []string{"react"},
		},
		{
			name:    "named_import_with_alias",
			source:  "import { useEffect as fx } from 'react';",
			wantImp: []string{"react"},
		},
		{
			name:    "namespace_import",
			source:  "import * as ns from './mod';",
			wantImp: []string{"./mod"},
		},
		{
			name:    "default_and_named_combined",
			source:  "import Def, { a, b } from './mod';",
			wantImp: []string{"./mod"},
		},
		{
			name:    "bare_side_effect_import",
			source:  "import './side-effect';",
			wantImp: []string{"./side-effect"},
		},
		{
			name:    "cjs_require",
			source:  "const axios = require('axios');",
			wantImp: []string{"axios"},
		},
		{
			name:    "multiple_esm_and_cjs_together",
			source:  "import React from 'react';\nconst axios = require('axios');",
			wantImp: []string{"axios", "react"},
		},
		{
			name:    "ts_import_type",
			source:  "import type { Foo } from './types';",
			ext:     ".ts",
			wantImp: []string{"./types"},
		},
		{
			name:    "ts_import_require_equals",
			source:  "import x = require('mod');",
			ext:     ".ts",
			wantImp: []string{"mod"},
		},
	}

	p := NewJSParser()
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			result, err := p.ParseExt(tc.source, tc.ext)
			if err != nil {
				t.Fatalf("Parse error: %v", err)
			}
			if !equalStringSlices(result.Imports, tc.wantImp) {
				t.Errorf("Imports = %v, want %v", result.Imports, tc.wantImp)
			}
		})
	}
}

// Helper functions

func equalFileStructure(a, b FileStructure) bool {
	return equalStringSlices(a.Imports, b.Imports) &&
		equalStringSlices(a.Functions, b.Functions) &&
		equalStringSlices(a.Classes, b.Classes) &&
		equalStringSlices(a.Exports, b.Exports)
}

// Regression: `export default abstract class Foo` — the fallback default-export
// regex must capture the class NAME, not the `abstract` modifier.
func TestExtractExports_DefaultAbstractClass(t *testing.T) {
	got := extractExports("export default abstract class Foo {}\n")
	has := func(name string) bool {
		for _, e := range got {
			if e == name {
				return true
			}
		}
		return false
	}
	if !has("Foo") {
		t.Errorf("want class name Foo in exports, got %v", got)
	}
	if has("abstract") {
		t.Errorf("modifier 'abstract' must not be captured as an export: %v", got)
	}
}

func equalStringSlices(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
