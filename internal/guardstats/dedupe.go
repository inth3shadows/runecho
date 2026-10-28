package guardstats

import (
	"sort"
	"strings"
	"time"
)

// duplicateFireWindow is how close two identical ask records must be to be read
// as one edit seen by several hook wirings rather than two edits. Measured on
// the reference window, every duplicate pair landed within 2s (99 of 103 within
// the same second); the nearest genuine re-ask on an unchanged symbol set was
// 61s away, so the gap this sits in is two orders of magnitude wide.
const duplicateFireWindow = 5 * time.Second

// duplicateAskFires maps the index into ds of each hook-mode ask record that is
// a repeat fire of an earlier record for the same tool call (#252) to the index
// of that call's kept record — its earliest fire. Claude
// Code runs every matching hook wiring, so one edit can log the same ask two
// or three times. FPReport, Audit and Aggregate all skip exactly these, so
// the three reports count asks the same way on the same log (#493).
//
// Two records are the same event when every field that could tell two real
// asks apart matches — file, reason, symbol set (order-insensitive), repo,
// language, guard version and edit fingerprint — and the later lands
// within duplicateFireWindow of the FIRST fire of its cluster. Grouping and
// sorting by time make this independent of log order, so interleaved
// sessions and out-of-order writes from parallel hook processes collapse too.
// Measuring from the cluster's first fire, not the previous record, keeps
// genuine re-asks a few seconds apart from chaining into one event.
//
// Session is deliberately NOT in the key: one wiring's fire can lack the
// session another's carries (TestAskSessions), and splitting them would
// re-inflate the count the collapse exists to correct (#534 review). The cost
// is that two sessions making a byte-identical edit to one file within the
// window count once. Records with no fingerprint (older guards) match on ""
// and collapse as before. `call` (#467) is not consulted yet: on v0.85.2 the
// duplicate fires of one main-thread call shared it, but older records lack it.
//
// Pre-commit asks are never collapsed: they carry no file to group on.
// Outcome records are not either — they measured 1.000 records per event on
// the reference log, and collapsing them would risk cutting the numerator to
// fix the denominator.
func duplicateAskFires(ds []Decision, since time.Time) map[int]int {
	groups := map[string][]int{}
	for i, d := range ds {
		if (d.Decision != "ask" && d.Decision != "deny") || d.Mode != "hook" || d.TS.Before(since) {
			continue
		}
		k := strings.Join([]string{
			symbolKey(d.File, d.Symbols), d.Reason, d.Repo, d.Lang, d.GV, d.Edit, d.Decision,
		}, "\x02")
		groups[k] = append(groups[k], i)
	}
	dup := map[int]int{}
	for _, idx := range groups {
		sort.SliceStable(idx, func(a, b int) bool { return ds[idx[a]].TS.Before(ds[idx[b]].TS) })
		anchor := idx[0]
		for _, i := range idx[1:] {
			if ds[i].TS.Sub(ds[anchor].TS) <= duplicateFireWindow {
				dup[i] = anchor
			} else {
				anchor = i
			}
		}
	}
	return dup
}
