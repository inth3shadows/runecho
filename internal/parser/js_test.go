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
	result, err := parser.Parse(source)
	if err != nil {
		t.Fatalf("Parse failed: %v", err)
	}

	// Should extract function (may not extract interface/type - that's OK for v1)
	if len(result.Functions) == 0 {
		t.Errorf("Expected to find processUser function")
	}

	if len(result.Classes) == 0 {
		t.Errorf("Expected to find Service class")
	}
}

func TestJSParser_SupportsExtension(t *testing.T) {
	parser := NewJSParser()

	tests := []struct {
		ext      string
		expected bool
	}{
		{".js", true},
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

// Helper functions

func equalFileStructure(a, b FileStructure) bool {
	return equalStringSlices(a.Imports, b.Imports) &&
		equalStringSlices(a.Functions, b.Functions) &&
		equalStringSlices(a.Classes, b.Classes) &&
		equalStringSlices(a.Exports, b.Exports)
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
