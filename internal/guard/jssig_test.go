package guard

import "testing"

// #475: a TS interface, abstract or ambient (declare) method signature declares a
// member; it is not a call. The IR stores these qualified (`Repo.findUser`), so
// the bare name is unknown and, before the fix, every signature that ended in
// `;` was flagged. The issue's exact repro, end to end.
func TestRun_TSSignatures_Issue475Repro(t *testing.T) {
	src := []string{
		`import { User } from "./seed";`,
		`export interface Repo {`,
		`  findUser(id: string): Promise<User>;`,
		`  save(u: User): void;`,
		`}`,
		`export abstract class Base {`,
		`  abstract render(): void;`,
		`  protected abstract load(id: string): Promise<void>;`,
		`}`,
	}
	diffs := []FileDiff{{Path: "web/repo.ts", AddedLines: lines(src...)}}
	v := Run(map[string]struct{}{"User": {}, "Repo": {}, "Base": {}}, "", diffs)
	if names := violationSymbols(v); len(names) != 0 {
		t.Errorf("signatures must not be flagged, got %v", names)
	}
}

// Declaration shapes that must yield no ref at all.
func TestExtractRefs_TS_SignaturesSkipped(t *testing.T) {
	for _, tc := range []struct {
		name string
		src  []string
		nope []string
	}{
		{"interface", []string{`interface Repo {`, `  findUser(id: string): Promise<User>;`, `  save(u: User): void`, `}`}, []string{"findUser", "save"}},
		{"export interface generic extends", []string{`export interface Repo<T extends Base> extends Store<T>, Other {`, `  get(id: string): T;`, `  put(t: T): void;`, `}`}, []string{"get", "put"}},
		{"multi-line interface head", []string{`export interface Repo<T>`, `  extends Store<T> {`, `  fetchAll(): T[];`, `}`}, []string{"fetchAll"}},
		{"optional method", []string{`interface P {`, `  onClose?(why: string): void;`, `  retry?<T>(x: T): T;`, `}`}, []string{"onClose", "retry"}},
		{"call and construct signatures", []string{`interface F {`, `  (x: string): number;`, `  new (x: string): F;`, `  <T>(x: T): T;`, `  name(): string;`, `}`}, []string{"name", "new"}},
		{"readonly getter setter", []string{`interface G {`, `  get size(): number;`, `  set size(v: number);`, `}`}, []string{"size"}},
		{"multi-line signature", []string{`interface H {`, `  find(`, `    id: string,`, `    opts: Opts,`, `  ): Promise<User>;`, `}`}, []string{"find"}},
		{"comma separated members", []string{`interface C {`, `  a(): void,`, `  b(): void,`, `}`}, []string{"a", "b"}},
		{"nested literal in interface", []string{`interface N {`, `  inner: {`, `    deep(): void;`, `  };`, `}`}, []string{"deep"}},
		{"abstract methods", []string{`export abstract class Base {`, `  abstract render(): void;`, `  protected abstract load(id: string): Promise<void>;`, `  public abstract async run(): Promise<void>;`, `  abstract get name(): string;`, `}`}, []string{"render", "load", "run", "name"}},
		{"abstract without class line", []string{`  protected abstract load(id: string): Promise<void>;`}, []string{"load"}},
		{"declare module", []string{`declare module "x" {`, `  export function f(a: string): void;`, `  export class K { m(): void; }`, `}`}, []string{"f", "m"}},
		{"declare global nested interface", []string{`declare global {`, `  interface Window {`, `    track(e: string): void;`, `  }`, `}`}, []string{"track"}},
		{"declare namespace", []string{`export declare namespace N {`, `  function g(): void;`, `}`}, []string{"g"}},
		{"declare const", []string{`declare const h: (x: number) => Thing;`}, []string{"h"}},
		{"one-line interface", []string{`interface I { m(): void; n(x: T): U }`}, []string{"m", "n"}},
		{"interface generic default arrow", []string{`interface E<T = () => void> {`, `  m(): T;`, `}`}, []string{"m"}},
		{"interface generic constraint literal", []string{`interface R<T extends { id: string }> {`, `  m(): void;`, `}`}, []string{"m"}},
		{"abstract property and member", []string{`abstract class A {`, `  abstract readonly size: number;`, `  abstract m?(): void;`, `}`}, []string{"m"}},
		{"declare class", []string{`declare class D {`, `  m(): void;`, `  static s(x: number): D;`, `}`}, []string{"m", "s"}},
		{"declare function", []string{`declare function f(): void;`, `export declare function g(x: number): string;`}, []string{"f", "g"}},
		{"newline terminated no semicolon", []string{`interface Q {`, `  a(): void`, `  b(x: number): string`, `}`}, []string{"a", "b"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			refs := ExtractRefs(LangJS, lines(tc.src...))
			if !containsNone(refs, tc.nope...) {
				t.Errorf("signature names must not be refs, got %v", refNames(refs))
			}
		})
	}
}

// The other half: recognising signatures must not blind the guard to calls in
// real code, including code that follows or sits beside a signature body.
func TestExtractRefs_TS_RealCallsStillFlagged(t *testing.T) {
	for _, tc := range []struct {
		name string
		src  []string
		want []string
	}{
		{"class method body", []string{`class A {`, `  run() {`, `    ghostCall();`, `  }`, `}`}, []string{"ghostCall"}},
		{"class method body one line", []string{`class A {`, `  run() { ghostOne(); }`, `}`}, []string{"ghostOne"}},
		{"arrow body", []string{`const f = () => {`, `  ghostArrow();`, `};`}, []string{"ghostArrow"}},
		{"arrow in class property", []string{`class A {`, `  h = () => {`, `    ghostProp();`, `  };`, `}`}, []string{"ghostProp"}},
		{"top level", []string{`ghostTop();`}, []string{"ghostTop"}},
		{"after interface", []string{`interface R {`, `  m(): void;`, `}`, `ghostAfter();`}, []string{"ghostAfter"}},
		{"object literal method body", []string{`const o = {`, `  run() {`, `    obj.method();`, `    ghostObj();`, `  },`, `};`}, []string{"ghostObj"}},
		{"object literal method body, bare call", []string{`const o = {`, `  run() {`, `    ghostInObj();`, `  },`, `};`}, []string{"ghostInObj"}},
		{"case block", []string{`switch (x) {`, `  case 1: {`, `    ghostCase();`, `  }`, `}`}, []string{"ghostCase"}},
		{"case call block", []string{`switch (x) {`, `  case f(): {`, `    ghostCase2();`, `  }`, `}`}, []string{"ghostCase2"}},
		{"static block", []string{`class A {`, `  static {`, `    ghostStatic();`, `  }`, `}`}, []string{"ghostStatic"}},
		{"call after class", []string{`class A {`, `  abstractish(): void;`, `}`, `abstractish();`}, []string{"abstractish"}},
		{"params of signature body method", []string{`interface I {`, `  m(): void;`, `}`, `function k() {`, `  ghostFn();`, `}`}, []string{"ghostFn"}},
		{"R3 shorthand ambient module without semicolon", []string{`declare module "*.svg"`, `function g() {`, `  ghostA();`, `}`, `ghostB();`}, []string{"ghostA", "ghostB"}},
		{"R3 two shorthand modules", []string{`declare module "*.svg"`, `declare module "*.png"`, `export const icon = makeIcon()`}, []string{"makeIcon"}},
		{"R3 code after a declare line's semicolon", []string{`declare const x: number; ghostE();`}, []string{"ghostE"}},
		{"R3 interface signature name called later", []string{`interface I {`, `  run(): void;`, `}`, `run();`}, []string{"run"}},
		{"after declare module", []string{`declare module "x" {`, `  export function f(): void;`, `}`, `ghostAfterDecl();`}, []string{"ghostAfterDecl"}},
		{"declare function without semicolon", []string{`declare function f(): void`, `function g() {`, `  ghostG();`, `}`}, []string{"ghostG"}},
		{"after one-line interface", []string{`interface I { m(): void }`, `ghostAfterOne();`}, []string{"ghostAfterOne"}},
		{"interface word in a call", []string{`const interface_ = 1;`, `makeInterface({`, `  a: 1,`, `});`, `ghostIf();`}, []string{"makeInterface", "ghostIf"}},
		{"class body calls", []string{`class S {`, `  x = build(1);`, `  m() { return ghostM(); }`, `}`}, []string{"build", "ghostM"}},
		{"abstract class concrete method body", []string{`abstract class A {`, `  abstract m(): void;`, `  run() { ghostRunA(); }`, `}`}, []string{"ghostRunA"}},
		{"jsx class attr", []string{`const v = <div class={cls}>`, `  {ghostJsx()}`, `</div>;`}, []string{"ghostJsx"}},
		{"object literal arg", []string{`foo({`, `  a: 1,`, `});`, `ghostArg();`}, []string{"foo", "ghostArg"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			refs := ExtractRefs(LangJS, lines(tc.src...))
			if !containsAll(refs, tc.want...) {
				t.Errorf("real calls must still be refs, want %v got %v", tc.want, refNames(refs))
			}
		})
	}
	// obj.method() is a selector, never a ref, so assert the end to end shape:
	// the object-literal method body's bare call is flagged by Run.
	diffs := []FileDiff{{Path: "a.ts", AddedLines: lines(
		`const o = {`, `  run() {`, `    obj.method();`, `    ghostRun();`, `  },`, `};`)}}
	if names := violationSymbols(Run(map[string]struct{}{"obj": {}}, "", diffs)); !containsStr(names, "ghostRun") {
		t.Errorf("ghostRun in an object-literal method body must be flagged, got %v", names)
	}
}

// Review round 1 (#563): wrapped initializers, ternary object branches, regex
// literals and heritage generics must neither hide a real call nor lose a
// signature.
func TestExtractRefs_TS_ReviewRound1(t *testing.T) {
	for _, tc := range []struct {
		name string
		src  []string
		want []string // must be refs
		nope []string // must not be refs
	}{
		{"T1 wrapped field initializer", []string{`class Store {`, `  private readonly client =`, `    createClient(config);`, `}`}, []string{"createClient"}, nil},
		{"T1 static field", []string{`class Store {`, `  static x =`, `    makeX(1);`, `}`}, []string{"makeX"}, nil},
		{"T1 arrow body wrap", []string{`class Store {`, `  h = () =>`, `    doIt(1);`, `}`}, []string{"doIt"}, nil},
		{"T1 or wrap", []string{`class Store {`, `  x = a ||`, `    fallbackOne();`, `}`}, []string{"fallbackOne"}, nil},
		{"T1 await wrap", []string{`class Store {`, `  x = await`, `    loadIt();`, `}`}, []string{"loadIt"}, nil},
		{"T1 ternary wrap", []string{`class Store {`, `  x = cond ?`, `    pickA() : pickB();`, `}`}, []string{"pickA"}, nil},
		{"T1 sig after a wrapped union member is skipped", []string{`interface I {`, `  a: string |`, `    number;`, `  m(): void;`, `}`}, nil, []string{"m"}},
		{"T2 ternary return", []string{`function f() {`, `  return enabled ? defaults() : {`, `    extra:`, `      ghostE(1),`, `  };`, `}`}, []string{"ghostE"}, nil},
		{"T2 ternary call arg", []string{`foo(cond ? a : {`, `  b:`, `    ghostArg(),`, `});`}, []string{"ghostArg"}, nil},
		{"T3 regex brace", []string{`class A {`, `  r = /\{/;`, `}`, `ghost();`}, []string{"ghost"}, nil},
		{"T5 interface generic literal", []string{`interface R<T extends { id: string }> extends B<{ a: 1 }> {`, `  m(): void;`, `}`}, nil, []string{"m"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			refs := ExtractRefs(LangJS, lines(tc.src...))
			if !containsAll(refs, tc.want...) {
				t.Errorf("want refs %v, got %v", tc.want, refNames(refs))
			}
			if !containsNone(refs, tc.nope...) {
				t.Errorf("must not be refs %v, got %v", tc.nope, refNames(refs))
			}
		})
	}
}
