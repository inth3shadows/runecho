package guard

import "regexp"

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
	reJSCaseLabel     = regexp.MustCompile(`^\s*(?:case|default)\b`)
	// reJSAbstractHead is a member head that is a declaration wherever it
	// appears: `abstract` methods never have a body, and `declare function` is
	// ambient. Neither needs the enclosing frame, which a hunk may not contain.
	reJSAbstractHead = regexp.MustCompile(`^\s*(?:(?:public|private|protected|static|override|readonly|declare|accessor)\s+)*abstract\s+(?:(?:async|get|set)\s+)*\*?\s*#?([A-Za-z_$][\w$]*)\s*(?:<[^()]*>)?\s*\(`)
	reJSDeclareFunc  = regexp.MustCompile(`^\s*(?:export\s+)?declare\s+(?:async\s+)?function\s*\*?\s*([A-Za-z_$][\w$]*)`)
)

// jsFrames is the brace/paren stack carried across the lines of a contiguous
// run. Zero value is an empty stack; reset on a diff-hunk gap.
type jsFrames struct {
	stack []byte
	head  string // text since the last `{`, `}` or `;`
}

func (f *jsFrames) reset() {
	f.stack = f.stack[:0]
	f.head = ""
}

func (f *jsFrames) top() byte {
	if len(f.stack) == 0 {
		return 0
	}
	return f.stack[len(f.stack)-1]
}

func isMemberFrame(k byte) bool { return k == jsFrameType || k == jsFrameClass }

// classify decides what kind of frame the `{` just reached opens, from the
// text since the last statement boundary.
func (f *jsFrames) classify() byte {
	h := f.head
	switch {
	case reJSInterfaceHead.MatchString(h), reJSTypeAliasHead.MatchString(h):
		return jsFrameType
	case reJSClassHead.MatchString(h):
		return jsFrameClass
	}
	// An annotation `: {` is a type literal in a parameter list, after a
	// signature's `)` (a return type) and inside another type body. Elsewhere
	// it is an object literal or a block label (`case 1: {`), so those stay out.
	trimmed := trimRightSpace(h)
	if len(trimmed) > 0 && trimmed[len(trimmed)-1] == ':' && !reJSCaseLabel.MatchString(h) {
		t := f.top()
		if t == '(' || t == jsFrameType {
			return jsFrameType
		}
		if rest := trimRightSpace(trimmed[:len(trimmed)-1]); len(rest) > 0 && rest[len(rest)-1] == ')' {
			return jsFrameType
		}
	}
	return jsFrameBlock
}

func trimRightSpace(s string) string {
	for len(s) > 0 && (s[len(s)-1] == ' ' || s[len(s)-1] == '\t') {
		s = s[:len(s)-1]
	}
	return s
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
	segment(0)
	for i := 0; i < len(scan); i++ {
		c := scan[i]
		switch c {
		case '{':
			f.stack = append(f.stack, f.classify())
			f.head = ""
			segment(i + 1)
			continue
		case '}':
			f.popTo('{')
			f.head = ""
			segment(i + 1)
			continue
		case '(', '[':
			f.stack = append(f.stack, c)
		case ')':
			f.popTo('(')
		case ']':
			f.popTo('[')
		case ';':
			f.head = ""
			segment(i + 1)
			continue
		case ',':
			segment(i + 1)
		}
		if len(f.head) >= jsHeadBufMax {
			f.head = f.head[len(f.head)-jsHeadBufMax/2:]
		}
		f.head += string(c)
	}
	return heads
}

// popTo pops the innermost frame that closes with the given opener (`{` stands
// for every brace kind). A closer with no opener on the stack is a stray (the
// run began mid-block) and is ignored.
func (f *jsFrames) popTo(open byte) {
	for i := len(f.stack) - 1; i >= 0; i-- {
		k := f.stack[i]
		if k == open || (open == '{' && (k == jsFrameType || k == jsFrameClass || k == jsFrameBlock)) {
			f.stack = f.stack[:i]
			return
		}
	}
}
