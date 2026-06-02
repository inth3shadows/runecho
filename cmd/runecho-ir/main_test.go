package main

import (
	"testing"

	"github.com/inth3shadows/runecho/internal/snapshot"
)

func TestDeriveRepoName(t *testing.T) {
	cases := []struct {
		root string
		want string
	}{
		{"/home/ericm/personal_projects/runecho/master", "runecho-master"},
		{"/home/ericm/personal_projects/coriolis/eric", "coriolis-eric"},
		{"/home/ericm/foo", "ericm-foo"},
		{"/foo", "foo"}, // parent is filesystem root → basename only
	}
	for _, c := range cases {
		if got := snapshot.DeriveRepoName(c.root); got != c.want {
			t.Errorf("DeriveRepoName(%q) = %q, want %q", c.root, got, c.want)
		}
	}
}
