package guard

// Test-only exports for the external guard_test package (the compiler-oracle
// differential). Declared in an _test.go file so nothing here ships in the
// binary or widens the package's real API.

// StripLiteralsForTest exposes the literal-masking scan so the differential can
// count braces without a brace inside a string literal splitting a function in
// the wrong place.
var StripLiteralsForTest = stripLiteralsStateful
