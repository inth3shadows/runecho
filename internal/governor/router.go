package governor

import (
	"regexp"
	"strings"
)

var (
	reOpus = regexp.MustCompile(
		`(?i)(architect|design.*system|review.*(security|code|pr|approach|plan|direction|design|strategy)|trade.?off|compare.*approach|strategy|evaluate.*option|assess.*risk|critique|redesign|migrate|overhaul|debug.*complex|root.cause|right direction|right approach|right track|work together|do these.*work|make sure.*align|they.*align|are.*aligned|is this.*right|feasib|how much work|realisti|really want|actually want|market.*want|market.*need|would.*market)`)

	rePipeline = regexp.MustCompile(
		`(?i)(implement.*feature|build.*new|create.*system|add.*feature|full.*implementation|end.to.end|start.to.finish|from scratch|scaffold|implement the plan|execute the plan|build this out)`)

	reHaiku = regexp.MustCompile(
		`(?i)( summariz| summary | tl;?dr | recap | search | find | explore | grep | look for | check if | where is | what files | show me | scan | browse | format | boilerplate | template | document | explain .* code| what does .* do| how does .* work| describe | generate .* docs| write .*(readme|comment|doc)| add .* comment| rename | move .* file| diff | compare .* file| git status| git log | git history | write.*handoff| create.*handoff| session handoff| write.*\.ai/handoff)`)
)

// RegexRoute classifies prompt intent using regex patterns.
// Order: opus → pipeline → haiku → sonnet (default).
// Mirrors the fallback logic in session-governor.sh.
func RegexRoute(prompt string) Route {
	lower := " " + strings.ToLower(prompt) + " "
	switch {
	case reOpus.MatchString(lower):
		return RouteOpus
	case rePipeline.MatchString(lower):
		return RoutePipeline
	case reHaiku.MatchString(lower):
		return RouteHaiku
	default:
		return RouteSonnet
	}
}
