package guard

import (
	"bytes"
	"regexp"
	"strings"
)

// #475: a TS interface, type-literal or abstract member signature
// (`findUser(id: string): Promise<User>;`) declares a member, it is not a call.
// The #431 retract only fires when a `{` body follows, so a signature ending in
// `;` (or in nothing) passed through as a call, and the IR holds such members
// qualified (`Repo.findUser`), so the bare name never resolved. jsFrames is the
// JS/TS counterpart of the Go scanner's inIface: it tracks which brace the scan
// is inside and reports the member-head names that are declarations.
//
// Fail-open by construction. Only a frame opened by an explicit `interface`,
// `class` or `type X =` head (or a `: {` annotation in a parameter list, a
// return type or an interface member) is a member frame; every other `{` is an
// ordinary block, so a method body, arrow body, object literal, `static {}` or
// `case x: {` keeps its calls checked. A hunk that starts inside an existing
// interface has no frame to read and keeps today's behaviour.

const (
	jsFrameType  = 'T' // interface body or type literal
	jsFrameClass = 'C' // class body
	jsFrameBlock = 'o' // any other `{`
	jsFrameGen   = 'g' // a `{` inside a class/interface/type head's type arguments
)

// jsSigWindow bounds how far a member head's name, modifiers and type
// parameters are searched for, so a pathological one-line file stays linear.
const jsSigWindow = 200

// jsHeadBufMax bounds the text kept since the last `{`, `}` or `;`, which is
// all classify reads.
const jsHeadBufMax = 400

var (
	reJSInterfaceHead = regexp.MustCompile(`(?:^|[^\w$.])interface\s+[A-Za-z_$][\w$]*[^{};=(]*$`)
	reJSTypeAliasHead = regexp.MustCompile(`(?:^|[^\w$.])type\s+[A-Za-z_$][\w$]*(?:<[^{};]*>)?\s*=\s*(?:[^;{}()]*[&|]\s*)?$`)
	reJSClassHead     = regexp.MustCompile(`(?:^|[^\w$.])class(?:\s+[A-Za-z_$][\w$]*)?(?:\s*<[^{};]*>)?(?:\s+(?:extends|implements)\b[^{;]*)?\s*$`)
	reJSHeadKeyword   = regexp.MustCompile(`(?:^|[^\w$.])(?:class|interface|type)\s+[A-Za-z_$][\w$]*`)
	// reJSTernaryQ finds a ternary `?` (not `m?(`, `x?:`, `m?<T>(` or `a?.b`).
	reJSTernaryQ = regexp.MustCompile(`\?(?:\s|[^:(<.?]|$)`)
	// reJSContinues matches a head that ends in a token awaiting an operand, so
	// the next line continues the statement instead of starting a member.
	reJSContinues = regexp.MustCompile(`(?:[=|&?:+\-*/%^!~.]|=>|(?:^|[^\w$.])(?:await|new|typeof|yield|delete|in|instanceof|as|satisfies|extends|keyof|return|throw|else|do))\s*$`)
	reJSCaseLabel = regexp.MustCompile(`^\s*(?:case|default)\b`)
	// reJSAbstractHead is a member head that is a declaration wherever it
	// appears: `abstract` methods never have a body, and `declare function` is
	// ambient. Neither needs the enclosing frame, which a hunk may not contain.
	reJSAbstractHead = regexp.MustCompile(`^\s*(?:(?:public|private|protected|static|override|readonly|declare|accessor)\s+)*abstract\s+(?:(?:async|get|set)\s+)*\*?\s*#?([A-Za-z_$][\w$]*)\s*(?:<[^()]*>)?\s*\(`)
	reJSDeclareFunc  = regexp.MustCompile(`^\s*(?:export\s+)?declare\s+(?:async\s+)?function\s*\*?\s*([A-Za-z_$][\w$]*)`)
)

// jsFrame is one entry of the stack. saved is the head text to restore when a
// jsFrameGen frame closes.
type jsFrame struct {
	kind  byte
	saved []byte
}

// jsFrames is the brace/paren stack carried across the lines of a contiguous
// run. Zero value is an empty stack; reset on a diff-hunk gap.
type jsFrames struct {
	stack []jsFrame
	head  []byte // text since the last `{`, `}` or `;`
}

func (f *jsFrames) reset() {
	f.stack = f.stack[:0]
	f.head = f.head[:0]
}

func (f *jsFrames) top() byte {
	if len(f.stack) == 0 {
		return 0
	}
	return f.stack[len(f.stack)-1].kind
}

func isMemberFrame(k byte) bool { return k == jsFrameType || k == jsFrameClass }

// openGeneric reports whether the head is a class/interface/type head whose
// `<` type-argument list is still open, so the `{` reached is a type literal
// inside it (`class A<T extends { id: string }> {`), not the body.
func openGeneric(h []byte) bool {
	return reJSHeadKeyword.Match(h) && bytes.Count(h, []byte("<")) > bytes.Count(h, []byte(">"))
}

// classify decides what kind of frame the `{` just reached opens, from the
// text since the last statement boundary.
func (f *jsFrames) classify() byte {
	h := f.head
	switch {
	case openGeneric(h):
		return jsFrameGen
	case reJSInterfaceHead.Match(h), reJSTypeAliasHead.Match(h):
		return jsFrameType
	case reJSClassHead.Match(h):
		return jsFrameClass
	}
	// An annotation `: {` is a type literal in a parameter list, after a
	// signature's `)` (a return type) and inside another type body. Elsewhere
	// it is an object literal or a block label (`case 1: {`), so those stay out,
	// as does a ternary's object branch (`c ? f() : {`).
	trimmed := bytes.TrimRight(h, " \t")
	if len(trimmed) > 0 && trimmed[len(trimmed)-1] == ':' && !reJSCaseLabel.Match(h) && !reJSTernaryQ.Match(h) {
		t := f.top()
		if t == '(' || t == jsFrameType {
			return jsFrameType
		}
		if rest := bytes.TrimRight(trimmed[:len(trimmed)-1], " \t"); len(rest) > 0 && rest[len(rest)-1] == ')' {
			return jsFrameType
		}
	}
	return jsFrameBlock
}

// atMemberBoundary reports whether a new line starts a member: nothing is
// pending, or what is pending does not end in a continuation token. A wrapped
// initializer (`x =` / `make();`) is not a member start.
func (f *jsFrames) atMemberBoundary() bool {
	t := bytes.TrimSpace(f.head)
	return len(t) == 0 || !reJSContinues.Match(t)
}

// scanLine advances the frame stack over one literal-stripped line and returns
// the offsets of the member-head names on it that are signatures. nil for none.
func (f *jsFrames) scanLine(scan string) map[int]struct{} {
	var heads map[int]struct{}
	add := func(off int) {
		if heads == nil {
			heads = make(map[int]struct{})
		}
		heads[off] = struct{}{}
	}
	if m := reJSAbstractHead.FindStringSubmatchIndex(scan); m != nil {
		add(m[2])
	} else if m := reJSDeclareFunc.FindStringSubmatchIndex(scan); m != nil {
		add(m[2])
	}
	// A `/` beside a brace may be a regex literal (`/\{/`), which the literal
	// stripping leaves in place and which would desync the stack for the rest of
	// the run. Drop the context instead: every later signature is then judged
	// as before this tracker existed.
	if strings.IndexByte(scan, '/') >= 0 && strings.ContainsAny(scan, "{}") {
		f.reset()
		return heads
	}
	// A member head is the first thing after a member boundary inside a member
	// frame: modifiers, the name, optional type parameters, then `(`.
	segment := func(at int) {
		if !isMemberFrame(f.top()) {
			return
		}
		end := min(len(scan), at+jsSigWindow)
		if m := reJSMethodHead.FindStringSubmatchIndex(scan[at:end]); m != nil {
			add(at + m[2])
		}
	}
	if f.atMemberBoundary() {
		segment(0)
	}
	for i := 0; i < len(scan); i++ {
		c := scan[i]
		switch c {
		case '{':
			k := f.classify()
			var saved []byte
			if k == jsFrameGen {
				saved = append([]byte(nil), f.head...)
			}
			f.stack = append(f.stack, jsFrame{kind: k, saved: saved})
			f.head = f.head[:0]
			segment(i + 1)
			continue
		case '}':
			f.closeBrace()
			segment(i + 1)
			continue
		case '(', '[':
			f.stack = append(f.stack, jsFrame{kind: c})
		case ')':
			f.popTo('(')
		case ']':
			f.popTo('[')
		case ';':
			f.head = f.head[:0]
			segment(i + 1)
			continue
		case ',':
			if isMemberFrame(f.top()) {
				f.head = f.head[:0]
				segment(i + 1)
				continue
			}
		}
		if len(f.head) >= jsHeadBufMax {
			n := copy(f.head, f.head[len(f.head)-jsHeadBufMax/2:])
			f.head = f.head[:n]
		}
		f.head = append(f.head, c)
	}
	return heads
}

// closeBrace pops the innermost brace frame. Closing a type-argument literal
// restores the head it interrupted (the literal collapsing to `_`), so `class A<T extends {…}> {` still reads
// as a class head; any other `}` starts a fresh statement.
func (f *jsFrames) closeBrace() {
	for i := len(f.stack) - 1; i >= 0; i-- {
		k := f.stack[i]
		if k.kind == '(' || k.kind == '[' {
			continue
		}
		f.stack = f.stack[:i]
		if k.kind == jsFrameGen {
			f.head = append(append(f.head[:0], k.saved...), '_')
		} else {
			f.head = f.head[:0]
		}
		return
	}
	f.head = f.head[:0]
}

// popTo pops the innermost paren or bracket frame. A closer with no opener on
// the stack is a stray (the run began mid-block) and is ignored.
func (f *jsFrames) popTo(open byte) {
	for i := len(f.stack) - 1; i >= 0; i-- {
		if f.stack[i].kind == open {
			f.stack = f.stack[:i]
			return
		}
	}
}
