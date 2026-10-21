# Contributing to RunEcho

Thanks for helping. Bug reports, false-positive reports and fixes are all welcome.

## Reporting

- **A false positive or a missed defect:** open an issue with the smallest input
  that reproduces it (a few lines of code is ideal), what the guard said, and what
  you expected.
- **A new kind of check:** use the *Propose a new guard check* issue template, and read
  [docs/check-worthiness.md](docs/check-worthiness.md) first. It is the gate those
  requests go through.
- **A security issue:** do not open a public issue. See [SECURITY.md](SECURITY.md).

## Building and testing

You need Go 1.26 or newer.

```sh
bash install.sh          # build all three binaries
go test ./...            # the full suite
go vet ./...
gofmt -l .               # must print nothing
```

## Pull requests

- **Start from a failing test.** A fix comes with a test that fails before it
  and passes after. Changing an existing test to make it pass is a finding, not
  a fix.
- **Guard and parser changes need evidence beyond the unit test.** The extractors
  run on every edit, so a change that binds one more name can hide a real
  hallucination elsewhere. Say what you checked: a differential over a real corpus
  (for example GOROOT/src) and what changed, or why it can't change other inputs.
- **Commit subjects set the release.** `docs:` and `chore:` cut no release.
  `guard:` and `parser:` cut a minor release. Anything else cuts a patch.
- **Keep a PR to one issue.** Reference it with `Fixes #N` only when it fixes all
  of that issue.

Every PR is reviewed before merge, and CI must be green.

## Credit

If we take your PR as-is, it merges with your commits and you appear as a
contributor. Sometimes we use part of a PR and rework the rest, for example
because the code it touches changed while it was open. In that case we keep at
least one of your commits (or add a `Co-authored-by:` trailer), so GitHub still
credits you. We also comment on your PR to say what was used and where it landed
before closing it.

## Licensing

RunEcho is MIT-licensed. By submitting a contribution you agree that it is
licensed under the same terms (inbound = outbound). There is no separate CLA to
sign.

## Conduct

This project follows the [Code of Conduct](CODE_OF_CONDUCT.md).
