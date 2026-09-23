package main

import (
	"bufio"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/inth3shadows/runecho/internal/store"
)

// Once-per-binding suppression for the contract check (#209).
//
// A session declares `cmd/runecho-guard/**` and then legitimately has to touch
// `internal/guard/lang.go` twenty times. Without this, every one of those edits
// produces an identical ask naming the same path under the same contract — the
// "noise that trains a person to ignore the tool" the contract design names as
// its central risk, from the one check that had no escape valve at all.
//
// The memo records that THIS session already answered THIS question (this file,
// under this activation) and declines to ask it twice. It is written only from
// an OBSERVED approval — a PostToolUse outcome joined to its ask by edit
// fingerprint — never from the ask itself: a denied ask produces no outcome, so
// a denial keeps asking, and a PreToolUse re-invocation inside one tool call
// (#252) cannot suppress the first ask's own re-prompt.
//
// It is NOT a learned-allow analogue, and the difference is the whole argument
// for it existing. Learned-allow changes what the guard BELIEVES: a name enters
// guard.Run's known-set repo-wide, for every session, until decay. The memo
// changes nothing the guard believes — the contract file is untouched, every
// other out-of-scope file still asks, every other session still asks, and a
// re-activation with different text starts clean. A scope that trains itself
// wider is not a scope; a question not asked twice is still the same question.
//
// The key is (session id, short activated hash, cleaned absolute path):
//
//   - the ACTIVATED hash, not the current one, because the activation is the
//     binding the user declared (D-2) and the ask record already carries it, so
//     the PostToolUse side needs no store open. Re-activating the same text keeps
//     the memo (nothing the user judged has changed); re-activating edited text
//     is a new declaration and starts clean. A mid-session edit to the contract
//     without re-activation keeps it too — the check still evaluates the CURRENT
//     file, so a path newly excluded had no memo to begin with and asks.
//   - the raw tool-call path, cleaned on both sides. Pre and Post carry the same
//     tool_input (the #300 fingerprint join already depends on that). A symlinked
//     alias of the same file asks once more, which is the safe direction.
//
// Every failure is fail-SAFE, which here means "ask again": a missing, corrupt,
// oversized or unreadable store suppresses nothing.

const (
	// contractApprovalsFile is the memo store inside RUNECHO_HOME.
	contractApprovalsFile = "contract-approvals.json"
	// maxContractApprovals caps the store; the oldest entries are evicted first.
	// A session key already isolates entries, so this is hygiene, not policy.
	maxContractApprovals = 1024
	// contractApprovalTTL drops entries from long-dead sessions. Filtered on read
	// and pruned on write, like learned-allow.
	contractApprovalTTL = 7 * 24 * time.Hour
	// maxContractApprovalBytes bounds what the PreToolUse read will parse, as
	// maxEnrollNoticeBytes does for the enrollment marker.
	maxContractApprovalBytes = 1 << 20
)

// contractOnceEnabled reports whether the memo is on. Default ON whenever the
// contract check itself is on; RUNECHO_GUARD_CONTRACT_ONCE=0 disables both the
// read and the write. The opt-out is the A/B instrument if the suppressed count
// is ever doubted: raw ask volume with =0 should equal Total + Suppressed with
// it on.
func contractOnceEnabled() bool { return os.Getenv("RUNECHO_GUARD_CONTRACT_ONCE") != "0" }

// contractApproval is one answered question: this session approved an edit to
// File while the contract activated with Hash (short form) was bound.
type contractApproval struct {
	Session string `json:"session"`
	Hash    string `json:"hash"`
	File    string `json:"file"`
	At      string `json:"at"` // RFC3339 UTC, for TTL and eviction order
}

type contractApprovals struct {
	V       int                `json:"v"`
	Entries []contractApproval `json:"entries"`
}

// loadContractApprovals reads the memo store. Missing, oversized or corrupt
// yields an empty store — the next edit asks again, and the next approval
// rewrites the file whole.
func loadContractApprovals(dir string) contractApprovals {
	ca := contractApprovals{V: 1}
	path := filepath.Join(dir, contractApprovalsFile)
	if fi, err := os.Stat(path); err != nil || fi.Size() > maxContractApprovalBytes {
		return ca
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return ca
	}
	var parsed contractApprovals
	if json.Unmarshal(b, &parsed) != nil {
		return ca
	}
	parsed.V = 1
	return parsed
}

func saveContractApprovals(dir string, ca contractApprovals) error {
	ca.V = 1
	b, err := json.Marshal(ca)
	if err != nil {
		return err
	}
	return store.AtomicWriteFile(filepath.Join(dir, contractApprovalsFile), b)
}

// contractApproved reports whether this exact (session, hash, file) question has
// already been answered. READ-ONLY: it never prunes or writes, so the PreToolUse
// hot path stays a single small file read, and it runs only on an edit that
// would otherwise ask.
func contractApproved(dir, session, hash, file string, now time.Time) bool {
	if !contractOnceEnabled() || dir == "" || session == "" || hash == "" || file == "" {
		return false
	}
	cutoff := now.UTC().Add(-contractApprovalTTL)
	for _, e := range loadContractApprovals(dir).Entries {
		if e.Session != session || e.Hash != hash || e.File != file {
			continue
		}
		at, err := time.Parse(time.RFC3339, e.At)
		if err != nil || at.Before(cutoff) {
			continue
		}
		return true
	}
	return false
}

// recordContractApproval folds one observed approval into the store, under the
// same cross-process lock discipline as recordApprovals (two PostToolUse hooks
// can fire for one edit — see recentUnrecordedAsk). Dedupes on the key, prunes
// past-TTL entries, and evicts the oldest past the cap. Best-effort: any error
// leaves the next edit asking, which is the safe direction.
func recordContractApproval(dir, session, hash, file string, now time.Time) {
	if !contractOnceEnabled() || dir == "" || session == "" || hash == "" || file == "" {
		return
	}
	store.WithFileLock(filepath.Join(dir, contractApprovalsFile+".lock"), func() {
		ca := loadContractApprovals(dir)
		nowStr := now.UTC().Format(time.RFC3339)
		cutoff := now.UTC().Add(-contractApprovalTTL)
		kept := ca.Entries[:0]
		for _, e := range ca.Entries {
			if e.Session == session && e.Hash == hash && e.File == file {
				continue // re-added below with a fresh timestamp
			}
			at, err := time.Parse(time.RFC3339, e.At)
			if err != nil || at.Before(cutoff) {
				continue
			}
			kept = append(kept, e)
		}
		kept = append(kept, contractApproval{Session: session, Hash: hash, File: file, At: nowStr})
		if len(kept) > maxContractApprovals {
			// RFC3339 UTC strings sort chronologically. Stable, so equal
			// timestamps keep insertion order and the new entry stays last.
			sort.SliceStable(kept, func(i, j int) bool { return kept[i].At < kept[j].At })
			kept = kept[len(kept)-maxContractApprovals:]
		}
		ca.Entries = kept
		_ = saveContractApprovals(dir, ca)
	})
}

// humanApproval reports whether an outcome recorded under this permission mode
// reflects a person's judgement about the edit. In bypassPermissions and dontAsk
// the harness can resolve an ask without prompting anyone, so an outcome there
// says nothing about whether the path belonged in scope — recording it would
// suppress an ask nobody ever saw. Unknown or empty modes count as human: that
// is today's behaviour everywhere else, and RUNECHO_GUARD_CONTRACT_ONCE=0 is
// the escape if a new mode turns out to auto-resolve too.
func humanApproval(permissionMode string) bool {
	switch permissionMode {
	case "bypassPermissions", "dontAsk":
		return false
	}
	return true
}

// splitContractOnce separates a would-ask contract warning into the one to
// render and the one an earlier approval already answered. Exactly one of the
// two is non-nil when cw is; both are nil when cw is. Keeping the ask side
// nil-normalised is what lets every existing `cw != nil` site stay untouched: a
// suppressed repeat renders no contract section, logs no contract reason, and
// restores learned-allow training for a merged ask's fact half — correct, since
// the approval of THIS edit now answers only the fact question.
//
// The memo read happens here, on an edit that would otherwise ask, and only
// there: an in-scope edit or a session with no contract never reaches it.
func splitContractOnce(cw *contractWarning) (ask, suppressed *contractWarning) {
	if cw == nil {
		return nil, nil
	}
	dir, err := runechoDir()
	if err != nil {
		return cw, nil
	}
	if contractApproved(dir, cw.SessionID, shortHash(cw.ActivatedHash), cw.File, time.Now()) {
		return nil, cw
	}
	return cw, nil
}

// noteContractSuppressed stamps a decision record with the fact that a repeat
// contract ask was silenced for this edit (#209). It rides on whatever record
// the hook was going to write anyway, so the would-have-asked volume the issue
// wants measured stays countable (fpreport ByCheck[contract].Suppressed) without
// a second record per edit or a write on the PreToolUse path.
func noteContractSuppressed(rec decisionRecord, sc *contractWarning) decisionRecord {
	if sc == nil {
		return rec
	}
	rec.Contract, rec.ContractHash = sc.Name, shortHash(sc.ActivatedHash)
	rec.Suppressed = append(rec.Suppressed, "contract")
	return rec
}

// contractAskStillStands reports whether the ask an outcome just joined is still
// the guard's LAST word on this edit — the only case in which the outcome can be
// read as the user approving that ask.
//
// The fingerprint join cannot tell on its own. It pairs an outcome with the most
// recent ask for the same edit, and a denied ask writes nothing — so this
// sequence joins cleanly: contract ask for edit E, user DENIES; the agent retries
// the identical E, and this time the hook defers (the contract was deactivated,
// or the hook timed out); E runs; PostToolUse joins the denied ask. Remembering
// that as an answer would silence the file for the rest of the session on the
// strength of a "no". Learned-allow shares the join, but needs two approvals;
// this memo needs one, so it closes the gap for itself.
//
// So: after the joined ask, any later hook record for the same file that is NOT
// a re-fire of that same ask (#252 re-invokes the hook per permission
// round-trip, logging identical asks) means the edit was answered by something
// else. A timeout defer carries no file at all, so any timeout after the ask
// counts too — over-broad on purpose: a missed memo costs one more ask, a wrong
// memo costs every ask for that file. Reads the same bounded tail as
// recentUnrecordedAsk; anything unreadable, or an ask no longer in the window,
// reads as "does not stand".
func contractAskStillStands(path string, ask decisionRecord, file, editHash string) bool {
	if editHash == "" {
		return false
	}
	f, err := os.Open(path)
	if err != nil {
		return false
	}
	defer f.Close()
	stat, err := f.Stat()
	if err != nil {
		return false
	}
	if offset := stat.Size() - maxOutcomeReadBytes; offset > 0 {
		if _, err := f.Seek(offset, io.SeekStart); err != nil {
			return false
		}
	}
	needleBytes, _ := json.Marshal(file)
	needle := string(needleBytes)

	stands := false
	r := bufio.NewReader(f)
	for {
		line, readErr := r.ReadString('\n')
		if len(line) > 0 && (strings.Contains(line, needle) || strings.Contains(line, `"timeout"`)) {
			var cur decisionRecord
			if json.Unmarshal([]byte(line), &cur) == nil && cur.Decision != "outcome" {
				switch {
				case cur.Decision == "ask" && cur.File == file && cur.Edit == editHash && (stands || cur.TS == ask.TS):
					stands = true // the joined ask, or a #252 re-fire of it
				case cur.File == file && cur.Mode == "hook":
					stands = false // answered by a later record for this file
				case cur.File == "" && cur.Reason == "timeout":
					stands = false
				}
			}
		}
		if readErr != nil {
			break
		}
	}
	return stands
}
