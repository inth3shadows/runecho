// Package guardstats aggregates cmd/runecho-guard's decisions.jsonl into a
// summary report: ask/defer counts by repo and language, the most-frequently
// flagged symbols, and defer-reason frequency, over a trailing time window.
//
// The guard's decisionRecord (cmd/runecho-guard/declog.go) is unexported and
// lives in a separate main package, so it can't be imported directly. Rather
// than promoting it to a shared internal package for one read-side consumer,
// this package decodes JSONL lines into its own local type with matching
// json tags — the same ad-hoc-decode approach the guard's own tests already
// use (hookmode_test.go's readLastDecisionLog, e6_test.go's e6CountTraces).
package guardstats

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"sort"
	"strings"
	"time"
)

// Decision is one JSONL record read back from decisions.jsonl, with TS
// parsed into a time.Time for filtering/sorting.
type Decision struct {
	TS       time.Time
	GV       string // guard binary version that wrote the record; "" for pre-#207 records
	Mode     string
	Repo     string
	File     string
	Lang     string
	Decision string
	Reason   string
	Symbols  []string
}

// rawDecision mirrors cmd/runecho-guard's decisionRecord by JSON tag (not by
// import — see package doc). Keep in sync with declog.go's decisionRecord.
type rawDecision struct {
	V        int      `json:"v"`
	GV       string   `json:"gv,omitempty"`
	TS       string   `json:"ts"`
	Mode     string   `json:"mode"`
	Repo     string   `json:"repo,omitempty"`
	File     string   `json:"file,omitempty"`
	Lang     string   `json:"lang,omitempty"`
	Decision string   `json:"decision"`
	Reason   string   `json:"reason"`
	Symbols  []string `json:"symbols,omitempty"`
}

// LoadReader streams JSONL decision records from r. A malformed line, one
// with an unparseable ts, or one longer than bufio.Scanner's fixed token cap
// is skipped, not treated as an error — consistent with the guard's own
// fail-open posture elsewhere in this codebase (e.g. matchesIgnoreGlob's
// bad-pattern handling in internal/guard/validate.go). The decision log is
// observability data; one bad (or oversized — an "ask" can carry an
// arbitrarily large symbols array) line should never block a report over
// the rest of it. bufio.Reader.ReadString is used instead of bufio.Scanner
// because Scanner has no way to recover after a too-long line: once Scan
// returns false, iteration cannot resume, so a single oversized line would
// silently truncate every record after it.
func LoadReader(r io.Reader) ([]Decision, error) {
	var out []Decision
	br := bufio.NewReader(r)
	for {
		line, readErr := br.ReadString('\n')
		line = strings.TrimSuffix(line, "\n")
		if line != "" {
			var raw rawDecision
			if err := json.Unmarshal([]byte(line), &raw); err == nil {
				if ts, err := time.Parse(time.RFC3339, raw.TS); err == nil {
					out = append(out, Decision{
						TS:       ts,
						GV:       raw.GV,
						Mode:     raw.Mode,
						Repo:     raw.Repo,
						File:     raw.File,
						Lang:     raw.Lang,
						Decision: raw.Decision,
						Reason:   raw.Reason,
						Symbols:  raw.Symbols,
					})
				}
			}
		}
		if readErr != nil {
			if readErr == io.EOF {
				return out, nil
			}
			return out, fmt.Errorf("scan decision log: %w", readErr)
		}
	}
}

// Load reads and decodes the decision log at path. A missing file returns an
// error satisfying os.IsNotExist so callers can distinguish "no log yet"
// from a real I/O failure.
func Load(path string) ([]Decision, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	return LoadReader(f)
}

// Counts holds ask/defer tallies for one grouping key (a repo or a language).
type Counts struct {
	Ask   int `json:"ask"`
	Defer int `json:"defer"`
}

// SymbolCount is one entry in the top-flagged-symbols ranking.
type SymbolCount struct {
	Name  string `json:"name"`
	Count int    `json:"count"`
}

// Stats is the aggregated guard-decision report for a time window.
type Stats struct {
	Since        time.Time
	Until        time.Time
	TotalAsk     int
	TotalDefer   int
	ByRepo       map[string]Counts
	ByLang       map[string]Counts
	TopSymbols   []SymbolCount
	DeferReasons map[string]int
}

// Aggregate filters decisions to ask/defer records at or after since, and
// summarizes them: totals, per-repo and per-language ask/defer counts, the
// topN most-frequently-flagged symbols (from ask records' Symbols field,
// sorted by count desc then name asc for determinism), and defer-reason
// frequency (from defer records only — a spike in e.g. "parse-fail" or
// "unknown-lang" is exactly the parser-gap signal issue #87 asks for).
//
// "outcome" and "refresh" (mode "e6") records are excluded — a different
// signal, not what this report covers.
func Aggregate(decisions []Decision, since time.Time, topN int) Stats {
	s := Stats{
		Since:        since,
		Until:        time.Now().UTC(),
		ByRepo:       make(map[string]Counts),
		ByLang:       make(map[string]Counts),
		DeferReasons: make(map[string]int),
	}

	symbolCounts := make(map[string]int)

	for _, d := range decisions {
		if d.Decision != "ask" && d.Decision != "defer" {
			continue
		}
		if d.TS.Before(since) {
			continue
		}

		switch d.Decision {
		case "ask":
			s.TotalAsk++
			if d.Repo != "" {
				c := s.ByRepo[d.Repo]
				c.Ask++
				s.ByRepo[d.Repo] = c
			}
			if d.Lang != "" {
				c := s.ByLang[d.Lang]
				c.Ask++
				s.ByLang[d.Lang] = c
			}
			for _, sym := range d.Symbols {
				symbolCounts[sym]++
			}
		case "defer":
			s.TotalDefer++
			if d.Repo != "" {
				c := s.ByRepo[d.Repo]
				c.Defer++
				s.ByRepo[d.Repo] = c
			}
			if d.Lang != "" {
				c := s.ByLang[d.Lang]
				c.Defer++
				s.ByLang[d.Lang] = c
			}
			if d.Reason != "" {
				s.DeferReasons[d.Reason]++
			}
		}
	}

	symbols := make([]SymbolCount, 0, len(symbolCounts))
	for name, count := range symbolCounts {
		symbols = append(symbols, SymbolCount{Name: name, Count: count})
	}
	sort.Slice(symbols, func(i, j int) bool {
		if symbols[i].Count != symbols[j].Count {
			return symbols[i].Count > symbols[j].Count
		}
		return symbols[i].Name < symbols[j].Name
	})
	if topN >= 0 && len(symbols) > topN {
		symbols = symbols[:topN]
	}
	s.TopSymbols = symbols

	return s
}

// sortedCountsKeys returns the keys of a map[string]Counts (repo or language
// names — this helper is grouping-agnostic) sorted for deterministic output:
// by total (ask+defer) desc, then name asc.
func sortedCountsKeys(m map[string]Counts) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool {
		ti := m[keys[i]].Ask + m[keys[i]].Defer
		tj := m[keys[j]].Ask + m[keys[j]].Defer
		if ti != tj {
			return ti > tj
		}
		return keys[i] < keys[j]
	})
	return keys
}

// sortedReasonKeys returns defer-reason keys sorted by count desc, then name asc.
func sortedReasonKeys(m map[string]int) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool {
		if m[keys[i]] != m[keys[j]] {
			return m[keys[i]] > m[keys[j]]
		}
		return keys[i] < keys[j]
	})
	return keys
}

// Format renders a human-readable text report, mirroring
// snapshot.FormatChurn's section-header/table style.
func Format(s Stats) string {
	since := s.Since.Format("2006-01-02")
	until := s.Until.Format("2006-01-02")
	var sb strings.Builder
	fmt.Fprintf(&sb, "GUARD STATS [%s → %s]\n", since, until)
	fmt.Fprintf(&sb, "\nTotal: %d ask, %d defer\n", s.TotalAsk, s.TotalDefer)

	if len(s.ByRepo) > 0 {
		sb.WriteString("\nBy repo:\n")
		for _, repo := range sortedCountsKeys(s.ByRepo) {
			c := s.ByRepo[repo]
			fmt.Fprintf(&sb, "  %-30s ask=%-4d defer=%-4d\n", repo, c.Ask, c.Defer)
		}
	}

	if len(s.ByLang) > 0 {
		sb.WriteString("\nBy language:\n")
		for _, lang := range sortedCountsKeys(s.ByLang) {
			c := s.ByLang[lang]
			fmt.Fprintf(&sb, "  %-30s ask=%-4d defer=%-4d\n", lang, c.Ask, c.Defer)
		}
	}

	if len(s.TopSymbols) > 0 {
		sb.WriteString("\nTop flagged symbols:\n")
		for _, sc := range s.TopSymbols {
			fmt.Fprintf(&sb, "  %-40s %d\n", sc.Name, sc.Count)
		}
	}

	if len(s.DeferReasons) > 0 {
		sb.WriteString("\nDefer reasons:\n")
		for _, reason := range sortedReasonKeys(s.DeferReasons) {
			fmt.Fprintf(&sb, "  %-40s %d\n", reason, s.DeferReasons[reason])
		}
	}

	return sb.String()
}

// Payload converts Stats into the canonical JSON-friendly map for
// `runecho-ir guard-stats --json`, mirroring snapshot.ChurnPayload's
// convention: snake_case top-level keys, arrays never nil.
func Payload(s Stats) map[string]any {
	byRepo := make([]map[string]any, 0, len(s.ByRepo))
	for _, repo := range sortedCountsKeys(s.ByRepo) {
		c := s.ByRepo[repo]
		byRepo = append(byRepo, map[string]any{
			"repo":  repo,
			"ask":   c.Ask,
			"defer": c.Defer,
		})
	}

	byLang := make([]map[string]any, 0, len(s.ByLang))
	for _, lang := range sortedCountsKeys(s.ByLang) {
		c := s.ByLang[lang]
		byLang = append(byLang, map[string]any{
			"lang":  lang,
			"ask":   c.Ask,
			"defer": c.Defer,
		})
	}

	topSymbols := s.TopSymbols
	if topSymbols == nil {
		topSymbols = []SymbolCount{}
	}

	deferReasons := make([]map[string]any, 0, len(s.DeferReasons))
	for _, reason := range sortedReasonKeys(s.DeferReasons) {
		deferReasons = append(deferReasons, map[string]any{
			"reason": reason,
			"count":  s.DeferReasons[reason],
		})
	}

	return map[string]any{
		"since":         s.Since,
		"until":         s.Until,
		"total_ask":     s.TotalAsk,
		"total_defer":   s.TotalDefer,
		"by_repo":       byRepo,
		"by_lang":       byLang,
		"top_symbols":   topSymbols,
		"defer_reasons": deferReasons,
	}
}
