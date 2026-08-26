package ir

import (
	"path/filepath"
	"strings"
)

// This file is the ONE place the walk decides what to do with an entry.
// Everything here is pure: the caller does the syscalls, classifies what it saw
// into a walkEntry, and applies the walkDecision it gets back.
//
// It is a decision table because the branch-per-case version was not
// maintainable. Five review rounds on PR #366 produced 12 / 18 / 11 / 11 / 10
// findings, a flat rate, and every round's fix introduced a new regression --
// always the same shape: one branch decided an axis differently from another
// branch facing the same state. A table cannot do that. There is one row per
// state, every row has a named test, and no branch decides anything that is not
// a row.

// errKind classifies the error filepath.Walk hands the callback.
type errKind int

const (
	// errNone -- the entry was lstatted successfully.
	errNone errKind = iota
	// errGone -- os.IsNotExist. The entry is not there, so nothing was hidden
	// from us and nothing is reported. Covers both the ordinary mid-walk race
	// (a checkout or an editor temp file) and the permanent dangling link.
	errGone
	// errOpaque -- anything else (EACCES, EIO, ELOOP, ESTALE). Something IS
	// there and the walk could not look at it. That is a real blind spot.
	errOpaque
)

// entryKind classifies what Walk lstatted. Meaningful only when err == errNone.
// Walk LSTATS, so a link to a directory arrives as kindSymlink, never kindDir.
type entryKind int

const (
	kindFile entryKind = iota
	kindDir
	kindSymlink
)

// extClass classifies an entry's extension.
//
// The rule this used to run on -- "a path that HAS an extension is a file" --
// is FALSE, and believing it cost a silent fail-open: `deps -> <unreadable>/deps.v2`
// was read as "has an extension, not source, so a file we do not care about"
// and dropped, discarding an entire unexamined subtree at exit 0. Dotted
// directory names are ordinary.
//
// So file-ness is asserted only where a curated table vouches for it
// (extDocument) or where the extension names source (extSupported,
// extKnownSource). extNonCode and extNone are both "we do not know", and every
// row that must choose between a file and an unreadable directory fails closed
// on them.
type extClass int

const (
	// extNone -- no extension. Could be a directory, a Makefile, or a dotfile.
	extNone extClass = iota
	// extNonCode -- has an extension, and it is not source code, and the table
	// does not positively vouch for it being a file. `deps.v2`, `src.bak`,
	// `pkg.d`: every one of those is a directory as readily as a file, which is
	// why this class is NOT evidence of file-ness. See extDocument.
	extNonCode
	// extDocument -- an extension that positively names a documentation,
	// configuration or asset FILE, from the curated documentExtensions table.
	// This is the only class that is evidence a path is a file rather than a
	// directory, and the only one an unresolvable symlink may be dropped on.
	extDocument
	// extKnownSource -- source code in a language no registered parser handles.
	// THE reason skip reporting exists: absent from the IR, therefore absent
	// from a diff, therefore a deleted symbol is invisible at exit 0.
	extKnownSource
	// extSupported -- a registered parser handles it.
	extSupported
)

// linkKind classifies os.Stat (not Lstat) of a symlink's target.
type linkKind int

const (
	linkGone linkKind = iota
	linkOpaque
	linkDir
	linkFile
)

// blameTarget names which path a record is about. The caller resolves it; blame
// that lands at or above the walk root becomes rootUnreadable instead of an
// entry, because "." and ".." match no repo-relative path a consumer holds.
type blameTarget int

const (
	blameSelf blameTarget = iota
	blameParent
)

// walkEntry is everything the decision may look at for one walk callback.
// The two probes are lazy because each is a syscall and most rows never consult
// one; a row that touches a probe it should not is a defect the table test
// catches by failing.
type walkEntry struct {
	isRoot bool
	err    errKind
	// infoPresent is false when Walk's LSTAT of a child failed (the child's
	// name is then the only evidence we have) and true when the entry itself
	// was statted and its READDIR failed (the directory is the blind spot).
	infoPresent bool
	kind        entryKind
	ignoredName bool
	// ext classifies the entry's extension. For a FILE that is the file's own
	// base name. For a SYMLINK it is the base name of the link's TARGET, read
	// with os.Readlink -- which does not follow, so it answers even when the
	// target is unreadable or absent.
	//
	// The link's own name is the wrong input: `README -> /mnt/x/README.md` and
	// `LICENSE -> /mnt/x/LICENSE` are indistinguishable by it, so the first was
	// recorded as an unreadable directory and blocked every documentation change
	// in the repo. The target's name resolves the first and leaves the second
	// honestly ambiguous, where failing closed is correct. One cheap syscall for
	// strictly more information. The path REPORTED is still the link's own -- it
	// is the path a consumer holds.
	ext extClass

	// parentUnreadable probes the containing directory DIRECTLY rather than
	// inferring it from one child's failure. Memoised by the caller per parent:
	// unmemoised it was O(N^2) and 4000 entries blew the generate deadline.
	parentUnreadable func() bool
	// target resolves a symlink. Walk lstats, so this is the only way to learn
	// what a link points at.
	target func() linkKind
}

// walkAction is the complete set of things the walk may do with an entry.
//
// The zero value is actionUnset and is NOT a decision. That is the whole point.
// The first draft used a struct of bools, so an omitted state produced
// walkDecision{} -- byte-identical to "index nothing, record nothing, keep
// walking". A state nobody thought about therefore became a silent fail-open,
// which is the branch-nest failure mode relocated into the type rather than
// removed from the program. The caller treats actionUnset as rootUnreadable: if
// the table cannot say what an entry is, the honest report is that the walk
// cannot vouch for the tree.
type walkAction int

const (
	// actionUnset -- no row matched. Never returned by decideWalkEntry; see
	// TestWalkDecisionCrossProduct, which proves it.
	actionUnset walkAction = iota
	// actionDescend -- an ordinary directory. Keep walking into it.
	actionDescend
	// actionSkipQuietly -- nothing to index and nothing to report. Distinct from
	// actionDescend so the table can tell "walked into src/" from "ignored a
	// Makefile"; as two bool-structs they were the same value and the rows that
	// meant different things tested identical.
	actionSkipQuietly
	// actionIndex -- hand it to the walker func.
	actionIndex
	// actionRecord -- add (blame, reason) to the recorder.
	actionRecord
	// actionRecordPrune -- record it AND return filepath.SkipDir. Only ever from
	// a real directory: Walk documents that SkipDir from a non-directory skips
	// the remaining SIBLINGS in the containing directory.
	actionRecordPrune
)

// walkDecision is one row's answer.
type walkDecision struct {
	action walkAction
	reason string // skip reason constant; empty unless the action records
	blame  blameTarget
}

func (d walkDecision) indexes() bool { return d.action == actionIndex }
func (d walkDecision) records() bool {
	return d.action == actionRecord || d.action == actionRecordPrune
}
func (d walkDecision) prunes() bool { return d.action == actionRecordPrune }

func doNothing() walkDecision { return walkDecision{action: actionSkipQuietly} }
func descend() walkDecision   { return walkDecision{action: actionDescend} }
func indexIt() walkDecision   { return walkDecision{action: actionIndex} }
func recordSelf(reason string) walkDecision {
	return walkDecision{action: actionRecord, reason: reason}
}
func recordParent(reason string) walkDecision {
	return walkDecision{action: actionRecord, reason: reason, blame: blameParent}
}
func recordSelfPrune(reason string) walkDecision {
	return walkDecision{action: actionRecordPrune, reason: reason}
}

// classifyExt answers "what kind of extension does this base name have", with
// one correction to filepath.Ext.
//
// filepath.Ext(".github") is ".github": a name that is ALL leading dot with no
// interior dot reads as its own extension. That classified every dot-named
// DIRECTORY -- .git, .venv, .tox, .cursor, .vscode -- as a file with an unknown
// non-code extension, and every rule asking "is this something we do not care
// about" silently dropped it.
//
// Stripping the leading dot unconditionally OVER-corrects, and the over-
// correction is worse than the bug: a file named literally `.go` or `.java` is
// source, and stripping turns it into extNone -- never indexed, never reported,
// a fail-open one level in. So a dot-name is discarded only when it is not
// itself an extension the indexer recognises as code. `.github` -> extNone;
// `.go` -> extSupported. `.eslintrc.js` classifies on `.js` either way, so no
// ordinary source file changes hands.
func (g *Generator) classifyExt(base string) extClass {
	ext := filepath.Ext(base)
	if ext == base && strings.HasPrefix(base, ".") && !g.isCodeExt(ext) {
		// The discard is right for `.github`, `.git`, `.venv` -- directories --
		// and wrong for `.npmrc` and `.gitignore`, which are files and are not
		// code. Left as extNone they read as "may be a directory" and started
		// failing CLOSED, blocking a commit that touched `.npmrc` with an entry
		// saying `unreadable_dir` on a file.
		if IsDocumentDotName(base) {
			return extDocument
		}
		return extNone
	}
	switch {
	case ext == "":
		return extNone
	case g.supportsExtension(ext):
		return extSupported
	case IsKnownSourceExtension(ext):
		return extKnownSource
	case IsDocumentExtension(ext):
		return extDocument
	default:
		return extNonCode
	}
}

// isCodeExt reports whether the indexer recognises ext as source at all --
// parsed or merely known. It is the predicate that keeps the dot-name
// correction from eating a file named `.go`.
func (g *Generator) isCodeExt(ext string) bool {
	return g.supportsExtension(ext) || IsKnownSourceExtension(ext)
}

// isCodeClass reports whether an extClass names source, parsed or not.
func isCodeClass(c extClass) bool {
	return c == extSupported || c == extKnownSource
}

// classifySymlinkExt combines a symlink's OWN base name with its target's.
//
// Neither name alone is enough, and the two failures are opposite:
//
//   - The link's own name alone cannot separate `README -> /mnt/x/README.md`
//     from `LICENSE -> /mnt/x/LICENSE`. Both are extensionless, so both failed
//     closed, and a non-code path in the fail-closed array blocks every
//     documentation change in the repo.
//   - The TARGET's name alone loses `handler.go -> handler.go.tmpl`, a readable
//     regular file. Walk never follows the link, so those symbols are absent
//     from the IR -- and with the target's name calling it non-code, nothing is
//     recorded either. A deleted symbol then goes unnoticed at exit 0, which is
//     the failure this whole package exists to close. It also inverted the
//     safety gradient: an UNREADABLE extensionless target failed closed while a
//     READABLE one failed open, treating the thing we can see as more suspicious
//     than the thing we cannot.
//
// The rule that resolves both is the one the rest of the table already runs on:
// an extension is decisive in exactly ONE direction -- a name that HAS a code
// extension is code. Applied to BOTH names, code on either side wins, and the
// target's name breaks the tie only when neither says code.
func (g *Generator) classifySymlinkExt(linkBase, targetBase string) extClass {
	own := g.classifyExt(linkBase)
	if targetBase == "" || isCodeClass(own) {
		return own
	}
	return g.classifyExt(targetBase)
}

// decideWalkEntry maps one observed entry to the single thing to do with it.
//
// Read it top to bottom: each helper owns one region of the state space and
// nothing outside it, so no two branches can decide the same axis differently.
// Every return is a named row in TestWalkDecisionTable, and
// TestWalkDecisionCrossProduct proves no reachable state escapes them.
func (g *Generator) decideWalkEntry(e walkEntry) walkDecision {
	if e.err != errNone {
		return decideWalkError(e)
	}
	if e.isRoot {
		return g.decideRoot(e)
	}
	// An ignored NAME beats symlink classification, but only for things that can
	// BE a directory. pnpm and yarn routinely store node_modules as a link: that
	// is the operator's configured ignore, not a capability blind spot, and
	// putting it in the fail-closed array makes a permanent unfixable entry out
	// of a directory they explicitly excluded. A regular FILE named `.git` (git
	// worktrees and submodules use one) is not an ignored directory -- calling it
	// one was untrue and spent recorder budget on every walk.
	if e.ignoredName && e.kind != kindFile {
		if e.kind == kindDir {
			return recordSelfPrune(SkipIgnoredDir)
		}
		// Never prune from a link: Walk documents that SkipDir from a
		// non-directory skips the remaining SIBLINGS, which would drop
		// main.go for having sorted after a vendor symlink.
		return recordSelf(SkipIgnoredDir)
	}
	switch e.kind {
	case kindDir:
		return descend()
	case kindSymlink:
		return decideSymlink(e)
	default:
		return decideFile(e)
	}
}

// decideWalkError owns every state where Walk handed the callback an error.
func decideWalkError(e walkEntry) walkDecision {
	// An error reaching the ROOT is total blindness, whichever error it is. The
	// caller turns a blame on the root into rootUnreadable -- the one condition
	// no path string can express, and the one every other signal reads clean
	// through: zero files indexed is a vacuous 100% coverage.
	//
	// This is the deliberate exception to "ENOENT records nothing". That rule
	// keeps transient, self-healing paths out of the fail-closed ARRAY;
	// rootUnreadable is a flag, not an entry, and a root that is not there is
	// not transient from the walk's point of view -- it examined nothing.
	if e.isRoot {
		return recordSelf(SkipUnreadableDir)
	}
	// The entry is GONE: nothing was hidden from us. Covers both the ordinary
	// mid-walk race (a git checkout, an editor temp file) and the permanent
	// dangling link -- `latest -> build-123` would otherwise block every change
	// in the repo until someone deleted it.
	if e.err == errGone {
		return doNothing()
	}
	// info != nil means the entry itself was statted and its READDIR failed: the
	// directory is the blind spot, and one entry covers everything beneath it
	// under the prefix rule.
	if e.infoPresent {
		return recordSelf(SkipUnreadableDir)
	}
	// info == nil: Walk's LSTAT of a CHILD failed, so the child's name is the
	// only evidence there is. A 0444 directory fails every child lstat, so probe
	// the parent DIRECTLY rather than inferring it from one child's failure --
	// and blame it on its own evidence.
	if e.parentUnreadable() {
		return recordParent(SkipUnreadableDir)
	}
	// Siblings are fine, so the parent is not the problem: one entry is, EIO or
	// ESTALE on a flaky mount. Blaming the parent here marked all of src/
	// unexamined while the walk indexed every sibling in it.
	//
	// The extension is decisive in exactly ONE direction -- a path that HAS a
	// non-code extension is a FILE, and a non-code file is not a symbol blind
	// spot. It can never tell you a directory is non-code, so everything else
	// falls through to a blind spot.
	if e.ext == extNonCode || e.ext == extDocument {
		return doNothing()
	}
	return recordSelf(SkipUnreadableFile)
}

// decideRoot owns the walk root, which is exempt from both the ignore rule and
// the symlink rule.
func (g *Generator) decideRoot(e walkEntry) walkDecision {
	switch e.kind {
	case kindDir:
		// The ignore rule exists to prune SUBdirectories. Applied to the
		// directory the operator named -- a repo that happens to be called
		// `vendor`, `dist`, `venv`, `testdata` or `node_modules` -- it pruned the
		// whole walk, indexed zero files and reported a vacuous 100% coverage.
		//
		// The ordinary root reaches the same answer, and it is a row of its own
		// rather than a fall-through to the non-root directory rule. Correct
		// behaviour arrived at by implicit reasoning is exactly the habit that
		// produced five review rounds.
		return descend()
	case kindSymlink:
		// The root is resolved before the walk, so it only ARRIVES as a link when
		// resolution failed. resolvedOrSame silently returns the path unchanged
		// in that case, so a dangling or unreadable root symlink walked one
		// entry, indexed nothing and recorded nothing.
		return recordSelf(SkipUnreadableDir)
	default:
		// A root that is a regular file: `runecho-ir` pointed at one file. It is
		// indexed, or reported unsupported, on exactly the same terms as any
		// other file. Named rather than inherited, for the reason above.
		return decideFile(e)
	}
}

// decideFile owns a regular file whose lstat succeeded.
func decideFile(e walkEntry) walkDecision {
	switch e.ext {
	case extSupported:
		return indexIt()
	case extKnownSource:
		// THE reason skip reporting exists: absent from the IR, therefore absent
		// from a diff, therefore a symbol deleted from it is invisible at exit 0
		// with empty stderr.
		return recordSelf(SkipUnsupportedLanguage)
	default:
		// A README, a .png, a Makefile. Not a blind spot in a symbol index, and
		// recording it blocks every documentation change.
		return doNothing()
	}
}

// decideSymlink owns a symlink whose name is not on the ignore list. Walk
// LSTATS, so info.IsDir() is always false here and the target needs an explicit
// probe. e.ext is classified from the TARGET's base name; see walkEntry.ext.
func decideSymlink(e walkEntry) walkDecision {
	switch e.target() {
	case linkGone:
		// Permanent, unlike a mid-walk race. See the errGone note above.
		return doNothing()
	case linkDir:
		// Not followed -- cycle and escape safety -- so the whole subtree is
		// unexamined under this path.
		return recordSelf(SkipSymlinkDir)
	case linkFile:
		if e.ext == extSupported || e.ext == extKnownSource {
			return recordSelf(SkipSymlink)
		}
		// A symlinked README is not a blind spot in a symbol index.
		return doNothing()
	default: // linkOpaque -- something IS there and we could not see it.
		switch e.ext {
		case extSupported, extKnownSource:
			// Calling a file a directory is simply false, and the reason string
			// is read by humans. Behaviourally identical under the prefix rule.
			return recordSelf(SkipUnreadableFile)
		case extDocument:
			// The ONLY class that is positive evidence of file-ness. A curated
			// table vouches for it, so dropping it cannot discard a subtree.
			return doNothing()
		default:
			// extNone AND extNonCode. Neither is evidence: an extensionless name
			// gives nothing away, and a dotted one is a directory as readily as a
			// file -- `deps.v2`, `python3.11`, `src.bak`, `pkg.d`. Reading
			// extNonCode as "a file we do not care about" dropped an entire
			// unexamined Go subtree at exit 0. Fail closed on capability.
			return recordSelf(SkipUnreadableDir)
		}
	}
}

// blameOutcome is what the recorder does with a decision's blame path, once the
// caller has resolved it to a repo-relative string.
type blameOutcome int

const (
	// blameOutcomeUnset -- the zero value is not an outcome, for the reason
	// walkAction's is not. See decideBlame.
	blameOutcomeUnset blameOutcome = iota
	// blameRootUnreadable -- set the rootUnreadable flag instead of emitting an
	// entry. There is no path string that expresses "all of it".
	blameRootUnreadable
	// blameRecord -- emit (rel, reason) into the skip list.
	blameRecord
)

// decideBlame is the SECOND pure table, and it exists because the first one did
// not remove the branch nest, it relocated half of it.
//
// decideWalkEntry says WHICH path is implicated. Turning that path into an entry
// -- resolving it repo-relative, and deciding that a blame at or above the root
// is the root's problem -- lived in the walk callback as an untested `switch` on
// one side and a silent `if rerr == nil` drop on the other. Two call paths
// disagreeing about the same state is the exact defect class the rewrite exists
// to eliminate, and it was surviving the rewrite untouched.
//
// Everything here fails CLOSED, because every failure mode is the same one: a
// blame the consumer cannot match. It intersects these entries against its own
// repo-relative changed paths, so an absolute entry, a `..`-relative one, or one
// carrying platform separators matches nothing at all -- a fail-open wearing a
// clean-looking payload. rootUnreadable is the honest report for every path
// shape that cannot be matched, and it is checked first by every consumer.
//
// rel is normalised HERE rather than assumed to have been normalised by the
// caller. FuzzDecideBlame found the difference on its first run: `"./"` -- the
// root written with a trailing slash -- passed every check below and was
// RECORDED, while its normalised form is the empty string and would not have
// been. The current caller cannot produce that value, which is exactly why the
// assumption was safe to make and impossible to notice; a future caller that
// resolves the blame differently would have reintroduced an unmatchable entry
// with no test in the way. normalizePath is pure and idempotent, so doing it
// twice costs nothing and removes the assumption.
func decideBlame(rel string, relErr bool) blameOutcome {
	if relErr {
		return blameRootUnreadable
	}
	rel = normalizePath(rel)
	// "." is the root itself. ".." and anything under it is ABOVE the root: the
	// parent of a child directly under the root IS the root, and filepath.Rel
	// happily returns ".." for anything further out, so a repo whose parent
	// directory was unreadable emitted {"Path": "..", "Reason": "unreadable_dir"}
	// with rootUnreadable false. ".." matches nothing under the prefix rule,
	// Coverage() reported a vacuous 100, and the whole tree read clean.
	if rel == "" || rel == "." || rel == ".." || strings.HasPrefix(rel, "../") {
		return blameRootUnreadable
	}
	// An absolute entry silently never matches. filepath.Rel cannot produce one,
	// which is exactly why nobody tested it -- and why a future caller that
	// resolves the blame differently would reintroduce the fail-open with no test
	// in the way.
	//
	// Deliberately NOT also rejecting a backslash. A backslash would mean
	// normalizePath's ToSlash did not run, which is a Windows-only concern -- and
	// on Linux a backslash is a perfectly legal filename character, so rejecting
	// it would set rootUnreadable for the WHOLE repo over one oddly named file.
	// That trades a theoretical fail-open for a certain false block, which is the
	// wrong direction on a gate whose zero-false-block property is the product.
	if strings.HasPrefix(rel, "/") {
		return blameRootUnreadable
	}
	return blameRecord
}
