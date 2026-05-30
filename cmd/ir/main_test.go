package main

import "testing"

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
		if got := deriveRepoName(c.root); got != c.want {
			t.Errorf("deriveRepoName(%q) = %q, want %q", c.root, got, c.want)
		}
	}
}
