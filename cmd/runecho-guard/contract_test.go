package main

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/inth3shadows/runecho/internal/contract"
	"github.com/inth3shadows/runecho/internal/snapshot"
)

// contractRepo stands up a git repo enrolled in a temp store, writes a contract
// file with the given body, and activates it for sessionID. It returns the
// worktree root. Passing an empty body writes no contract file and activates
// nothing — the "no active contract" baseline.
func contractRepo(t *testing.T, sessionID, body string) string {
	t.Helper()
	root := t.TempDir()
	gitInit(t, root)
	// Resolve symlinks (macOS /var -> /private/var): ResolveRepo keys on the
	// git common dir, and an unresolved path would enrol one spelling and look
	// up another.
	if r, err := filepath.EvalSymlinks(root); err == nil {
		root = r
	}
	top := enrolledStore(t, root, []string{"KnownFunc"})

	if body == "" {
		return top
	}
	cdir := filepath.Join(top, contract.Dir)
	if err := os.MkdirAll(cdir, 0o755); err != nil {
		t.Fatal(err)
	}
	cpath := filepath.Join(cdir, "scope")
	if err := os.WriteFile(cpath, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	c, err := contract.Load(cpath)
	if err != nil {
		t.Fatal(err)
	}

	db, err := snapshot.Open(filepath.Join(os.Getenv("RUNECHO_HOME"), "history.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	repo, _, ok := db.ResolveRepo(top)
	if !ok {
		t.Fatal("enrolled repo did not resolve")
	}
	if err := db.ActivateContract(repo.ID, sessionID, c.Name, c.Path, c.Hash); err != nil {
		t.Fatal(err)
	}
	return top
}

// contractPayload renders a PreToolUse Write body carrying a session_id, which
// the shared payload() helper does not emit. It also creates the target's parent
// directory: repo resolution runs git in filepath.Dir(file_path), so a path
// under a directory that does not exist yet resolves to no repo and every check
// — not just this one — abstains. Real edits land in directories that exist.
func contractPayload(t *testing.T, sessionID, filePath, content string) string {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(filePath), 0o755); err != nil {
		t.Fatal(err)
	}
	b, err := json.Marshal(map[string]any{
		"session_id": sessionID,
		"tool_name":  "Write",
		"tool_input": map[string]any{"file_path": filePath, "content": content},
	})
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

const inScopeBody = `name: scope
description: only the guard package

cmd/runecho-guard/**
`

// The core contract: an edit inside the declared scope is silent, an edit
// outside it asks, and BOTH are silent with the flag off. This is the D2
// success criterion stated verbatim, and it is one test on purpose — a pass
// that does not also prove the off-state is silent proves nothing about a
// default-off feature.
func TestContract_InScopeSilent_OutOfScopeAsks_OffIsSilent(t *testing.T) {
	const sess = "sess-d2"
	top := contractRepo(t, sess, inScopeBody)
	inScope := filepath.Join(top, "cmd", "runecho-guard", "x.go")
	outOfScope := filepath.Join(top, "internal", "guard", "y.go")
	body := "package main\n\nfunc F() { KnownFunc() }\n"

	t.Run("flag off, out of scope", func(t *testing.T) {
		// Set explicitly rather than assuming the ambient environment. A machine
		// that dogfoods this feature exports RUNECHO_GUARD_CONTRACT=1 globally, and
		// a test that reads "off" from the outside world would silently stop
		// covering the default path on exactly the machine that runs it most.
		t.Setenv("RUNECHO_GUARD_CONTRACT", "")
		_, raw, d := runHook(t, contractPayload(t, sess, outOfScope, body))
		if d.Hook.PermissionDec != "" {
			t.Fatalf("default-off must not ask; got %q\n%s", d.Hook.PermissionDec, raw)
		}
	})

	t.Setenv("RUNECHO_GUARD_CONTRACT", "1")

	t.Run("flag on, in scope", func(t *testing.T) {
		_, raw, d := runHook(t, contractPayload(t, sess, inScope, body))
		if d.Hook.PermissionDec != "" {
			t.Fatalf("in-scope edit must be silent; got %q\n%s", d.Hook.PermissionDec, raw)
		}
	})

	t.Run("flag on, out of scope", func(t *testing.T) {
		_, raw, d := runHook(t, contractPayload(t, sess, outOfScope, body))
		if d.Hook.PermissionDec != "ask" {
			t.Fatalf("out-of-scope edit must ask; got %q\n%s", d.Hook.PermissionDec, raw)
		}
		if !strings.Contains(d.Hook.PermissionReason, "internal/guard/y.go") {
			t.Errorf("reason must name the offending path: %q", d.Hook.PermissionReason)
		}
		if !strings.Contains(d.Hook.PermissionReason, `"scope"`) {
			t.Errorf("reason must name the contract: %q", d.Hook.PermissionReason)
		}
		// Success criterion: the log records name + ACTIVATION hash, so the ask
		// can be replayed against the exact text that produced it.
		rec := readLastDecisionLog(t)
		if rec["reason"] != "contract" || rec["contract"] != "scope" {
			t.Errorf("decision log lost the contract attribution: %v", rec)
		}
		if h, _ := rec["contract_hash"].(string); len(h) != 12 {
			t.Errorf("decision log must carry the short activation hash, got %q", h)
		}
	})
}

// With no contract activated for the session the check must be invisible even
// with the flag on — the D-4 total-abstention rule, and the thing that keeps the
// ask rate at exactly zero for everyone who did not opt in.
func TestContract_NoActiveContractAbstains(t *testing.T) {
	t.Setenv("RUNECHO_GUARD_CONTRACT", "1")
	top := contractRepo(t, "sess", "")
	_, raw, d := runHook(t, contractPayload(t, "sess", filepath.Join(top, "anywhere.go"), "package main\n"))
	if d.Hook.PermissionDec != "" {
		t.Fatalf("no active contract must abstain entirely; got %q\n%s", d.Hook.PermissionDec, raw)
	}
}

// A contract activated for session A must say nothing about session B. Without
// this the binding would leak across concurrent sessions in the same repo — the
// claudew worktree flow runs several at once.
func TestContract_OtherSessionUnaffected(t *testing.T) {
	t.Setenv("RUNECHO_GUARD_CONTRACT", "1")
	top := contractRepo(t, "sess-a", inScopeBody)
	out := filepath.Join(top, "internal", "guard", "y.go")

	if _, _, d := runHook(t, contractPayload(t, "sess-b", out, "package main\n")); d.Hook.PermissionDec != "" {
		t.Fatalf("a contract bound to another session must not fire; got %q", d.Hook.PermissionDec)
	}
	if _, _, d := runHook(t, contractPayload(t, "sess-a", out, "package main\n")); d.Hook.PermissionDec != "ask" {
		t.Fatalf("control: the bound session should still ask; got %q", d.Hook.PermissionDec)
	}
}

// The hook's empty-input fast return is keyed on the edit's TEXT, and every
// check but this one is about the text. A contract is about the PATH, so an edit
// carrying no added text is still an in-scope question. All three shapes below
// were silently missed while the check sat behind that gate — and the first one
// only fired at all when RUNECHO_GUARD_DANGLING happened to be set, since that
// is what populates removedText. An unrelated flag deciding whether this check
// runs is the bug this test pins.
func TestContract_TextlessEditsStillAsk(t *testing.T) {
	t.Setenv("RUNECHO_GUARD_CONTRACT", "1")
	// Explicitly OFF: the contract check must not depend on them.
	t.Setenv("RUNECHO_GUARD_DANGLING", "")
	t.Setenv("RUNECHO_GUARD_DROPPED_IMPORT", "")
	top := contractRepo(t, "sess", inScopeBody)

	victim := filepath.Join(top, "internal", "victim.go")
	wipe := contractPayload(t, "sess", victim, "") // also creates the parent dir
	if err := os.WriteFile(victim, []byte("package x\n\nfunc Keep() {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	pureDeletion, err := json.Marshal(map[string]any{
		"session_id": "sess",
		"tool_name":  "Edit",
		"tool_input": map[string]any{"file_path": victim, "old_string": "func Keep() {}", "new_string": ""},
	})
	if err != nil {
		t.Fatal(err)
	}

	for _, c := range []struct{ name, body string }{
		{"Write truncating an existing file", wipe},
		{"Write creating a new out-of-scope file", contractPayload(t, "sess", filepath.Join(top, "internal", "brand_new.go"), "")},
		{"Edit deleting text with an empty new_string", string(pureDeletion)},
	} {
		t.Run(c.name, func(t *testing.T) {
			if _, raw, d := runHook(t, c.body); d.Hook.PermissionDec != "ask" {
				t.Fatalf("out-of-scope %s must ask; got %q\n%s", c.name, d.Hook.PermissionDec, raw)
			}
		})
	}

	// Control: with no contract activated, all three keep the fast return — the
	// escape hatch above must not widen what an opted-OUT session sees.
	t.Setenv("RUNECHO_GUARD_CONTRACT", "")
	if _, _, d := runHook(t, wipe); d.Hook.PermissionDec != "" {
		t.Fatalf("flag off must stay silent; got %q", d.Hook.PermissionDec)
	}
}

// The reason this check runs BEFORE the language gate: scope drift lands in
// docs and config at least as often as in code, and every other guard dimension
// defers on an unknown extension.
func TestContract_FiresOnNonCodeFile(t *testing.T) {
	t.Setenv("RUNECHO_GUARD_CONTRACT", "1")
	top := contractRepo(t, "sess", inScopeBody)
	_, _, d := runHook(t, contractPayload(t, "sess", filepath.Join(top, "docs", "README.md"), "# hi\n"))
	if d.Hook.PermissionDec != "ask" {
		t.Fatalf("an out-of-scope Markdown edit must ask; got %q", d.Hook.PermissionDec)
	}
}

// An empty contract puts nothing in scope per contract.InScope — correct for
// `runecho-ir contract check`, which you run once and read. In the hook the same
// rule would ask on every edit for the rest of the session, which is how a guard
// earns being switched off, so the hook abstains instead.
func TestContract_EmptyContractAbstainsInHook(t *testing.T) {
	t.Setenv("RUNECHO_GUARD_CONTRACT", "1")
	top := contractRepo(t, "sess", "name: empty\n# no patterns at all\n")
	_, raw, d := runHook(t, contractPayload(t, "sess", filepath.Join(top, "anything.go"), "package main\n"))
	if d.Hook.PermissionDec != "" {
		t.Fatalf("an empty contract must not turn the hook into an ask-everything; got %q\n%s", d.Hook.PermissionDec, raw)
	}
}

// A contract deleted or renamed after activation must fail open, not start
// asking about every edit because "nothing matches".
func TestContract_MissingFileFailsOpen(t *testing.T) {
	t.Setenv("RUNECHO_GUARD_CONTRACT", "1")
	top := contractRepo(t, "sess", inScopeBody)
	if err := os.Remove(filepath.Join(top, contract.Dir, "scope")); err != nil {
		t.Fatal(err)
	}
	_, _, d := runHook(t, contractPayload(t, "sess", filepath.Join(top, "internal", "y.go"), "package main\n"))
	if d.Hook.PermissionDec != "" {
		t.Fatalf("a deleted contract must fail open; got %q", d.Hook.PermissionDec)
	}
}

// Editing the contract after activation changes what is being enforced. The ask
// must say so — the stored hash exists precisely so this cannot happen silently.
func TestContract_DriftIsDisclosedInTheAsk(t *testing.T) {
	t.Setenv("RUNECHO_GUARD_CONTRACT", "1")
	top := contractRepo(t, "sess", inScopeBody)
	if err := os.WriteFile(filepath.Join(top, contract.Dir, "scope"),
		[]byte(inScopeBody+"\n# widened after activation\ndocs/**\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	_, _, d := runHook(t, contractPayload(t, "sess", filepath.Join(top, "internal", "y.go"), "package main\n"))
	if d.Hook.PermissionDec != "ask" {
		t.Fatalf("still out of scope after the edit, so it must ask; got %q", d.Hook.PermissionDec)
	}
	if !strings.Contains(d.Hook.PermissionReason, "changed after activation") {
		t.Errorf("the ask must disclose that the contract drifted: %q", d.Hook.PermissionReason)
	}
}

// An edit in an unrelated, unenrolled tree must abstain — the binding is looked
// up per repo, so a contract in one repo can never speak about another.
func TestContract_UnrelatedRepoAbstains(t *testing.T) {
	t.Setenv("RUNECHO_GUARD_CONTRACT", "1")
	contractRepo(t, "sess", inScopeBody)
	elsewhere := t.TempDir()
	_, _, d := runHook(t, contractPayload(t, "sess", filepath.Join(elsewhere, "x.go"), "package main\n"))
	if d.Hook.PermissionDec != "" {
		t.Fatalf("a path outside the contract's repo must abstain; got %q", d.Hook.PermissionDec)
	}
}

// The claudew/codexw case, and the one place the abstain is load-bearing rather
// than obvious: ResolveRepo keys on the git COMMON dir, so an edit in a sibling
// linked worktree resolves to the same enrolled repo and finds the same binding
// — but the contract's globs were written against the activating worktree's
// root, and relativizing the edit against it escapes with "../". Abstain: the
// globs say nothing about a path in another tree, and reporting every file there
// as out of scope would be noise of exactly the kind that trains a person to
// switch the guard off.
func TestContract_SiblingWorktreeAbstains(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	t.Setenv("RUNECHO_GUARD_CONTRACT", "1")
	top := contractRepo(t, "sess", inScopeBody)
	for _, args := range [][]string{
		{"git", "-C", top, "config", "user.email", "test@test.com"},
		{"git", "-C", top, "config", "user.name", "Test"},
		{"git", "-C", top, "add", "-A"},
		{"git", "-C", top, "commit", "-m", "init"},
	} {
		if out, err := exec.Command(args[0], args[1:]...).CombinedOutput(); err != nil {
			t.Fatalf("%v: %v: %s", args, err, out)
		}
	}
	sibling := filepath.Join(t.TempDir(), "wt2")
	if out, err := exec.Command("git", "-C", top, "worktree", "add", "--detach", sibling).CombinedOutput(); err != nil {
		t.Skipf("git worktree add: %v: %s", err, out)
	}

	// Control: the same relative path in the ACTIVATING worktree does ask, so
	// this test is proving the worktree boundary and not some unrelated abstain.
	if _, _, d := runHook(t, contractPayload(t, "sess", filepath.Join(top, "internal", "y.go"), "package main\n")); d.Hook.PermissionDec != "ask" {
		t.Fatalf("control: the same path in the activating worktree should ask; got %q", d.Hook.PermissionDec)
	}
	if _, _, d := runHook(t, contractPayload(t, "sess", filepath.Join(sibling, "internal", "y.go"), "package main\n")); d.Hook.PermissionDec != "" {
		t.Fatalf("a sibling worktree is outside the contract's root; must abstain, got %q", d.Hook.PermissionDec)
	}
}

// A contract ask and a hallucination ask are answered in ONE decision, with both
// sections present and a joined reason. The hook emits a single JSON object; a
// second finding must never silently displace the first.
func TestContract_MergesWithSymbolViolationInOneAsk(t *testing.T) {
	t.Setenv("RUNECHO_GUARD_CONTRACT", "1")
	top := contractRepo(t, "sess", inScopeBody)
	body := "package main\n\nfunc F() { TotallyMadeUpSymbol() }\n"
	_, raw, d := runHook(t, contractPayload(t, "sess", filepath.Join(top, "internal", "y.go"), body))
	if d.Hook.PermissionDec != "ask" {
		t.Fatalf("expected an ask; got %q\n%s", d.Hook.PermissionDec, raw)
	}
	if !strings.Contains(d.Hook.PermissionReason, "outside the scope") {
		t.Errorf("contract section missing from the merged ask: %q", d.Hook.PermissionReason)
	}
	if !strings.Contains(d.Hook.PermissionReason, "TotallyMadeUpSymbol") {
		t.Errorf("symbol section missing from the merged ask: %q", d.Hook.PermissionReason)
	}
	rec := readLastDecisionLog(t)
	if rec["reason"] != "contract+violations" {
		t.Errorf("joined reason lost: %v", rec["reason"])
	}
}

// The contract token must LEAD the joined reason, so a dogfood analysis can
// separate the one intent check from the fact checks by prefix.
func TestContractReason(t *testing.T) {
	if got := contractReason(false, "violations"); got != "violations" {
		t.Errorf("no contract => unchanged, got %q", got)
	}
	if got := contractReason(true, "violations+duplicate-symbol"); got != "contract+violations+duplicate-symbol" {
		t.Errorf("contract must lead, got %q", got)
	}
}

func TestContractRepoRoot(t *testing.T) {
	root := filepath.FromSlash("/repo/x")
	in := filepath.Join(root, filepath.FromSlash(contract.Dir), "name")
	if got := contractRepoRoot(in); got != root {
		t.Errorf("contractRepoRoot(%q) = %q, want %q", in, got, root)
	}
	// Not shaped like a contract path — abstain rather than guess a root.
	if got := contractRepoRoot(filepath.FromSlash("/repo/x/somewhere/name")); got != "" {
		t.Errorf("non-contract path should yield \"\", got %q", got)
	}
}

func TestShortHash(t *testing.T) {
	if got := shortHash("0123456789abcdef"); got != "0123456789ab" {
		t.Errorf("got %q", got)
	}
	// A truncated or hand-edited row must not panic on the slice bound.
	if got := shortHash("abc"); got != "abc" {
		t.Errorf("got %q", got)
	}
}
