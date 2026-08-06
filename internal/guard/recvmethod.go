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
//  4. The receiver type must already have at least one indexed method. Zero means
//     the parser never recorded a method set for T, so absence proves nothing.
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

// reGoRebind matches a short variable declaration or `var` binding, used to
// disqualify a receiver name that is re-bound anywhere in the file. The `:=`
// alternative allows a multi-assign tail (`r, err := …`) because binding r in a
// tuple rebinds it just as surely as binding it alone.
var reGoRebind = regexp.MustCompile(`(^|[^\w.])([A-Za-z_]\w*)\s*(?:,[^=\n]*)?:=|(?:^|[^\w.])var\s+([A-Za-z_]\w*)\b`)

// goReceiverTypes maps a receiver variable name to the type it receives, for the
// names that are unambiguous and never re-bound in ctx. A name that fails either
// condition is absent from the result, which makes every consumer abstain on it.
func goReceiverTypes(ctx []AddedLine) map[string]string {
	types := make(map[string]string)
	ambiguous := make(map[string]struct{})
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
		if prev, seen := types[recv]; seen && prev != typ {
			ambiguous[recv] = struct{}{}
			continue
		}
		types[recv] = typ
	}
	if len(types) == 0 {
		return nil
	}
	// Second pass for rebinding. It runs over the same ctx rather than being
	// folded into the loop above because a rebinding can appear before the method
	// declaration that introduces the name.
	open = ""
	for _, l := range ctx {
		scan, newOpen := stripLiteralsStateful(LangGo, l.Text, open)
		open = newOpen
		for _, m := range reGoRebind.FindAllStringSubmatch(scan, -1) {
			for _, name := range []string{m[2], m[3]} {
				if name != "" {
					ambiguous[name] = struct{}{}
				}
			}
		}
	}
	for name := range ambiguous {
		delete(types, name)
	}
	if len(types) == 0 {
		return nil
	}
	return types
}

// goTypesWithMethods returns the set of receiver type names that have at least
// one method in known. known holds methods as "Type.Method" (parser/go.go
// qualifies them), so the prefix before the first '.' is the type.
func goTypesWithMethods(known map[string]struct{}) map[string]struct{} {
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
func GoReceiverMethodViolations(wholeFile, addedLines []AddedLine, known map[string]struct{}) []Violation {
	// Both slices feed the binding scan for the same reason GoQualifiedViolations
	// concatenates them: a receiver declaration or a rebinding introduced by THIS
	// edit has to be seen, or the check judges the call against a stale file.
	ctx := make([]AddedLine, 0, len(wholeFile)+len(addedLines))
	ctx = append(ctx, wholeFile...)
	ctx = append(ctx, addedLines...)

	recvTypes := goReceiverTypes(ctx)
	if len(recvTypes) == 0 {
		return nil
	}
	typesWithMethods := goTypesWithMethods(known)

	var violations []Violation
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
				continue // gate 1/2: not an unambiguous, never-rebound receiver
			}
			if _, ok := typesWithMethods[typ]; !ok {
				continue // gate 4: T has no indexed method set to argue from
			}
			if _, ok := known[typ+"."+sym]; ok {
				continue // the method exists on this type
			}
			if _, ok := known[sym]; ok {
				continue // gate 5: the name exists somewhere — could be promoted
			}
			// Gate 5, second half: the name is not a method of any other type
			// either. A name the repository has never used in any form is the
			// hallucination profile; anything else is abstained on.
			if goNameUsedAsAnyMethod(known, sym) {
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
	return violations
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
