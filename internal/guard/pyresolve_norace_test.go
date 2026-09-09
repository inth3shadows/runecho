//go:build !race

package guard_test

// pyRaceBuild reports whether this binary was built with the race detector.
// See pyresolve_race_test.go for why the #313 differential scales itself down
// when it is.
const pyRaceBuild = false
