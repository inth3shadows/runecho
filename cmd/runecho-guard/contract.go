package main

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/inth3shadows/runecho/internal/contract"
	"github.com/inth3shadows/runecho/internal/guard"
	"github.com/inth3shadows/runecho/internal/snapshot"
)

// contractEnabled reports whether the edit-scope contract check is on
// (RUNECHO_GUARD_CONTRACT=1). Default OFF, dogfood-first, like every other
// experimental guard surface — but this one is default-off for a stronger reason
// than the rest.
//
// Every other check answers a question about FACT (does this symbol exist in
// this repo), which is computable from the index and where a false positive is
// always a bug. This one answers a question about INTENT (is this edit inside
// what we agreed to change), which the repo cannot supply. An edit outside the
// declared scope is frequently legitimate — a necessary refactor, a missed
// dependency, a rename that reached further than expected — so its ask rate is
// structurally higher than any fact check's, and "false positive" is not even
// the right name for what it produces.
//
// The whole design therefore leans on two firewalls, and both live below rather
// than in this flag: activation is EXPLICIT (never inferred from a branch name,
// a prompt, or a model), and with no active contract the check abstains
// completely. Together they hold the ask rate at exactly zero for anyone who did
// not name a contract, by name, this session.
func contractEnabled() bool { return os.Getenv("RUNECHO_GUARD_CONTRACT") == "1" }

// contractWarning is one out-of-scope edit, ready to render into an ask.
type contractWarning struct {
	Name          string // contract name, as declared or as its file is named
	ContractPath  string // absolute path to the contract file
	RepoRoot      string // repo root the contract's globs are written against
	SessionID     string // session the contract is bound to, so the ask can print a runnable command
	RelPath       string // the edited file, relative to the contract's repo root
	Patterns      int    // pattern count, so the ask can say what it was measured against
	ActivatedHash string // contract hash at activation time (from the store)
	CurrentHash   string // contract hash on disk right now
	// RepoName is the enrolled repo's name, carried so askContractOnly can
	// attribute the decision record even on the paths where the caller has no
	// repo in hand (an unknown file extension, an empty-text edit). Without it
	// those records log an empty repo and guardstats drops them from ByRepo —
	// silently excluding non-code files, which is where the design says scope
	// drift most often lands.
	RepoName string
}

// drifted reports whether the contract file changed after it was activated.
// Storing the activation hash is what makes an ask reproducible; if the text
// moved underneath it, the ask must say so rather than quietly enforce
// something other than what was agreed.
func (cw *contractWarning) drifted() bool { return cw.ActivatedHash != cw.CurrentHash }

// section renders the contract part of the ask. It carries its own guidance
// sentence because the generic one the fact checks share ("new/local/dynamic,
// or an intended removal") is about symbols and says nothing useful here.
//
// Every command it prints is fully qualified and runnable as-is. The first draft
// suggested a bare `runecho-ir contract deactivate`, which ALWAYS fails —
// --session is required and the guard is the only party that knows the id, since
// it read it from the hook payload and never showed it. A remedy the user cannot
// execute is worse than no remedy: it spends the one moment they are paying
// attention on a usage error.
//
// The ignorefile disclaimer is not padding either. In a merged ask this section
// sits beside the fact checks' trailer, which offers .runechoguardignore as the
// way to silence repeats — and that file is read only by guard.Run, never here.
// A user who reaches for it would watch the symbol half go quiet while the
// contract half kept asking, and conclude the guard was broken.
func (cw *contractWarning) section() string {
	var sb strings.Builder
	fmt.Fprintf(&sb, "[runecho-guard] this edit is outside the scope declared by contract %q:\n", cw.Name)
	fmt.Fprintf(&sb, "  %s — matched none of its %d pattern(s)\n", cw.RelPath, cw.Patterns)
	if cw.drifted() {
		fmt.Fprintf(&sb, "  note: %s changed after activation (%s → %s) — this check used the CURRENT file.\n",
			cw.ContractPath, shortHash(cw.ActivatedHash), shortHash(cw.CurrentHash))
	}
	fmt.Fprintf(&sb, "Approve if the edit is legitimate — an out-of-scope edit often is. "+
		"The scope lives in %s; widen it there, or drop it for this session with:\n", cw.ContractPath)
	fmt.Fprintf(&sb, "  runecho-ir contract deactivate --dir %s --session %s\n", cw.RepoRoot, cw.SessionID)
	sb.WriteString("(.runechoguardignore does not apply to a contract — only the contract file controls scope.)\n")
	return sb.String()
}

// contractWarningFor returns the out-of-scope warning for one edit, or nil.
//
// nil means ABSTAIN, and almost every path here returns nil. That is the point:
// the abstain stack below is the feature, not defensive padding. In order —
//
//  1. flag off (one getenv, so the default path costs nothing);
//  2. no session id in the hook payload — a contract is bound per session, and
//     without one there is nothing to look up;
//  3. store missing, unopenable, repo unenrolled — fail-open, same as every
//     other check;
//  4. no active contract for this session — the D-4 rule. Not a warning, not a
//     nudge. A user who did not activate a contract must never see this check;
//  5. the contract file is gone or unreadable — fail-open. A deleted contract
//     must not start asking about every edit;
//  6. the contract has no POSITIVE pattern — empty, or negation-only like
//     `!internal/**` (see Contract.HasPositive). Either way it puts nothing in
//     scope for any path. This DIVERGES from contract.InScope, which reports the
//     unfinished declaration loudly so `runecho-ir contract check` surfaces it.
//     That is right for a command you ran on purpose and read once; in the hook
//     the same rule would ask on every edit for the rest of the session, which
//     is exactly how a guard earns being switched off. The CLI is where an
//     unfinished contract gets surfaced (#234);
//  7. the edited path is not absolute, or sits outside the repo root the
//     contract was authored against (a different worktree, a different repo) —
//     the globs say nothing about it, so neither does this check.
//
// Only after all of those does a path get matched against the globs.
func contractWarningFor(filePath, sessionID string) *contractWarning {
	if !contractEnabled() || sessionID == "" {
		return nil
	}
	storeDir, err := runechoDir()
	if err != nil {
		return nil
	}
	dbPath := filepath.Join(storeDir, "history.db")
	if _, err := os.Stat(dbPath); err != nil {
		return nil
	}
	// OpenFast skips the on-open integrity scan, as everywhere else on this hot
	// path. This standalone open is used ONLY where the symbol pipeline never
	// runs — an unknown file extension — because a store open measured ~9.5 ms
	// on the hook's per-edit budget and paying it twice is the kind of tax that
	// gets a default-off feature switched off instead of dogfooded. Every other
	// edit reaches this check through contractWarningWith on the open
	// lookupSymbolsFor already makes.
	db, err := snapshot.OpenFast(dbPath)
	if err != nil {
		return nil
	}
	defer db.Close()

	repo, _, ok := db.ResolveRepo(filepath.Dir(filePath))
	if !ok {
		return nil
	}
	return contractWarningWith(db, repo.ID, repo.Name, filePath, sessionID)
}

// contractWarningWith is the half of contractWarningFor that needs an open
// store and a resolved repo, so a caller that already has both (the hook's
// lookupSymbolsFor) can answer the contract question without a second open. The
// gates are repeated rather than assumed: this is reachable from a caller that
// opened the store for entirely unrelated reasons, and "the flag is off" must
// still cost nothing there.
func contractWarningWith(db *snapshot.DB, repoID int64, repoName, filePath, sessionID string) *contractWarning {
	if !contractEnabled() || sessionID == "" || !filepath.IsAbs(filePath) {
		return nil
	}
	active, err := db.GetActiveContract(repoID, sessionID)
	if err != nil {
		// snapshot.ErrNoActiveContract is the overwhelmingly common case and is
		// the D-4 rule itself: no contract means total abstention. Every other
		// error is a degraded store, which abstains too. Both are nil, and they
		// are deliberately not distinguished — there is no behaviour to differ.
		return nil
	}
	c, err := contract.Load(active.Path)
	// HasPositive subsumes the empty-contract case: a contract with no positive
	// pattern (empty, or negation-only like `!internal/**`) puts nothing in scope
	// for any path, so InScope would fire on EVERY edit for the whole session.
	// That is the "noise that trains a person to switch the tool off" the abstain
	// exists to prevent, so it abstains the same way an empty contract does (#234).
	if err != nil || !c.HasPositive() {
		return nil
	}
	root := contractRepoRoot(active.Path)
	if root == "" {
		return nil
	}
	rel, ok := relWithinRoot(root, filePath)
	if !ok {
		return nil
	}
	if c.InScope(rel) {
		return nil
	}
	return &contractWarning{
		Name:          c.Name,
		ContractPath:  active.Path,
		RepoRoot:      root,
		SessionID:     sessionID,
		RelPath:       rel,
		Patterns:      len(c.Patterns),
		ActivatedHash: active.ContentHash,
		CurrentHash:   c.Hash,
		RepoName:      repoName,
	}
}

// relWithinRoot returns filePath relative to root in slash form, and whether it
// is actually inside root.
//
// The symlink retry is the point. The stored root came from
// `git rev-parse --show-toplevel`, which is fully resolved; filePath is whatever
// the tool call carried, which may not be. One symlinked component between them
// — /var vs /private/var on macOS, a symlinked home or project directory
// anywhere — makes Rel produce a "../" path, and the check then abstains SILENTLY
// on every edit in the repo, which is indistinguishable from "everything is in
// scope". A test suite cannot catch that by normalising its own fixture, which
// is exactly what the first version of these tests did.
//
// The resolve is deliberately in the retry and not the fast path: it costs a
// syscall per component, and the overwhelmingly common case (no symlinks) never
// reaches it.
func relWithinRoot(root, filePath string) (string, bool) {
	if rel, err := filepath.Rel(root, filepath.Clean(filePath)); err == nil && !escapes(rel) {
		return filepath.ToSlash(rel), true
	}
	// Resolve the DIRECTORY, not the file. EvalSymlinks requires every component
	// to exist, and the edited file frequently does not yet — a Write creating a
	// new file is the whole point of a PreToolUse hook. Resolving the full path
	// fails there and abstains, which is the same silent miss this retry exists
	// to close. The parent directory does exist: repo resolution already ran git
	// inside it to get here.
	dir, err := filepath.EvalSymlinks(filepath.Dir(filePath))
	if err != nil {
		return "", false
	}
	rel, err := filepath.Rel(root, filepath.Join(dir, filepath.Base(filePath)))
	if err != nil || escapes(rel) {
		return "", false
	}
	return filepath.ToSlash(rel), true
}

func escapes(rel string) bool {
	return rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

// contractRepoRoot recovers the repo root from a stored contract path by
// stripping contract.Dir off its directory. Deriving it from the constant means
// it cannot drift if the directory layout ever changes, and it avoids the git
// subprocess a TopLevel lookup would cost on a hook with a ~12 ms budget.
// Returns "" if the path is not shaped like a contract path, which the caller
// treats as abstain.
func contractRepoRoot(contractPath string) string {
	suffix := string(filepath.Separator) + filepath.FromSlash(contract.Dir)
	dir := filepath.Dir(contractPath)
	if !strings.HasSuffix(dir, suffix) {
		return ""
	}
	return strings.TrimSuffix(dir, suffix)
}

// askContractOnly emits the ask for an edit whose ONLY finding is that it fell
// outside the active contract, and reports whether it emitted. It exists because
// the contract check has to be answerable on paths where the symbol pipeline
// never ran or found nothing — an unknown language, a repo with no snapshot, a
// perfectly clean edit to an out-of-scope file. Those paths would otherwise
// defer, and a contract that is silent for every non-code file would miss the
// place scope drift most often lands.
//
// Callers must invoke this INSTEAD OF their defer, not before it: the hook emits
// exactly one decision.
func askContractOnly(out io.Writer, cw *contractWarning, filePath string, lang guard.Lang) bool {
	if cw == nil {
		return false
	}
	hookAsk(out, cw.section())
	logDecision(decisionRecord{
		Mode:         "hook",
		Repo:         cw.RepoName,
		File:         filePath,
		Lang:         string(lang),
		Decision:     "ask",
		Reason:       "contract",
		Contract:     cw.Name,
		ContractHash: shortHash(cw.ActivatedHash),
	})
	return true
}

// contractReason prefixes an ask reason with the contract token, so an intent
// ask is always distinguishable from the fact asks it may be bundled with.
//
// Be honest about what this costs rather than what it buys. `reason` has been a
// compound key since askReason started joining with "+", and guardstats/fpreport
// bucket on the EXACT string — so a hallucination ask that previously logged
// "violations" logs "contract+violations" while a contract is active, and lands
// in a different bucket. Any per-check rate therefore has to split on "+" and
// count terms, which is already true of "violations+dangling" and is not a new
// requirement. What the prefix prevents is the worse alternative: an intent ask
// silently counted as a fact false positive, which would corrupt the very
// numbers the un-gating decision rests on. Splitting a bucket is recoverable
// arithmetic; conflating two check classes is not.
func contractReason(fired bool, rest string) string {
	if !fired {
		return rest
	}
	return "contract+" + rest
}

// shortHash truncates a hash for display without assuming a length — the
// activation hash comes back from the database, and a hand-edited or truncated
// row must not panic on a slice bound inside the guard's hot path.
func shortHash(h string) string {
	if len(h) <= 12 {
		return h
	}
	return h[:12]
}
