package ir

import (
	"errors"
	"fmt"
	"strings"
	"testing"
)

// The truth table. Every state the walk can observe appears exactly once, and
// every row is named for the rule it pins. A row that has no test, or a branch
// in decideWalkEntry that is not a row, is the defect this file exists to make
// impossible.
//
// Zero values are the common case and are left implicit: err = errNone,
// kind = kindFile, ext = extNone, isRoot = false, ignoredName = false.
type walkRow struct {
	name  string
	entry walkEntry
	want  walkDecision
}

// walkDecisionRows is the table itself, hoisted out of the test that reads it so
// TestWalkDecisionCrossProduct can read it too. The cross-product asserts that
// every decision decideWalkEntry can return appears HERE -- which is the half of
// "no branch decides anything that is not a row" that was previously only
// asserted in a comment.
func walkDecisionRows() []walkRow {
	yes := func() bool { return true }
	no := func() bool { return false }
	link := func(k linkKind) func() linkKind { return func() linkKind { return k } }

	return []walkRow{
		// ---- Walk handed us an error -------------------------------------
		{
			// The entry is GONE. Nothing was hidden, so nothing is reported --
			// recording it puts a transient, self-healing false block in the
			// array a gate fails closed on.
			name:  "vanished child records nothing",
			entry: walkEntry{err: errGone},
			want:  doNothing(),
		}, {
			name:  "vanished directory records nothing",
			entry: walkEntry{err: errGone, infoPresent: true},
			want:  doNothing(),
		}, {
			// infoPresent means the entry was statted and its READDIR failed:
			// the directory itself is the blind spot, and one entry covers
			// everything beneath it under the prefix rule.
			name:  "unreadable directory blames itself",
			entry: walkEntry{err: errOpaque, infoPresent: true},
			want:  recordSelf(SkipUnreadableDir),
		}, {
			// A 0444 directory fails EVERY child lstat. Probing the parent
			// directly implicates it on its own evidence, not by inference.
			name:  "child lstat failure inside a wholly unreadable directory blames the parent",
			entry: walkEntry{err: errOpaque, parentUnreadable: yes},
			want:  recordParent(SkipUnreadableDir),
		}, {
			// Siblings are fine, so the parent is not the problem: one entry
			// is (EIO/ESTALE on a flaky mount). Blaming the parent here marked
			// all of src/ unexamined while the walk indexed every sibling.
			name:  "lone unstattable child with no extension blames the child",
			entry: walkEntry{err: errOpaque, parentUnreadable: no, ext: extNone},
			want:  recordSelf(SkipUnreadableFile),
		}, {
			// An extension proves it is a FILE, and a non-code file is not a
			// symbol blind spot. Recording it blocked every docs change.
			name:  "lone unstattable non-code file records nothing",
			entry: walkEntry{err: errOpaque, parentUnreadable: no, ext: extNonCode},
			want:  doNothing(),
		}, {
			name:  "lone unstattable document file records nothing",
			entry: walkEntry{err: errOpaque, parentUnreadable: no, ext: extDocument},
			want:  doNothing(),
		}, {
			name:  "lone unstattable source file in an unparsed language is a blind spot",
			entry: walkEntry{err: errOpaque, parentUnreadable: no, ext: extKnownSource},
			want:  recordSelf(SkipUnreadableFile),
		}, {
			name:  "lone unstattable supported source file is a blind spot",
			entry: walkEntry{err: errOpaque, parentUnreadable: no, ext: extSupported},
			want:  recordSelf(SkipUnreadableFile),
		},

		// ---- The walk root ------------------------------------------------
		{
			// The rule exists to prune SUBdirectories. Applied to the directory
			// the operator named, it pruned the whole walk, indexed zero files,
			// and reported a vacuous 100% coverage.
			name:  "the walk root is descended even when its name is on the ignore list",
			entry: walkEntry{isRoot: true, kind: kindDir, ignoredName: true},
			want:  descend(),
		}, {
			// The root is resolved before walking, so it only arrives as a link
			// when resolution FAILED. Blame is the root, which the caller turns
			// into rootUnreadable -- the one condition no path string expresses.
			name:  "an unresolvable symlinked root is total blindness",
			entry: walkEntry{isRoot: true, kind: kindSymlink},
			want:  recordSelf(SkipUnreadableDir),
		}, {
			name:  "a root that is itself a source file is indexed",
			entry: walkEntry{isRoot: true, ext: extSupported},
			want:  indexIt(),
		}, {
			// The ORDINARY root. Previously reached only by falling through to
			// the non-root directory rule, because isRoot gated just two rules --
			// correct behaviour arrived at by implicit reasoning, which is the
			// habit that produced five review rounds. If a caller ever turned an
			// unmatched state into "do nothing", the entire repository would be
			// absent from the IR with root_unreadable false.
			name:  "the ordinary walk root is descended",
			entry: walkEntry{isRoot: true, kind: kindDir},
			want:  descend(),
		}, {
			// The blame is the root, so the CALLER turns this into root_unreadable
			// rather than an entry -- see TestFileRootInAnUnparsedLanguageFails-
			// Closed, which pins the observable outcome. Naming this row
			// "reported, not dropped" described the table's return value and not
			// what a consumer sees, which is the caller/callee gap the two tables
			// exist to close.
			name:  "a root that is source in a language with no parser is blamed, not dropped",
			entry: walkEntry{isRoot: true, ext: extKnownSource},
			want:  recordSelf(SkipUnsupportedLanguage),
		}, {
			name:  "a root that is a non-code file records nothing",
			entry: walkEntry{isRoot: true, ext: extDocument},
			want:  doNothing(),
		}, {
			name:  "a root that is an extensionless file records nothing",
			entry: walkEntry{isRoot: true, ext: extNone},
			want:  doNothing(),
		}, {
			// Zero files indexed is a vacuous 100% coverage, so every other
			// signal reads clean. The caller turns a blame on the root into
			// root_unreadable, which is the one condition no path string states.
			name:  "an unreadable root is total blindness",
			entry: walkEntry{isRoot: true, err: errOpaque, infoPresent: true, kind: kindDir},
			want:  recordSelf(SkipUnreadableDir),
		}, {
			// The deliberate exception to "ENOENT records nothing": that rule
			// keeps transient paths out of the fail-closed ARRAY, and
			// root_unreadable is a flag, not an entry. A root that is not there
			// examined nothing, which is not transient from the walk's view.
			name:  "a root that is not there is total blindness, not a mid-walk race",
			entry: walkEntry{isRoot: true, err: errGone, kind: kindDir},
			want:  recordSelf(SkipUnreadableDir),
		},

		// ---- The ignore list ----------------------------------------------
		{
			name:  "an ignored directory is recorded and pruned",
			entry: walkEntry{kind: kindDir, ignoredName: true},
			want:  recordSelfPrune(SkipIgnoredDir),
		}, {
			// pnpm and yarn make node_modules a link. That is the operator's
			// configured ignore, not a capability blind spot -- and SkipDir from
			// a non-directory skips the remaining SIBLINGS, so never prune here.
			name:  "an ignored directory stored as a symlink is an ignore, not a symlink",
			entry: walkEntry{kind: kindSymlink, ignoredName: true},
			want:  recordSelf(SkipIgnoredDir),
		}, {
			// Git worktrees and submodules use a .git FILE. Calling it an
			// ignored_dir was untrue and consumed recorder budget on every walk.
			name:  "a regular FILE whose name is on the ignore list is not an ignored directory",
			entry: walkEntry{ignoredName: true, ext: extNone},
			want:  doNothing(),
		}, {
			name:  "the ignore list never blocks indexing a regular source file",
			entry: walkEntry{ignoredName: true, ext: extSupported},
			want:  indexIt(),
		},

		// ---- Symlinks ------------------------------------------------------
		{
			// Permanent, unlike a mid-walk race: `latest -> build-123` would
			// block every change in the repo until someone deleted the link.
			name:  "a dangling symlink records nothing",
			entry: walkEntry{kind: kindSymlink, target: link(linkGone)},
			want:  doNothing(),
		}, {
			// No extension, so it could be a directory. Fail closed.
			name:  "an unresolvable symlink with no extension is an unreadable directory",
			entry: walkEntry{kind: kindSymlink, target: link(linkOpaque), ext: extNone},
			want:  recordSelf(SkipUnreadableDir),
		}, {
			// extDocument is the ONLY class that is positive evidence of
			// file-ness -- a curated table vouches for it -- so dropping it
			// cannot discard a subtree.
			name:  "an unresolvable symlink to a document file records nothing",
			entry: walkEntry{kind: kindSymlink, target: link(linkOpaque), ext: extDocument},
			want:  doNothing(),
		}, {
			// `deps -> <unreadable>/deps.v2`. Read as "has an extension, not
			// source, therefore a file we do not care about", this dropped an
			// entire unexamined Go subtree with skipped:[] and exit 0. A dotted
			// name is a DIRECTORY as readily as a file.
			name:  "an unresolvable symlink to an unvouched dotted name is a directory until proven otherwise",
			entry: walkEntry{kind: kindSymlink, target: link(linkOpaque), ext: extNonCode},
			want:  recordSelf(SkipUnreadableDir),
		}, {
			name:  "an unresolvable symlink to source is an unreadable FILE",
			entry: walkEntry{kind: kindSymlink, target: link(linkOpaque), ext: extSupported},
			want:  recordSelf(SkipUnreadableFile),
		}, {
			name:  "an unresolvable symlink to unparsed source is an unreadable FILE",
			entry: walkEntry{kind: kindSymlink, target: link(linkOpaque), ext: extKnownSource},
			want:  recordSelf(SkipUnreadableFile),
		}, {
			name:  "a symlinked directory is not followed",
			entry: walkEntry{kind: kindSymlink, target: link(linkDir)},
			want:  recordSelf(SkipSymlinkDir),
		}, {
			name:  "a symlinked source file is not followed",
			entry: walkEntry{kind: kindSymlink, target: link(linkFile), ext: extSupported},
			want:  recordSelf(SkipSymlink),
		}, {
			name:  "a symlinked source file in an unparsed language is still a blind spot",
			entry: walkEntry{kind: kindSymlink, target: link(linkFile), ext: extKnownSource},
			want:  recordSelf(SkipSymlink),
		}, {
			name:  "a symlinked README records nothing",
			entry: walkEntry{kind: kindSymlink, target: link(linkFile), ext: extDocument},
			want:  doNothing(),
		}, {
			name:  "a symlinked file with an unvouched extension records nothing",
			entry: walkEntry{kind: kindSymlink, target: link(linkFile), ext: extNonCode},
			want:  doNothing(),
		}, {
			name:  "a symlinked extensionless file records nothing",
			entry: walkEntry{kind: kindSymlink, target: link(linkFile), ext: extNone},
			want:  doNothing(),
		},

		// ---- Directories and files ----------------------------------------
		{
			name:  "an ordinary directory is descended, not recorded",
			entry: walkEntry{kind: kindDir},
			want:  descend(),
		}, {
			name:  "a supported source file is indexed",
			entry: walkEntry{ext: extSupported},
			want:  indexIt(),
		}, {
			name:  "source in a language with no parser is the headline blind spot",
			entry: walkEntry{ext: extKnownSource},
			want:  recordSelf(SkipUnsupportedLanguage),
		}, {
			name:  "a README is neither indexed nor recorded",
			entry: walkEntry{ext: extDocument},
			want:  doNothing(),
		}, {
			name:  "a file with an unvouched extension is neither indexed nor recorded",
			entry: walkEntry{ext: extNonCode},
			want:  doNothing(),
		}, {
			name:  "a Makefile is neither indexed nor recorded",
			entry: walkEntry{ext: extNone},
			want:  doNothing(),
		},
	}
}

func TestWalkDecisionTable(t *testing.T) {
	g := NewGenerator(GeneratorConfig{})
	for _, tc := range walkDecisionRows() {
		t.Run(tc.name, func(t *testing.T) {
			e := tc.entry
			// Any probe the row did not supply must not be called: each is a
			// syscall, and a row that reaches for one it does not need is
			// exactly the cross-branch coupling this table exists to prevent.
			if e.parentUnreadable == nil {
				e.parentUnreadable = func() bool {
					t.Fatal("probed the parent directory; this row must not need it")
					return false
				}
			}
			if e.target == nil {
				e.target = func() linkKind {
					t.Fatal("resolved a symlink target; this row must not need it")
					return linkGone
				}
			}
			if got := g.decideWalkEntry(e); got != tc.want {
				t.Fatalf("decideWalkEntry(%s):\n got %+v\nwant %+v", tc.name, got, tc.want)
			}
		})
	}
}

// classifyExt is consulted only where the entry is already known to be
// file-shaped, but it must never mistake a dot-named DIRECTORY for a file with
// an unknown extension -- that dropped .git, .venv, .tox, .cursor and .vscode
// from every rule that asks "is this something we do not care about".
func TestClassifyExt(t *testing.T) {
	g := NewGenerator(GeneratorConfig{})
	cases := []struct {
		base string
		want extClass
	}{
		{"main.go", extSupported},
		{"A.GO", extSupported},
		{"Widget.java", extKnownSource},
		{"b.JAVA", extKnownSource},
		{"README.md", extDocument},
		{"logo.png", extDocument},
		{"pkg.d", extNonCode},
		{"deps.v2", extNonCode},
		{"src.bak", extNonCode},
		{"python3.11", extNonCode},
		{"Makefile", extNone},
		{".github", extNone},
		{".git", extNone},
		{".gitignore", extNone},
		// A file named literally `.go` IS Go source. Stripping the leading dot
		// unconditionally -- the first fix for the `.github` defect -- turned it
		// into extNone: never indexed and never reported, a fail-open one level
		// in from the bug it was fixing.
		{".go", extSupported},
		{".java", extKnownSource},
		{".py", extSupported},
		// Not a language, so the dot-name is discarded and it has no extension.
		{".md", extNone},
		{".terraform.lock.hcl", extNonCode},
		{".env", extNone},
		{".dockerignore", extNone},
		{".eslintrc.js", extSupported},
	}
	for _, tc := range cases {
		if got := g.classifyExt(tc.base); got != tc.want {
			t.Errorf("classifyExt(%q) = %v, want %v", tc.base, got, tc.want)
		}
	}
}

// TestWalkDecisionCrossProduct is the completeness gate. The named rows above
// carry the WHY; this carries the proof that nothing escaped them.
//
// "Every row has a named test, and no branch decides anything that is not a row"
// was, until this test existed, asserted in a comment and enforced by nobody. An
// omitted state produced the struct zero value, which under the old bool-struct
// walkDecision was indistinguishable from "index nothing, record nothing, keep
// walking" -- a silent fail-open, and the branch-nest failure mode relocated
// into the type rather than removed from the program.
//
// Three properties over all 2880 states:
//
//  1. TOTALITY -- no state returns actionUnset. This is why actionUnset exists.
//  2. CLOSURE -- every decision returned appears in walkDecisionRows(). A branch
//     that invents a decision no row names fails here even though it is total.
//
// Both probes are supplied on every state, deliberately. Probe DISCIPLINE (a row
// must not reach for a syscall it does not need) is the named table's job, where
// an unsupplied probe calls t.Fatal. Mixing the two would make each weaker.
//
// parentUnreadable is a real axis and is enumerated. The first version wired it
// as `func() bool { return infoPresent }`, and decideWalkError only consults the
// probe when !infoPresent -- so across every iteration it returned false the one
// time it was asked. recordParent was never produced, neither property was
// proven for parentUnreadable==true, and the `total` assertion pinned half the
// space as if it were whole. A gate that enumerates an axis it has pinned to a
// constant is worse than no gate: it reads as proof.
func TestWalkDecisionCrossProduct(t *testing.T) {
	g := NewGenerator(GeneratorConfig{})

	named := map[walkDecision]string{}
	for _, row := range walkDecisionRows() {
		named[row.want] = row.name
	}

	errs := []errKind{errNone, errGone, errOpaque}
	kinds := []entryKind{kindFile, kindDir, kindSymlink}
	exts := []extClass{extNone, extNonCode, extDocument, extKnownSource, extSupported}
	links := []linkKind{linkGone, linkOpaque, linkDir, linkFile}
	bools := []bool{false, true}

	total, unset := 0, 0
	seenParentBlame := false
	for _, isRoot := range bools {
		for _, ek := range errs {
			for _, infoPresent := range bools {
				for _, kind := range kinds {
					for _, ignored := range bools {
						for _, ext := range exts {
							for _, lk := range links {
								for _, parentBad := range bools {
									total++
									e := walkEntry{
										isRoot:           isRoot,
										err:              ek,
										infoPresent:      infoPresent,
										kind:             kind,
										ignoredName:      ignored,
										ext:              ext,
										parentUnreadable: func() bool { return parentBad },
										target:           func() linkKind { return lk },
									}
									got := g.decideWalkEntry(e)
									if got.action == actionUnset {
										unset++
										if unset <= 5 {
											t.Errorf("no row matched %+v — an unmatched state "+
												"is a silent fail-open", summarize(e, lk, parentBad))
										}
										continue
									}
									if _, ok := named[got]; !ok {
										t.Errorf("state %s decided %+v, which no named row "+
											"claims — a branch is deciding something the table "+
											"does not say", summarize(e, lk, parentBad), got)
									}
									if got.records() != (got.reason != "") {
										t.Errorf("state %s: action %v with reason %q — a reason "+
											"without a record, or a record without a reason",
											summarize(e, lk, parentBad), got.action, got.reason)
									}
									if got.prunes() && !(ek == errNone && kind == kindDir) {
										t.Errorf("state %s pruned from a non-directory; Walk "+
											"documents that SkipDir there skips the remaining "+
											"SIBLINGS", summarize(e, lk, parentBad))
									}
									if got.blame == blameParent {
										seenParentBlame = true
									}
								}
							}
						}
					}
				}
			}
		}
	}
	// The axis exists to make this decision reachable. If no state produces it,
	// the axis is pinned to a constant again and the gate is back to reading as
	// proof of a space it never entered.
	if !seenParentBlame {
		t.Error("no state blamed the PARENT; the parentUnreadable axis is not " +
			"actually being varied")
	}
	if unset > 0 {
		t.Errorf("%d of %d states fell through the table", unset, total)
	}
	if total != 2880 {
		t.Errorf("cross-product size = %d, want 2880 — an axis was added or "+
			"removed without updating this gate", total)
	}
}

func summarize(e walkEntry, lk linkKind, parentBad bool) string {
	return fmt.Sprintf("{root:%v err:%v info:%v kind:%v ignored:%v ext:%v target:%v parentBad:%v}",
		e.isRoot, e.err, e.infoPresent, e.kind, e.ignoredName, e.ext, lk, parentBad)
}

// TestDecideBlameTable pins the second pure table. Every row is a shape that
// silently matches NOTHING in a consumer's changed-path set, which is a
// fail-open wearing a clean-looking payload.
func TestDecideBlameTable(t *testing.T) {
	cases := []struct {
		name   string
		rel    string
		relErr bool
		want   blameOutcome
	}{
		{"an ordinary repo-relative path is recorded", "src/legacy", false, blameRecord},
		{"a nested path is recorded", "a/b/c/d.java", false, blameRecord},
		{"a bare file name is recorded", "Makefile", false, blameRecord},
		// filepath.Rel failing means the blame is not under the root at all.
		{"an unresolvable blame is total blindness", "", true, blameRootUnreadable},
		{"the root itself is total blindness", ".", false, blameRootUnreadable},
		// The parent of a child directly under the root IS the root.
		{"the parent of the root is total blindness", "..", false, blameRootUnreadable},
		{"anything above the root is total blindness", "../sibling", false, blameRootUnreadable},
		{"an empty path is total blindness", "", false, blameRootUnreadable},
		// filepath.Rel cannot emit one, which is why nobody tested it.
		{"an absolute path is total blindness", "/etc/passwd", false, blameRootUnreadable},
		// A backslash is a legal Linux filename character. Rejecting it would set
		// rootUnreadable for the WHOLE repo over one oddly named file.
		{`a backslash in a name is an ordinary path on linux`, `weird\name.go`, false, blameRecord},
		{"a leading dot-slash is already stripped by normalizePath", ".hidden/x.go", false, blameRecord},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := decideBlame(tc.rel, tc.relErr); got != tc.want {
				t.Errorf("decideBlame(%q, %v) = %v, want %v", tc.rel, tc.relErr, got, tc.want)
			}
		})
	}
}

// FuzzDecideBlame pins the property the table exists for: anything decideBlame
// agrees to RECORD must be a path a consumer can actually match against its own
// repo-relative changed set. Fuzzing is a house pattern here -- internal/ir,
// internal/guard, internal/guardstats, internal/claims and three parser files
// already ship Fuzz functions -- not a new dependency.
func FuzzDecideBlame(f *testing.F) {
	for _, seed := range []string{
		"", ".", "..", "../x", "/abs", "a/b.go", "Makefile", `w\x.go`, "./x", "a/../b",
		// Found by this fuzz on its first run: "./" passed every explicit check
		// and was RECORDED, while its normalised form is "" and would not have
		// been. Kept as a named seed rather than an opaque corpus file so the
		// case is visible in review.
		"./", ".//", "././",
	} {
		f.Add(seed, false)
		f.Add(seed, true)
	}
	f.Fuzz(func(t *testing.T, rel string, relErr bool) {
		got := decideBlame(rel, relErr)
		if got == blameOutcomeUnset {
			t.Fatalf("decideBlame(%q, %v) returned the zero value; the table must be total",
				rel, relErr)
		}
		if got != blameRecord {
			return
		}
		if relErr {
			t.Errorf("recorded %q despite an unresolvable blame", rel)
		}
		if rel == "" || rel == "." || rel == ".." || strings.HasPrefix(rel, "../") {
			t.Errorf("recorded %q, which is at or above the root and matches no "+
				"repo-relative path a consumer holds", rel)
		}
		if strings.HasPrefix(rel, "/") {
			t.Errorf("recorded absolute path %q, which silently matches nothing", rel)
		}
		// The entry becomes an IR map key, and the caller may re-normalise it.
		if n := normalizePath(rel); decideBlame(n, false) != blameRecord {
			t.Errorf("recorded %q but its normalised form %q would not be recorded; "+
				"the decision must survive normalisation", rel, n)
		}
	})
}

// TestClassifySymlinkExt pins the two OPPOSITE failures neither name alone can
// avoid. Rows are named for the one that would come back if the rule regressed.
func TestClassifySymlinkExt(t *testing.T) {
	g := NewGenerator(GeneratorConfig{})
	cases := []struct {
		name, link, target string
		want               extClass
	}{{
		// Target-only classification loses this: Walk never follows the link, so
		// the symbols are absent from the IR, and a non-code target name means
		// nothing is recorded either. A deleted symbol goes unnoticed at exit 0.
		name: "a link NAMED as source is source even when its target is not",
		link: "handler.go", target: "handler.go.tmpl", want: extSupported,
	}, {
		name: "a link named as unparsed source is still source",
		link: "Widget.java", target: "widget.txt", want: extKnownSource,
	}, {
		name: "a link named as source beats a document target too",
		link: "main.go", target: "notes.md", want: extSupported,
	}, {
		// Link-only classification loses this: `README` and `LICENSE` are both
		// extensionless, so both failed closed, and non-code in the fail-closed
		// array blocks every documentation change in the repo.
		name: "an extensionless link to a doc is a document",
		link: "README", target: "README.md", want: extDocument,
	}, {
		// The severe one: a dotted DIRECTORY name is not evidence of a file.
		name: "an extensionless link to a dotted directory name stays unvouched",
		link: "deps", target: "deps.v2", want: extNonCode,
	}, {
		name: "an extensionless link to source is source",
		link: "Gadget", target: "Widget.java", want: extKnownSource,
	}, {
		// Neither name gives anything away, so it may be a source DIRECTORY.
		name: "extensionless both ends stays ambiguous and fails closed",
		link: "LICENSE", target: "LICENSE", want: extNone,
	}, {
		// A link whose target cannot be read at all: os.Readlink failed, so the
		// link's own name is the only evidence there is.
		name: "an unreadable link falls back to its own name",
		link: "main.go", target: "", want: extSupported,
	}, {
		name: "an unreadable link with no name evidence stays ambiguous",
		link: "current", target: "", want: extNone,
	}}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := g.classifySymlinkExt(tc.link, tc.target); got != tc.want {
				t.Errorf("classifySymlinkExt(%q, %q) = %v, want %v",
					tc.link, tc.target, got, tc.want)
			}
		})
	}
}

// TestApplyWalkDecisionFailsClosedOnAnUnknownDecision exercises the two
// backstops that no reachable state can reach today.
//
// That is the point of them. walkdecision.go documented "the caller treats
// actionUnset as rootUnreadable" while the caller returned early and recorded
// nothing — byte-equivalent to "index nothing, record nothing, keep walking",
// the fail-open actionUnset was introduced to remove, relocated from the struct
// into the caller. A documented backstop nobody executes is not a backstop.
func TestApplyWalkDecisionFailsClosedOnAnUnknownDecision(t *testing.T) {
	relOf := func(p string) (string, error) { return normalizePath(p), nil }

	t.Run("the zero decision does not vouch for the tree", func(t *testing.T) {
		rec := &skipRecorder{}
		applyWalkDecision(rec, "src/main.go", walkDecision{}, relOf)
		if !rec.rootUnreadable {
			t.Error("actionUnset recorded nothing and left rootUnreadable false; " +
				"a state nobody thought about must not read as a clean tree")
		}
		if items, _, _ := rec.result(); len(items) != 0 {
			t.Errorf("actionUnset emitted %v; it has no reason to emit", items)
		}
	})

	t.Run("an unclassifiable blame is never emitted as an entry", func(t *testing.T) {
		// relOf fails, so decideBlame cannot produce a matchable path. The entry
		// must not reach the array a gate fails closed on.
		rec := &skipRecorder{}
		failing := func(string) (string, error) { return "", errRelFailed }
		applyWalkDecision(rec, "src", recordSelf(SkipUnreadableDir), failing)
		if !rec.rootUnreadable {
			t.Error("an unresolvable blame left rootUnreadable false")
		}
		if items, _, _ := rec.result(); len(items) != 0 {
			t.Errorf("emitted %v for a blame that resolves to nothing a consumer holds", items)
		}
	})

	t.Run("an ordinary blame is still recorded", func(t *testing.T) {
		rec := &skipRecorder{}
		applyWalkDecision(rec, "src/legacy", recordSelf(SkipUnreadableDir), relOf)
		if rec.rootUnreadable {
			t.Error("an ordinary repo-relative blame raised rootUnreadable")
		}
		items, _, _ := rec.result()
		if len(items) != 1 || items[0].Path != "src/legacy" {
			t.Errorf("items = %v, want one entry for src/legacy", items)
		}
	})

	t.Run("a nil recorder is a no-op, as the API documents", func(t *testing.T) {
		// walkSourceFiles' doc says "nil disables recording", and skipRecorder
		// says a nil receiver is a no-op. add() honoured that; the direct
		// `rec.rootUnreadable = true` field write did not, and the rewrite
		// widened the states that reach it.
		defer func() {
			if p := recover(); p != nil {
				t.Fatalf("nil recorder panicked: %v", p)
			}
		}()
		applyWalkDecision(nil, "src", walkDecision{}, relOf)
		applyWalkDecision(nil, "src", recordSelf(SkipUnreadableDir), relOf)
	})
}

var errRelFailed = errors.New("cannot make relative")
