package guard

import "regexp"

// TypeScript member signatures are declarations, not calls (#475):
// `findUser(id: string): Promise<User>;` in an interface, `abstract render():
// void;` in a class. The IR stores them qualified (`Repo.findUser`), so the
// bare name is unknown and, read as a call, was flagged.
//
// Only shapes that announce themselves by keyword are recognised:
//   - the body of an `interface`, and of a block `declare` (`declare module`,
//     `declare global`, `declare namespace`, `declare class`, ...). Nothing in
//     such a body runs, so no name in it is a call;
//   - a one-line `declare function/const/let/var/type` statement;
//   - the name of an `abstract` member.
//
// Class bodies and type literals (`type T = { m(): void }`) are deliberately
// not recognised. Telling a member signature from a call there needs a brace
// stack that knows object literals, ternaries, regex literals and wrapped
// initializers; two line-heuristic versions of it hid real calls (review of
// #563). The frame here is only entered from a keyword head and only counts
// braces inside an ambient body, where none of those can occur, so it cannot
// desync on ordinary code.

// reTSInterfaceHead starts an interface declaration.
var reTSInterfaceHead = regexp.MustCompile(`^\s*(?:export\s+)?(?:default\s+)?(?:declare\s+)?interface\s+[A-Za-z_$]`)

// reTSDeclareBlock starts an ambient declaration that has a body.
var reTSDeclareBlock = regexp.MustCompile(`^\s*(?:export\s+)?declare\s+(?:global\b|module\b|namespace\b|class\b|abstract\s+class\b|enum\b|const\s+enum\b)`)

// reTSDeclareLine is an ambient declaration without a body: the whole line is
// a type.
var reTSDeclareLine = regexp.MustCompile(`^\s*(?:export\s+)?declare\s+(?:function|const|let|var|type)\b`)

// reTSAbstractMember is an abstract member; group 1 is its name.
var reTSAbstractMember = regexp.MustCompile(`^\s*(?:(?:public|protected|private|static|readonly|override|async)\s+)*abstract\s+(?:(?:async|readonly|override|get|set)\s+)*([A-Za-z_$][\w$]*)\s*[?!]?\s*[<(:;]`)

// jsSigHeadMaxLines bounds how far below an interface or declare head its
// body `{` may open. A head that never opens one is forgotten.
const jsSigHeadMaxLines = 8

// jsFrames tracks whether a line is inside an interface or ambient body.
type jsFrames struct {
	depth   int // brace depth inside the body; 0 outside
	pending int // lines left for a head's body `{` to open; 0 = no head
	angle   int // `<...>` depth in a pending head, whose `{` are constraint types
}

func (f *jsFrames) reset() { *f = jsFrames{} }

// scanLine reads one masked line (strings and comments blanked) and returns
// the byte offsets of the identifiers on it that are declarations, not calls.
func (f *jsFrames) scanLine(scan string) map[int]struct{} {
	var out map[int]struct{}
	mark := func(i int) {
		if out == nil {
			out = make(map[int]struct{})
		}
		out[i] = struct{}{}
	}
	whole := false
	if f.depth == 0 && f.pending == 0 {
		switch {
		case reTSInterfaceHead.MatchString(scan), reTSDeclareBlock.MatchString(scan):
			f.pending, f.angle = jsSigHeadMaxLines, 0
		case reTSDeclareLine.MatchString(scan):
			whole = true
		default:
			if m := reTSAbstractMember.FindStringSubmatchIndex(scan); m != nil {
				mark(m[2])
			}
			return out
		}
	}
	for i := 0; i < len(scan); i++ {
		c := scan[i]
		if (whole || f.depth > 0 || f.pending > 0) && jsSigIdentStart(c) &&
			(i == 0 || !jsSigIdentByte(scan[i-1])) {
			mark(i)
		}
		switch {
		case f.depth > 0:
			switch c {
			case '{':
				f.depth++
			case '}':
				f.depth--
			}
		case f.pending > 0:
			switch {
			case c == '<':
				f.angle++
			case c == '>' && f.angle > 0 && (i == 0 || scan[i-1] != '='):
				f.angle--
			case c == '{' && f.angle == 0:
				f.depth, f.pending = 1, 0
			case c == ';' && f.angle == 0:
				f.pending = 0 // no body after all
			}
		}
	}
	if f.depth == 0 && f.pending > 0 {
		f.pending--
	}
	return out
}

func jsSigIdentStart(c byte) bool {
	return c == '_' || c == '$' || (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || c >= 0x80
}

func jsSigIdentByte(c byte) bool {
	return jsSigIdentStart(c) || (c >= '0' && c <= '9')
}
