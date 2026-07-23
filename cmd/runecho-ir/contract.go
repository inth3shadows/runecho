package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/inth3shadows/runecho/internal/contract"
	"github.com/inth3shadows/runecho/internal/gitutil"
	"github.com/inth3shadows/runecho/internal/snapshot"
)

// runContract dispatches the edit-scope contract subcommands (issue #12, D1).
//
// D1 deliberately ships WITHOUT any guard behavior: nothing in the PreToolUse
// path consults a contract yet. It is still useful on its own, because `check`
// answers the same question after the fact — "did this working tree drift
// outside the scope I declared" — which is both a real pre-PR check and the way
// to validate the format and the matcher against real diffs before wiring
// anything into the hook.
func runContract(args []string) int {
	if len(args) == 0 {
		fmt.Fprintln(os.Stderr, "Usage: runecho-ir contract list|show|activate|deactivate|check ...")
		return ExitError
	}
	switch args[0] {
	case "list", "ls":
		return runContractList(args[1:])
	case "show":
		return runContractShow(args[1:])
	case "activate":
		return runContractActivate(args[1:])
	case "deactivate":
		return runContractDeactivate(args[1:])
	case "check":
		return runContractCheck(args[1:])
	default:
		fmt.Fprintf(os.Stderr, "runecho-ir contract: unknown subcommand %q\n", args[0])
		return ExitError
	}
}

// contractsDirFor returns the repo's contract directory and its git top-level.
func contractsDirFor(dir string) (root, contractsDir string, err error) {
	root, err = gitutil.TopLevel(dir)
	if err != nil {
		return "", "", fmt.Errorf("not a git repository: %w", err)
	}
	return root, filepath.Join(root, contract.Dir), nil
}

// listContracts returns the contract files in a repo, sorted by name.
func listContracts(contractsDir string) ([]contract.Contract, error) {
	entries, err := os.ReadDir(contractsDir)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var out []contract.Contract
	for _, e := range entries {
		if e.IsDir() || strings.HasPrefix(e.Name(), ".") {
			continue
		}
		c, err := contract.Load(filepath.Join(contractsDir, e.Name()))
		if err != nil {
			fmt.Fprintf(os.Stderr, "Warning: %v\n", err)
			continue
		}
		out = append(out, c)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

func runContractList(args []string) int {
	fs := flag.NewFlagSet("contract list", flag.ContinueOnError)
	dir := fs.String("dir", ".", "repo directory")
	if err := fs.Parse(args); err != nil {
		return ExitError
	}
	_, cdir, err := contractsDirFor(*dir)
	if err != nil {
		return printErr(err)
	}
	cs, err := listContracts(cdir)
	if err != nil {
		return printErr(err)
	}
	if len(cs) == 0 {
		fmt.Printf("No contracts in %s/\n", contract.Dir)
		return ExitOK
	}
	for _, c := range cs {
		line := c.Name
		if c.Description != "" {
			line += " — " + c.Description
		}
		fmt.Printf("%s  (%d pattern(s))\n", line, len(c.Patterns))
	}
	return ExitOK
}

func runContractShow(args []string) int {
	fs := flag.NewFlagSet("contract show", flag.ContinueOnError)
	dir := fs.String("dir", ".", "repo directory")
	if err := fs.Parse(args); err != nil {
		return ExitError
	}
	if fs.NArg() != 1 {
		fmt.Fprintln(os.Stderr, "Usage: runecho-ir contract show <name>")
		return ExitError
	}
	c, code := loadContractByName(*dir, fs.Arg(0))
	if code != ExitOK {
		return code
	}
	fmt.Printf("name:        %s\n", c.Name)
	if c.Description != "" {
		fmt.Printf("description: %s\n", c.Description)
	}
	fmt.Printf("path:        %s\n", c.Path)
	fmt.Printf("hash:        %s\n", shortHashDisplay(c.Hash))
	fmt.Println("patterns:")
	for _, p := range c.Patterns {
		prefix := "  + "
		if p.Negated {
			prefix = "  - "
		}
		fmt.Printf("%s%s\n", prefix, p.Glob)
	}
	return ExitOK
}

// loadContractByName resolves a contract by its `name:` header, falling back to
// its file name.
func loadContractByName(dir, name string) (contract.Contract, int) {
	_, cdir, err := contractsDirFor(dir)
	if err != nil {
		return contract.Contract{}, printErr(err)
	}
	cs, err := listContracts(cdir)
	if err != nil {
		return contract.Contract{}, printErr(err)
	}
	for _, c := range cs {
		if c.Name == name || filepath.Base(c.Path) == name {
			return c, ExitOK
		}
	}
	fmt.Fprintf(os.Stderr, "no contract named %q in %s/\n", name, contract.Dir)
	return contract.Contract{}, ExitError
}

func runContractActivate(args []string) int {
	fs := flag.NewFlagSet("contract activate", flag.ContinueOnError)
	dir := fs.String("dir", ".", "repo directory")
	session := fs.String("session", "", "session id to bind the contract to (required)")
	if err := fs.Parse(args); err != nil {
		return ExitError
	}
	if fs.NArg() != 1 || *session == "" {
		fmt.Fprintln(os.Stderr, "Usage: runecho-ir contract activate --session <id> <name>")
		return ExitError
	}
	c, code := loadContractByName(*dir, fs.Arg(0))
	if code != ExitOK {
		return code
	}
	db, code := mustOpenDB()
	if code != ExitOK {
		return code
	}
	defer db.Close()
	root, _, err := contractsDirFor(*dir)
	if err != nil {
		return printErr(err)
	}
	repo, code := resolveRepoForWrite(db, root)
	if code != ExitOK {
		return code
	}
	if err := db.ActivateContract(repo.ID, *session, c.Name, c.Path, c.Hash); err != nil {
		return printErr(err)
	}
	fmt.Printf("Activated contract %q for session %s (%d pattern(s), hash %s)\n",
		c.Name, *session, len(c.Patterns), shortHashDisplay(c.Hash))
	return ExitOK
}

func runContractDeactivate(args []string) int {
	fs := flag.NewFlagSet("contract deactivate", flag.ContinueOnError)
	dir := fs.String("dir", ".", "repo directory")
	session := fs.String("session", "", "session id (required)")
	if err := fs.Parse(args); err != nil {
		return ExitError
	}
	if *session == "" {
		fmt.Fprintln(os.Stderr, "Usage: runecho-ir contract deactivate --session <id>")
		return ExitError
	}
	db, code := mustOpenDB()
	if code != ExitOK {
		return code
	}
	defer db.Close()
	root, _, err := contractsDirFor(*dir)
	if err != nil {
		return printErr(err)
	}
	repo, _, ok := db.ResolveRepo(root)
	if !ok {
		fmt.Println("Repo not enrolled; nothing to deactivate.")
		return ExitOK
	}
	if err := db.DeactivateContract(repo.ID, *session); err != nil {
		return printErr(err)
	}
	fmt.Printf("Deactivated contract for session %s\n", *session)
	return ExitOK
}

// runContractCheck reports which changed files fall outside a contract's scope.
//
// This is what makes D1 useful before the guard dimension exists: run it before
// opening a PR and it answers "did this work drift outside what I said I was
// doing". It is also the honest way to validate the matcher — against real
// diffs, not hand-written fixtures.
//
// Exit code is non-zero when something is out of scope, so it composes into a
// pre-push hook or CI step without extra plumbing.
func runContractCheck(args []string) int {
	fs := flag.NewFlagSet("contract check", flag.ContinueOnError)
	dir := fs.String("dir", ".", "repo directory")
	name := fs.String("contract", "", "contract name (default: the session's active contract)")
	session := fs.String("session", "", "session id, to use its active contract")
	base := fs.String("base", "", "compare against this git ref instead of the working tree")
	if err := fs.Parse(args); err != nil {
		return ExitError
	}
	root, _, err := contractsDirFor(*dir)
	if err != nil {
		return printErr(err)
	}

	c, code := resolveCheckContract(root, *dir, *name, *session)
	if code != ExitOK {
		return code
	}

	changed, err := changedPaths(root, *base)
	if err != nil {
		return printErr(err)
	}
	if len(changed) == 0 {
		fmt.Println("No changed files.")
		return ExitOK
	}
	out := c.OutOfScope(changed)
	fmt.Printf("Contract %q — %d changed file(s), %d out of scope\n", c.Name, len(changed), len(out))
	if len(out) == 0 {
		return ExitOK
	}
	for _, p := range out {
		fmt.Printf("  ! %s\n", p)
	}
	// Out-of-scope is a finding, not an error: the edit may well be correct.
	// The non-zero exit exists so this can gate a hook, not to assert a defect.
	return ExitError
}

// resolveCheckContract picks the contract to check against: an explicit --contract
// name, else the session's active binding.
func resolveCheckContract(root, dir, name, session string) (contract.Contract, int) {
	if name != "" {
		return loadContractByName(dir, name)
	}
	if session == "" {
		fmt.Fprintln(os.Stderr, "Specify --contract <name> or --session <id> with an active contract.")
		return contract.Contract{}, ExitError
	}
	db, code := mustOpenDB()
	if code != ExitOK {
		return contract.Contract{}, code
	}
	defer db.Close()
	repo, _, ok := db.ResolveRepo(root)
	if !ok {
		fmt.Fprintln(os.Stderr, "Repo not enrolled; no active contract.")
		return contract.Contract{}, ExitError
	}
	active, err := db.GetActiveContract(repo.ID, session)
	if errors.Is(err, snapshot.ErrNoActiveContract) {
		fmt.Fprintf(os.Stderr, "No active contract for session %s.\n", session)
		return contract.Contract{}, ExitError
	}
	if err != nil {
		return contract.Contract{}, printErr(err)
	}
	c, err := contract.Load(active.Path)
	if err != nil {
		return contract.Contract{}, printErr(err)
	}
	// The activation hash is what makes a finding reproducible. If the file has
	// been edited since, say so rather than silently checking against different
	// text than was agreed.
	if c.Hash != active.ContentHash {
		fmt.Fprintf(os.Stderr,
			"Warning: %s changed since activation (%s → %s); checking against the CURRENT file.\n",
			active.Path, shortHashDisplay(active.ContentHash), shortHashDisplay(c.Hash))
	}
	return c, ExitOK
}

// changedPaths returns repo-relative paths that differ from base (or that are
// modified/untracked in the working tree when base is empty).
func changedPaths(root, base string) ([]string, error) {
	var out []string
	seen := map[string]bool{}
	add := func(raw []byte) {
		for _, line := range strings.Split(string(raw), "\n") {
			p := strings.TrimSpace(line)
			if p == "" || seen[p] {
				continue
			}
			seen[p] = true
			out = append(out, p)
		}
	}
	if base != "" {
		// Three-dot: compare against the MERGE-BASE of base and HEAD, not base's
		// tip. Two-dot reports every file that landed on the base branch since
		// this one was cut as though the current work had touched it — on a
		// branch a day behind master that is pure noise, and it is noise of
		// exactly the kind that trains a person to ignore the tool.
		raw, err := gitOutput(root, "diff", "--name-only", base+"...HEAD")
		if err != nil {
			return nil, err
		}
		add(raw)
		sort.Strings(out)
		return out, nil
	}
	// Working tree: tracked modifications, staged changes, and untracked files.
	for _, argv := range [][]string{
		{"diff", "--name-only"},
		{"diff", "--name-only", "--cached"},
		{"ls-files", "--others", "--exclude-standard"},
	} {
		raw, err := gitOutput(root, argv...)
		if err != nil {
			return nil, err
		}
		add(raw)
	}
	sort.Strings(out)
	return out, nil
}

// shortHashDisplay truncates a hash for display. It does not assume a length:
// the activation hash is read back from the database, and a hand-edited or
// truncated row would otherwise panic on a slice bound in the one command whose
// job is to explain what is happening.
func shortHashDisplay(h string) string {
	if len(h) <= 12 {
		return h
	}
	return h[:12]
}

func gitOutput(dir string, args ...string) ([]byte, error) {
	// gitutil.Command wraps exec.CommandContext, which panics on a nil Context —
	// it must be given a real one, not the zero value.
	cmd := gitutil.Command(context.Background(), dir, args...)
	b, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("git %s: %w", strings.Join(args, " "), err)
	}
	return b, nil
}
