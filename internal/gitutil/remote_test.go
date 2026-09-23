package gitutil

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

// remoteRepo makes an upstream with one commit, an annotated tag and a
// lightweight tag, plus a clone of it. Returns (upstream, clone).
func remoteRepo(t *testing.T) (string, string) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	up := t.TempDir()
	run := func(dir string, args ...string) string {
		t.Helper()
		cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
		cmd.Env = append(os.Environ(), "GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t")
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
		return string(out)
	}
	run(up, "-c", "init.defaultBranch=master", "init", "-q")
	os.WriteFile(filepath.Join(up, "install.sh"), []byte("#!/usr/bin/env bash\n"), 0o755)
	os.WriteFile(filepath.Join(up, "README"), []byte("hi\n"), 0o644)
	run(up, "add", ".")
	run(up, "commit", "-qm", "init")
	run(up, "tag", "-a", "v1.0.0", "-m", "v1.0.0")
	run(up, "tag", "v1.0.1")
	clone := filepath.Join(t.TempDir(), "clone")
	run("", "clone", "-q", up, clone)
	return up, clone
}

func ctx10(t *testing.T) context.Context {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	t.Cleanup(cancel)
	return ctx
}

func headOf(t *testing.T, dir string) string {
	out, err := exec.Command("git", "-C", dir, "rev-parse", "HEAD").Output()
	if err != nil {
		t.Fatal(err)
	}
	return string(out[:len(out)-1])
}

func TestRemoteTags_PeelsAnnotatedAndKeepsLightweight(t *testing.T) {
	up, clone := remoteRepo(t)
	tags, err := RemoteTags(ctx10(t), clone, "origin")
	if err != nil {
		t.Fatal(err)
	}
	head := headOf(t, up)
	if tags["v1.0.0"] != head {
		t.Errorf("annotated tag must map to the COMMIT (peeled), got %q want %q", tags["v1.0.0"], head)
	}
	if tags["v1.0.1"] != head {
		t.Errorf("lightweight tag = %q, want %q", tags["v1.0.1"], head)
	}
	if _, ok := tags["v1.0.0^{}"]; ok {
		t.Error("the ^{} peel line leaked through as its own tag")
	}
}

func TestRemoteTags_ErrorsOnUnreachableRemote(t *testing.T) {
	_, clone := remoteRepo(t)
	exec.Command("git", "-C", clone, "remote", "set-url", "origin", filepath.Join(t.TempDir(), "nope")).Run()
	if _, err := RemoteTags(ctx10(t), clone, "origin"); err == nil {
		t.Error("an unreachable remote must error, not return an empty list")
	}
}

func TestFetch_UpdatesRemoteTrackingRef(t *testing.T) {
	up, clone := remoteRepo(t)
	os.WriteFile(filepath.Join(up, "NEW"), []byte("x\n"), 0o644)
	for _, a := range [][]string{{"add", "NEW"}, {"commit", "-qm", "new"}} {
		cmd := exec.Command("git", append([]string{"-C", up}, a...)...)
		cmd.Env = append(os.Environ(), "GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("%v: %s", err, out)
		}
	}
	if err := Fetch(ctx10(t), clone, "origin"); err != nil {
		t.Fatal(err)
	}
	out, err := exec.Command("git", "-C", clone, "rev-parse", "refs/remotes/origin/master").Output()
	if err != nil || string(out[:len(out)-1]) != headOf(t, up) {
		t.Errorf("origin/master not advanced to upstream HEAD (err=%v)", err)
	}
}

func TestExport_ExtractsTreeAtRevWithModes(t *testing.T) {
	up, clone := remoteRepo(t)
	dest := t.TempDir()
	if err := Export(ctx10(t), clone, headOf(t, up), dest); err != nil {
		t.Fatal(err)
	}
	fi, err := os.Stat(filepath.Join(dest, "install.sh"))
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode()&0o100 == 0 {
		t.Errorf("install.sh lost its executable bit: %v", fi.Mode())
	}
	if _, err := os.Stat(filepath.Join(dest, ".git")); !os.IsNotExist(err) {
		t.Errorf("export must carry no .git (err=%v)", err)
	}
}

func TestExport_UnknownRevErrors(t *testing.T) {
	_, clone := remoteRepo(t)
	if err := Export(ctx10(t), clone, "0000000000000000000000000000000000000000", t.TempDir()); err == nil {
		t.Error("exporting an unknown rev must error")
	}
}
