# Contributing to RunEcho

Thanks for helping. Bug reports, false-positive reports and fixes are all welcome.

## Reporting

- **A bug:** use the *Report a bug* issue template. For a guard false positive or
  a missed defect, include the smallest input that reproduces it (a few lines of
  code is ideal), what the guard said, and what you expected. Bugs in
  `runecho-ir`, `runecho-mcp`, `install.sh` or self-update use the same template.
- **A new kind of check:** use the *Propose a new guard check* issue template, and read
  [docs/check-worthiness.md](docs/check-worthiness.md) first. It is the gate those
  requests go through.
- **A security issue:** do not open a public issue. See [SECURITY.md](SECURITY.md).

## Building and testing

You need Go 1.26 or newer (the floor in `go.mod`).

```sh
go build ./...           # build everything
go test ./...            # the full suite
go vet ./...
gofmt -l .               # must print nothing
```

CI formats with the gofmt of the Go version in `go.mod`. A much newer local
gofmt can disagree with it; if CI's Format step fails and yours doesn't, use
that version's gofmt.

`bash install.sh` builds the three binaries **and installs them** into
`$RUNECHO_BIN_DIR` (default `~/.local/bin`). If you use RunEcho's hooks yourself,
that replaces the binaries they run. Set `RUNECHO_BIN_DIR=$PWD/bin` to keep a
branch build separate.

## Pull requests

- **Start from a failing test.** A fix comes with a test that fails before it
  and passes after. Changing an existing test to make it pass is a finding, not
  a fix.
- **Guard and parser changes need evidence beyond the unit test.** The extractors
  run on every edit, so a change that binds one more name can hide a real
  hallucination elsewhere. Say what you checked: a differential over a real corpus
  (for example GOROOT/src) and what changed, or why it can't change other inputs.
- **The PR title sets the release.** PRs are squash-merged, and the squash
  commit's subject is the PR title. `docs:` and `chore:` (with or without a scope,
  e.g. `docs(bench):`) cut no release, `guard:` and `parser:` cut a minor
  release, and anything else cuts a patch.
- **Keep a PR to one issue.** Reference it with `Fixes #N` only when it fixes all
  of that issue.

Maintainers merge a PR only after review and with CI green.

## Credit

PRs are squash-merged, which on its own would leave only the merger as the
commit's author. So whenever a merged change includes your work, whether your
PR as-is or part of it reworked in another PR (for example because the code it
touches changed while it was open), the squash commit carries a
`Co-authored-by:` trailer with your GitHub identity, and GitHub lists you as a
contributor. If your PR is superseded, we also comment on it to say what was used
and where it landed before closing it.

## Licensing

RunEcho is MIT-licensed. By submitting a contribution you agree that it is
licensed under the same terms (inbound = outbound). There is no separate CLA to
sign.

## Conduct

This project follows the [Code of Conduct](CODE_OF_CONDUCT.md).
