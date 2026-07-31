package main

import (
	"os"
	"strings"
	"testing"
)

// guardEnvPrefix is the family of feature-gate variables this package reads.
// Every one of them changes which checks run, and therefore which decision the
// hook records.
const guardEnvPrefix = "RUNECHO_GUARD_"

// TestMain strips every RUNECHO_GUARD_* variable from the process environment
// before any test in this package runs, so which CHECKS a test exercises depends
// only on what it sets with t.Setenv. It deliberately covers the feature-gate
// family and nothing else: RUNECHO_HOME and RUNECHO_DEBUG are also read here,
// but every test already routes RUNECHO_HOME through enrolledStore/enrollSnapshot
// and neither changes a decision.
//
// This exists because the dogfooding box exports several of these durably —
// `go test ./...` there was silently exercising a different configuration than
// CI, which exports none (#262). A test that pins a decision under one specific
// flag combination would pass in CI and fail locally, or worse, the reverse.
//
// The prefix scan is deliberate: the earlier defence was a hand-maintained list
// of clears inside the one test that noticed the problem, and it went stale the
// moment #243 added a third feature gate to the condition it was guarding. A
// new RUNECHO_GUARD_* variable is covered here the day it is introduced,
// without anyone remembering to add it.
//
// os.Environ returns a copy, so unsetting while ranging over it is safe.
func TestMain(m *testing.M) {
	for _, kv := range os.Environ() {
		if k, _, ok := strings.Cut(kv, "="); ok && strings.HasPrefix(k, guardEnvPrefix) {
			os.Unsetenv(k)
		}
	}
	os.Exit(m.Run())
}

// TestGuardEnvIsIsolated fails loudly if TestMain above is removed or narrowed
// while a feature gate is exported in the ambient shell. Without it, losing the
// isolation is invisible in CI — where nothing is exported — and shows up only
// as a confusing local failure in an unrelated test.
func TestGuardEnvIsIsolated(t *testing.T) {
	for _, kv := range os.Environ() {
		if k, v, ok := strings.Cut(kv, "="); ok && strings.HasPrefix(k, guardEnvPrefix) {
			t.Errorf("%s=%q leaked into the test process; TestMain must clear the %s* family", k, v, guardEnvPrefix)
		}
	}
}
