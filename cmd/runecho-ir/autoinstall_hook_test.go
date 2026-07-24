package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// githooks/post-merge keeps the INSTALLED binaries in step with the source, so
// the guard cannot silently run a build six releases old while reporting quality
// numbers (which is exactly how a fixed check kept showing a 91% false-positive
// rate). It is a shell script installed by `install.sh --hook-auto-install` as
// both post-merge and post-checkout, so it is tested the way install_test.go
// tests shellQuote: by running real bash against a real git repo.

// hookFixture builds a throwaway git repo carrying a tag, a fake install.sh that
// records that it ran, and a fake runecho-guard reporting installedVer. It
// returns the repo dir and the marker path the fake installer touches.
func hookFixture(t *testing.T, tag, installedVer string) (repo, marker string) {
	t.Helper()
	if _, err := exec.LookPath("bash"); err != nil {
		t.Skip("bash not available")
	}
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	repo = t.TempDir()
	marker = filepath.Join(repo, "installed.marker")

	run := func(args ...string) {
		t.Helper()
		cmd := exec.Command(args[0], args[1:]...)
		cmd.Dir = repo
		// A tagged commit needs an identity; the ambient one may be unset in CI.
		cmd.Env = append(os.Environ(),
			"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t",
			"GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("%v: %v\n%s", args, err, out)
		}
	}

	// The fake installer must be executable-by-bash and observable: touching a
	// marker is what distinguishes "reinstalled" from "no-op" in every case below.
	write(t, filepath.Join(repo, "install.sh"), "#!/usr/bin/env bash\ntouch '"+marker+"'\n")
	write(t, filepath.Join(repo, "README.md"), "fixture\n")
	// The hook identifies the runecho tree by module path before it will run that
	// tree's install.sh, so the fixture must look like one. TestAutoInstall
	// Hook_RefusesForeignRepo overwrites this to prove the check bites.
	write(t, filepath.Join(repo, "go.mod"), "module github.com/inth3shadows/runecho\n\ngo 1.25\n")

	run("git", "init", "-q")
	run("git", "add", "-A")
	run("git", "commit", "-qm", "fixture")
	run("git", "tag", tag)

	bin := filepath.Join(repo, "bin")
	if err := os.MkdirAll(bin, 0o755); err != nil {
		t.Fatal(err)
	}
	if installedVer != "" {
		guard := filepath.Join(bin, "runecho-guard")
		write(t, guard, "#!/usr/bin/env bash\necho "+installedVer+"\n")
		if err := os.Chmod(guard, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	return repo, marker
}

func write(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o755); err != nil {
		t.Fatal(err)
	}
}

// runHookScript executes the tracked hook in repo with the given hook arguments
// and environment additions, returning its combined output. A non-zero exit is a
// test failure in itself: the hook must never break the git operation it runs on.
func runHookScript(t *testing.T, repo string, env []string, args ...string) string {
	t.Helper()
	script, err := filepath.Abs(filepath.Join("..", "..", "githooks", "post-merge"))
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("bash", append([]string{script}, args...)...)
	cmd.Dir = repo
	// RUNECHO_NO_AUTO_INSTALL is the documented opt-out, so a maintainer may well
	// have it exported in their shell profile. Inheriting it would make every
	// positive case below pass vacuously — the hook would exit before doing
	// anything. Clear it in the base env; a case that wants it passes it in env.
	cmd.Env = append(append(os.Environ(),
		"RUNECHO_BIN_DIR="+filepath.Join(repo, "bin"),
		"RUNECHO_NO_AUTO_INSTALL="), env...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("hook exited non-zero (%v) — it must never fail the git operation:\n%s", err, out)
	}
	return string(out)
}

func didInstall(t *testing.T, marker string) bool {
	t.Helper()
	_, err := os.Stat(marker)
	return err == nil
}

// The whole point: a binary older than the source's newest tag gets rebuilt.
func TestAutoInstallHook_ReinstallsWhenBehind(t *testing.T) {
	repo, marker := hookFixture(t, "v0.17.0", "v0.16.1")
	out := runHookScript(t, repo, nil)
	if !didInstall(t, marker) {
		t.Fatalf("installed v0.16.1 vs tag v0.17.0: expected a reinstall, got:\n%s", out)
	}
	if !strings.Contains(out, "v0.16.1") || !strings.Contains(out, "v0.17.0") {
		t.Errorf("output should name both versions so the reason is visible:\n%s", out)
	}
}

// The common case by far. Every merge and checkout runs this, so an up-to-date
// install must cost nothing but the version comparison.
func TestAutoInstallHook_NoopWhenCurrent(t *testing.T) {
	repo, marker := hookFixture(t, "v0.16.1", "v0.16.1")
	if out := runHookScript(t, repo, nil); didInstall(t, marker) {
		t.Fatalf("versions match: expected no reinstall, got:\n%s", out)
	}
}

// Checking out an older branch must not replace a newer installed binary with an
// older one — the hook is a freshness device, not a version pinner.
func TestAutoInstallHook_NeverDowngrades(t *testing.T) {
	repo, marker := hookFixture(t, "v0.15.0", "v0.16.1")
	if out := runHookScript(t, repo, nil); didInstall(t, marker) {
		t.Fatalf("installed v0.16.1 vs older tag v0.15.0: expected no reinstall, got:\n%s", out)
	}
}

// A binary built off a commit after the tag reports v0.16.1-3-gabc1234. That is
// ahead of the tag, not behind it, and `sort -V` orders such suffixes
// inconsistently — so the hook compares only the vX.Y.Z core.
func TestAutoInstallHook_DescribeSuffixIsNotBehind(t *testing.T) {
	repo, marker := hookFixture(t, "v0.16.1", "v0.16.1-3-gabc1234")
	if out := runHookScript(t, repo, nil); didInstall(t, marker) {
		t.Fatalf("a post-tag build must not read as behind its own tag, got:\n%s", out)
	}
}

// post-checkout passes (prev, new, branchFlag). branchFlag=0 is a file checkout,
// which cannot change the source version, so it must short-circuit. branchFlag=1
// is a real branch switch and must behave like post-merge.
func TestAutoInstallHook_PostCheckoutFlag(t *testing.T) {
	t.Run("file checkout skipped", func(t *testing.T) {
		repo, marker := hookFixture(t, "v0.17.0", "v0.16.1")
		if out := runHookScript(t, repo, nil, "aaa", "bbb", "0"); didInstall(t, marker) {
			t.Fatalf("a file checkout must not trigger a rebuild, got:\n%s", out)
		}
	})
	t.Run("branch switch honoured", func(t *testing.T) {
		repo, marker := hookFixture(t, "v0.17.0", "v0.16.1")
		if out := runHookScript(t, repo, nil, "aaa", "bbb", "1"); !didInstall(t, marker) {
			t.Fatalf("a branch switch must rebuild when behind, got:\n%s", out)
		}
	})
}

func TestAutoInstallHook_OptOut(t *testing.T) {
	repo, marker := hookFixture(t, "v0.17.0", "v0.16.1")
	out := runHookScript(t, repo, []string{"RUNECHO_NO_AUTO_INSTALL=1"})
	if didInstall(t, marker) {
		t.Fatalf("RUNECHO_NO_AUTO_INSTALL=1 must suppress the rebuild, got:\n%s", out)
	}
}

// Someone who merely cloned the repo has no runecho on PATH. Installing into
// their $BIN_DIR uninvited would be a surprise, not a service.
func TestAutoInstallHook_DoesNotInstallUninvited(t *testing.T) {
	repo, marker := hookFixture(t, "v0.17.0", "") // no fake guard binary
	if out := runHookScript(t, repo, nil); didInstall(t, marker) {
		t.Fatalf("no existing install: hook must stay out of the way, got:\n%s", out)
	}
}

// A broken build must surface loudly but must NOT fail the merge/checkout —
// runHookScript already fails the test on a non-zero exit.
func TestAutoInstallHook_InstallFailureIsNotFatal(t *testing.T) {
	repo, _ := hookFixture(t, "v0.17.0", "v0.16.1")
	write(t, filepath.Join(repo, "install.sh"), "#!/usr/bin/env bash\nexit 1\n")
	if out := runHookScript(t, repo, nil); !strings.Contains(out, "FAILED") {
		t.Errorf("a failed reinstall must say so:\n%s", out)
	}
}

// install.sh must actually ship the hook it documents; a flag that silently
// installs nothing is the failure mode grammar_subset_test.go exists to prevent.
func TestInstallScript_WiresAutoInstallHook(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("..", "..", "install.sh"))
	if err != nil {
		t.Fatalf("cannot read install.sh: %v", err)
	}
	for _, want := range []string{"--hook-auto-install", "githooks/post-merge", "post-checkout"} {
		if !strings.Contains(string(raw), want) {
			t.Errorf("install.sh does not mention %q", want)
		}
	}
}

// A foreign hook at EITHER name must abort the whole install, not just the one
// it collided with. The first version of this installer validated and copied in
// one pass, so a foreign post-checkout left post-merge installed behind an error
// that reads as "nothing happened" — half a hook, silently live.
func TestInstallScript_CollisionInstallsNothing(t *testing.T) {
	if _, err := exec.LookPath("bash"); err != nil {
		t.Skip("bash not available")
	}
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	installer, err := filepath.Abs(filepath.Join("..", "..", "install.sh"))
	if err != nil {
		t.Fatal(err)
	}

	// Collide on each name in turn: the abort must be total either way, so the
	// second case also proves the fix is not just "check post-checkout first".
	for _, foreign := range []string{"post-checkout", "post-merge"} {
		t.Run("foreign "+foreign, func(t *testing.T) {
			repo := t.TempDir()
			gitRun := func(args ...string) {
				t.Helper()
				cmd := exec.Command("git", args...)
				cmd.Dir = repo
				cmd.Env = append(os.Environ(),
					"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t",
					"GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t")
				if out, err := cmd.CombinedOutput(); err != nil {
					t.Fatalf("git %v: %v\n%s", args, err, out)
				}
			}
			gitRun("init", "-q")
			gitRun("commit", "-q", "--allow-empty", "-m", "x")

			hooks := filepath.Join(repo, ".git", "hooks")
			if err := os.MkdirAll(hooks, 0o755); err != nil {
				t.Fatal(err)
			}
			write(t, filepath.Join(hooks, foreign), "#!/bin/sh\necho someone-elses-hook\n")

			// --no-build: this test only exercises the hook-collision check, and
			// without it install.sh compiles all three binaries first — 2.5s per
			// invocation, and a Go toolchain requirement for a filesystem test.
			cmd := exec.Command("bash", installer, "--hook-auto-install", "--no-build")
			cmd.Dir = repo
			// install.sh always builds the binaries before touching hooks, and it
			// defaults to $HOME/.local/bin. Without this the test would overwrite
			// the developer's real, released install with an unreleased build off
			// the test branch — and that build would then stamp its version into
			// every decision-log record it wrote. Redirect it into the temp repo.
			cmd.Env = append(os.Environ(), "RUNECHO_BIN_DIR="+filepath.Join(repo, "bin"))
			out, err := cmd.CombinedOutput()
			if err == nil {
				t.Fatalf("expected a non-zero exit on collision, got success:\n%s", out)
			}

			for _, name := range []string{"post-merge", "post-checkout"} {
				body, readErr := os.ReadFile(filepath.Join(hooks, name))
				if readErr != nil {
					continue // absent is fine — nothing was written
				}
				if strings.Contains(string(body), "RUNECHO_NO_AUTO_INSTALL") {
					t.Errorf("collision on %s still installed %s — partial install:\n%s",
						foreign, name, out)
				}
			}
		})
	}
}

// The hook installs into whatever repo `install.sh` was invoked from — the README
// tells users to run --hook and --hook-pre-push from the TARGET repo's root — so
// it can land in a tree that is not runecho and whose install.sh belongs to some
// other project. Running that on every merge would be both wrong and unbounded:
// a foreign installer never moves runecho's version, so the behind-check stays
// true and it re-executes forever. The module path is the identity check.
func TestAutoInstallHook_RefusesForeignRepo(t *testing.T) {
	repo, marker := hookFixture(t, "v0.17.0", "v0.16.1")
	// Same shape as a real runecho tree except the module path.
	write(t, filepath.Join(repo, "go.mod"), "module example.com/not-runecho\n\ngo 1.25\n")
	if out := runHookScript(t, repo, nil); didInstall(t, marker) {
		t.Fatalf("hook ran a foreign repo's install.sh — unbounded and wrong:\n%s", out)
	}
}

// And it must still fire in a tree that IS runecho.
func TestAutoInstallHook_RunsInRunechoTree(t *testing.T) {
	repo, marker := hookFixture(t, "v0.17.0", "v0.16.1")
	write(t, filepath.Join(repo, "go.mod"), "module github.com/inth3shadows/runecho\n\ngo 1.25\n")
	if out := runHookScript(t, repo, nil); !didInstall(t, marker) {
		t.Fatalf("the real module path must still be accepted:\n%s", out)
	}
}

// A build can exit 0 without moving the version (tags not fetched, a stamping
// change, the wrong tree). Announcing "now <version>" from the exit status alone
// would report a still-stale binary as a success and silently re-fire on every
// future checkout — a fossil, which is the outcome this hook exists to prevent.
func TestAutoInstallHook_WarnsWhenVersionDidNotAdvance(t *testing.T) {
	repo, _ := hookFixture(t, "v0.17.0", "v0.16.1")
	// Installer succeeds but leaves the fake guard reporting the old version.
	write(t, filepath.Join(repo, "install.sh"), "#!/usr/bin/env bash\nexit 0\n")
	out := runHookScript(t, repo, nil)
	if !strings.Contains(out, "still says") {
		t.Errorf("a no-op reinstall must not be reported as success:\n%s", out)
	}
}

// Windows installs carry a .exe suffix (install.sh appends one on msys/cygwin/
// WINDIR). Probing only the bare name would make the hook a permanent silent
// no-op on a platform install.sh explicitly supports.
func TestAutoInstallHook_FindsExeSuffixedBinary(t *testing.T) {
	repo, marker := hookFixture(t, "v0.17.0", "") // no bare-named guard
	exe := filepath.Join(repo, "bin", "runecho-guard.exe")
	write(t, exe, "#!/usr/bin/env bash\necho v0.16.1\n")
	if err := os.Chmod(exe, 0o755); err != nil {
		t.Fatal(err)
	}
	if out := runHookScript(t, repo, nil); !didInstall(t, marker) {
		t.Fatalf("a .exe-suffixed install must be found:\n%s", out)
	}
}

// install.sh bakes its $BIN_DIR into the copied hook, so an install made with a
// custom RUNECHO_BIN_DIR keeps working in a shell that never exported it. Without
// this the hook looks in ~/.local/bin forever and silently does nothing.
func TestAutoInstallHook_BakedBinDirSurvivesUnsetEnv(t *testing.T) {
	repo, marker := hookFixture(t, "v0.17.0", "v0.16.1")
	installed := filepath.Join(repo, "hook-copy")
	raw, err := os.ReadFile(filepath.Join("..", "..", "githooks", "post-merge"))
	if err != nil {
		t.Fatal(err)
	}
	// Mirror the substitution install.sh performs.
	baked := strings.Replace(string(raw), "BIN_DIR_DEFAULT=''",
		"BIN_DIR_DEFAULT='"+filepath.Join(repo, "bin")+"'", 1)
	if baked == string(raw) {
		t.Fatal("githooks/post-merge no longer has the BIN_DIR_DEFAULT line install.sh rewrites")
	}
	write(t, installed, baked)

	cmd := exec.Command("bash", installed)
	cmd.Dir = repo
	// Deliberately NO RUNECHO_BIN_DIR: the baked value is the only way to find it.
	cmd.Env = append(os.Environ(), "RUNECHO_BIN_DIR=", "RUNECHO_NO_AUTO_INSTALL=")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("hook exited non-zero: %v\n%s", err, out)
	}
	if !didInstall(t, marker) {
		t.Fatalf("baked BIN_DIR was not honoured with RUNECHO_BIN_DIR unset:\n%s", out)
	}
}
