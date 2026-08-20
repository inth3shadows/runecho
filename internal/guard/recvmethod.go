package guard

import "regexp"

// Receiver-method check (Go): a method body calling a sibling method that does
// not exist on its own receiver type.
//
// WHY THIS SHAPE. The compiler-oracle differential (resolve_differential_test.go)
// measured the guard's reach over every way Go can reference a name the compiler
// must resolve. Three shapes were fully covered; `v.Method()` was 0/60 on two
// independent corpora — nothing saw it. It is also the shape a model hallucinates
// most: an invented method on a real receiver reads as plausible at every level
// except the type's actual method set.
//
// WHY ONLY THE RECEIVER SLICE. Resolving `v.Method()` in general needs v's type,
// which no check has and which a 12ms hook budget cannot afford. But one case
// needs no inference at all: inside `func (r *T) M()`, the type of `r` is written
// on the line that opens the method. That makes the receiver variable the single
// value in Go whose type is known lexically, so it is the one slice of this shape
// that can be checked without a type checker.
//
// THE GATES, and what each is protecting against. Every one of them costs recall
// and exists because the alternative is a false positive, which this guard treats
// as far more expensive than a miss:
//
//  1. The receiver variable must be unambiguous file-wide. Two methods in one
//     file using `r` for different types make `r.Foo` unresolvable; abstain.
//  2. The variable must never be re-bound in the file (`r :=`, `var r`). A
//     rebinding elsewhere means the name at the call site may not be the receiver.
//  3. (LIFTED) This gate previously required an exported method name, because the
//     IR indexed only exported methods and an unexported one was absent for
//     reasons that said nothing about existence. The Go parser now records
//     unexported methods under the "unexported" kind, so the gate is gone. It was
//     the binding constraint: a population probe put this check's ceiling at 0.5%
//     of selector sites with it in place, against 3.2% without, because
//     sibling-method calls are usually to unexported helpers.
//  4. The receiver type must already have at least one indexed member (method or
//     field). Zero means the parser never recorded a member set for T, so absence
//     proves nothing.
//  5. The method name must not exist ANYWHERE in the known set — not on another
//     type, not as a function. This is the embedding backstop: a promoted method
//     from an embedded field is not indexed under T, so requiring the name to be
//     globally unknown keeps the common embedding case from firing.
//
// Gate 5 is deliberately blunt, and it is what makes this check precision-first
// rather than recall-first: it fires only on a name the repository has never seen
// in any form, which is exactly the hallucination profile and almost never a
// real-but-unindexed method.
//
// KNOWN RESIDUAL. A type embedding a stdlib type whose promoted method name
// appears nowhere else in the repo (`sync.Mutex` → `r.Lock()` in a repo that
// never otherwise says Lock) still slips past gate 5. Measured rather than
// assumed: see the differential's phase 1 over foreign Go.

// reGoMethodRecv matches a method declaration's receiver: `func (r *Reader) …`,
// `func (r Reader) …`, `func (s *Set[T]) …`. The type name capture stops at the
// first non-word byte, which strips the generic instantiation exactly as
// parser.receiverTypeName does — so the name here matches the name the IR
// recorded.
var reGoMethodRecv = regexp.MustCompile(`^\s*func\s*\(\s*([A-Za-z_]\w*)\s+\*?\s*([A-Za-z_]\w*)`)

// reGoVarBinding matches a `var` binding. The `:=` forms are handled by
// goShortDeclNames instead of a regex: the pattern this replaced rooted its match
// at the FIRST identifier of the left-hand side and consumed the rest with a
// greedy class, so `err, r := f()` never disqualified `r` — gate 2 had a hole for
// any tuple where the receiver name was not first.
var reGoVarBinding = regexp.MustCompile(`(?:^|[^\w.])var\s+([A-Za-z_]\w*)\b`)

// goReceiverTypes maps a receiver variable name to the type it receives, for the
// names that are unambiguous and never re-bound in ctx. A name that fails either
// condition is absent from the result, which makes every consumer abstain on it.
//
// The second return is exactly those dropped names — every name ctx binds as a
// method receiver that gates 1/2 removed. A call on one of them is a candidate
// this check declined (#359's "ambiguous-receiver"), which is a different thing
// from a call on a name that was never a receiver at all and so was never this
// check's shape.
func goReceiverTypes(ctx []AddedLine) (map[string]string, map[string]struct{}) {
	types := make(map[string]string)
	ambiguous := make(map[string]struct{})
	receivers := make(map[string]struct{})
	open := ""
	for _, l := range ctx {
		scan, newOpen := stripLiteralsStateful(LangGo, l.Text, open)
		open = newOpen
		m := reGoMethodRecv.FindStringSubmatch(scan)
		if m == nil {
			continue
		}
		recv, typ := m[1], m[2]
		if recv == "_" {
			continue
		}
		receivers[recv] = struct{}{}
		if prev, seen := types[recv]; seen && prev != typ {
			ambiguous[recv] = struct{}{}
			continue
		}
		types[recv] = typ
	}
	if len(types) == 0 {
		return nil, nil
	}
	// Second pass for rebinding. It runs over the same ctx rather than being
	// folded into the loop above because a rebinding can appear before the method
	// declaration that introduces the name.
	open = ""
	for _, l := range ctx {
		scan, newOpen := stripLiteralsStateful(LangGo, l.Text, open)
		open = newOpen
		for _, name := range goShortDeclNames(scan) {
			ambiguous[name] = struct{}{}
		}
		for _, m := range reGoVarBinding.FindAllStringSubmatch(scan, -1) {
			ambiguous[m[1]] = struct{}{}
		}
	}
	for name := range ambiguous {
		delete(types, name)
	}
	dropped := make(map[string]struct{})
	for name := range receivers {
		if _, kept := types[name]; !kept {
			dropped[name] = struct{}{}
		}
	}
	if len(types) == 0 {
		return nil, dropped
	}
	return types, dropped
}

// goTypesWithMethods returns the set of receiver type names that have at least
// one QUALIFIED entry in known — a method or a field. known holds both as
// "Type.member" (parser/go.go qualifies them), so the prefix before the first
// '.' is the type.
//
// Fields count deliberately. Gate 4 asks "did the parser record a member set for
// T", and a struct with fields and no methods has a recorded member set; the
// call being judged is `r.name(...)`, which a func-typed field satisfies just as
// well as a method. Requiring a method specifically would abstain on precisely
// the types whose members are all func-typed fields.
func goTypesWithMembers(known map[string]struct{}) map[string]struct{} {
	out := make(map[string]struct{})
	for name := range known {
		for i := 0; i < len(name); i++ {
			if name[i] == '.' {
				if i > 0 {
					out[name[:i]] = struct{}{}
				}
				break
			}
		}
	}
	return out
}

// GoReceiverMethodViolations reports calls in addedLines of the form
// `r.Method(...)` where r is a method receiver of a repo type T and T has no
// indexed method Method. wholeFile is the current file (pre-edit in hook mode);
// known is the flat repo symbol set. Go only; returns nil when nothing in the
// file binds a usable receiver.
//
// See the file header for the gate stack and what each gate is protecting.
//
// Thin wrapper over GoReceiverMethodViolationsWithReason, discarding the
// abstain reason — kept so every existing caller is untouched by #359.
func GoReceiverMethodViolations(wholeFile, addedLines []AddedLine, known map[string]struct{}) []Violation {
	vs, _ := GoReceiverMethodViolationsWithReason(wholeFile, addedLines, known)
	return vs
}

// GoReceiverMethodViolationsWithReason is GoReceiverMethodViolations plus the
// reason a candidate `r.Method(` call was declined, where r is (or was) a
// method receiver in this file — the only calls this check ever adjudicates:
//
//   - "ambiguous-receiver" — gates 1/2: r names a receiver, but of two
//     different types, or it is re-bound elsewhere in the file.
//   - "no-indexed-members" — gate 4: the receiver type has no member recorded
//     in the index at all, so absence of this method proves nothing. Common on
//     an edit that adds the type and the method together, since the known set
//     is built from the PRE-edit file — which is why it is registered as a gate
//     reason rather than a degraded one (cmd/runecho-guard/checkresult.go).
//   - "name-known-elsewhere" — gate 5, either half: the name exists in the repo
//     as some other symbol or as another type's method, so it could be promoted
//     from an embedded field.
//
// Empty when the check ran to completion, and empty for every selector that is
// not a receiver call (`fmt.Println`, `x.field.Method()`, a local variable) —
// those were never this check's shape, and reporting them would make the reason
// fire on nearly every Go edit while saying nothing about coverage.
//
// First reason encountered wins, matching GoDepQualifiedViolationsWithReason.
func GoReceiverMethodViolationsWithReason(wholeFile, addedLines []AddedLine, known map[string]struct{}) ([]Violation, string) {
	// Both slices feed the binding scan for the same reason GoQualifiedViolations
	// concatenates them: a receiver declaration or a rebinding introduced by THIS
	// edit has to be seen, or the check judges the call against a stale file.
	ctx := make([]AddedLine, 0, len(wholeFile)+len(addedLines))
	ctx = append(ctx, wholeFile...)
	ctx = append(ctx, addedLines...)

	recvTypes, droppedRecvs := goReceiverTypes(ctx)
	if len(recvTypes) == 0 && len(droppedRecvs) == 0 {
		return nil, ""
	}
	// Only when a usable receiver survived: with recvTypes empty every call
	// below stops at the recvTypes lookup, so this map would be built and never
	// read. It is O(|known|) over the whole repo symbol set — ~1-2 ms on a 30k
	// symbol index against a ~12 ms hook budget — and the all-dropped case is
	// not exotic, since a single-letter receiver (`s`, `r`, `c`) collides with a
	// `:=` binding of the same name constantly. Measured by adversarial review
	// of #359, which is what made the removed early return affordable.
	var typesWithMembers map[string]struct{}
	if len(recvTypes) > 0 {
		typesWithMembers = goTypesWithMembers(known)
	}

	var violations []Violation
	var reason string
	seen := make(map[string]struct{})
	open := ""
	prevNo := 0
	for i, l := range addedLines {
		if i == 0 || l.LineNo != prevNo+1 {
			open = ""
		}
		prevNo = l.LineNo
		if open == "" && isCommentLine(LangGo, l.Text) {
			continue
		}
		scan, newOpen := stripLiteralsStateful(LangGo, l.Text, open)
		open = newOpen
		for _, idx := range reGoQualifiedCall.FindAllStringSubmatchIndex(scan, -1) {
			qStart, qEnd := idx[2], idx[3]
			symStart, symEnd := idx[4], idx[5]
			q := scan[qStart:qEnd]
			sym := scan[symStart:symEnd]
			// Left-guard exactly as the qualified check does: a preceding '.' or
			// word byte means this is a deeper selector (`a.r.Foo`), where `r` is a
			// field rather than the receiver variable.
			if qStart > 0 {
				if prev := scan[qStart-1]; prev == '.' || isWordByte(prev) {
					continue
				}
			}
			typ, ok := recvTypes[q]
			if !ok {
				// gate 1/2: not an unambiguous, never-rebound receiver. Only a
				// name this file DOES bind as a receiver is a declined
				// candidate; any other qualifier is out of shape entirely.
				if _, wasRecv := droppedRecvs[q]; wasRecv && reason == "" {
					reason = "ambiguous-receiver"
				}
				continue
			}
			if _, ok := typesWithMembers[typ]; !ok {
				// gate 4: T has no indexed member set to argue from.
				if reason == "" {
					reason = "no-indexed-members"
				}
				continue
			}
			if _, ok := known[typ+"."+sym]; ok {
				continue // the method exists on this type
			}
			if _, ok := known[sym]; ok {
				// gate 5: the name exists somewhere — could be promoted.
				if reason == "" {
					reason = "name-known-elsewhere"
				}
				continue
			}
			// Gate 5, second half: the name is not a method of any other type
			// either. A name the repository has never used in any form is the
			// hallucination profile; anything else is abstained on.
			if goNameUsedAsAnyMethod(known, sym) {
				if reason == "" {
					reason = "name-known-elsewhere"
				}
				continue
			}
			key := typ + "." + sym
			if _, dup := seen[key]; dup {
				continue
			}
			seen[key] = struct{}{}
			suggestions, _ := Suggest(sym, methodNamesOf(known, typ))
			violations = append(violations, Violation{
				Line:        l.LineNo,
				Symbol:      key,
				Lang:        LangGo,
				Suggestions: suggestions,
			})
		}
	}
	return violations, reason
}

// goNameUsedAsAnyMethod reports whether sym appears as the method half of any
// "Type.Method" entry in known.
func goNameUsedAsAnyMethod(known map[string]struct{}, sym string) bool {
	for name := range known {
		for i := 0; i < len(name); i++ {
			if name[i] == '.' {
				if name[i+1:] == sym {
					return true
				}
				break
			}
		}
	}
	return false
}

// methodNamesOf returns typ's indexed method names, unqualified, as the
// suggestion pool. Suggesting against the whole repo symbol set would offer
// names that are not callable on this receiver at all.
func methodNamesOf(known map[string]struct{}, typ string) map[string]struct{} {
	out := make(map[string]struct{})
	prefix := typ + "."
	for name := range known {
		if len(name) > len(prefix) && name[:len(prefix)] == prefix {
			out[name[len(prefix):]] = struct{}{}
		}
	}
	return out
}
