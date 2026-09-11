package parser

import (
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"testing"
)

// TestCacheVersionStable: the tag-schema fingerprint is a 64-char hex sha256
// and deterministic across calls.
func TestCacheVersionStable(t *testing.T) {
	v1 := CacheVersion()
	v2 := CacheVersion()
	if v1 != v2 {
		t.Fatalf("CacheVersion() not stable: %q != %q", v1, v2)
	}
	if !regexp.MustCompile(`^[0-9a-f]{64}$`).MatchString(v1) {
		t.Fatalf("CacheVersion() = %q, want 64 lowercase hex chars", v1)
	}
}

// parsePy writes src to a .py fixture in a temp dir and parses it.
func parsePy(t *testing.T, src string) []Tag {
	t.Helper()
	return parseFixture(t, "fixture.py", src)
}

// parseTS writes src to a .ts fixture in a temp dir and parses it.
func parseTS(t *testing.T, src string) []Tag {
	t.Helper()
	return parseFixture(t, "fixture.ts", src)
}

// parseGo writes src to a .go fixture in a temp dir and parses it.
func parseGo(t *testing.T, src string) []Tag {
	t.Helper()
	return parseFixture(t, "fixture.go", src)
}

// parseFixture writes src to rel in a temp dir and parses it.
func parseFixture(t *testing.T, rel, src string) []Tag {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, rel), []byte(src), 0o644); err != nil {
		t.Fatalf("write fixture: %v", err)
	}
	tags, err := ParseFile(dir, rel)
	if err != nil {
		t.Fatalf("ParseFile: %v", err)
	}
	return tags
}

// hasTag reports whether some tag has the given name and kind.
func hasTag(tags []Tag, name, kind string) bool {
	for _, t := range tags {
		if t.Name == name && t.Kind == kind {
			return true
		}
	}
	return false
}

// hasDefTag reports whether some definition (non-ref) tag has the given name.
func hasDefTag(tags []Tag, name string) bool {
	for _, t := range tags {
		if t.Name == name && t.IsDef() {
			return true
		}
	}
	return false
}

// countTags counts tags with the given name and kind ("ref" or "def" for
// any definition kind).
func countTags(tags []Tag, name, kind string) int {
	n := 0
	for _, t := range tags {
		if t.Name != name {
			continue
		}
		if (kind == "def" && t.IsDef()) || (kind == "ref" && t.Kind == "ref") {
			n++
		}
	}
	return n
}

// TestPythonModuleLevelVariableDef: a module-level assignment is a
// "variable" definition (kept).
func TestPythonModuleLevelVariableDef(t *testing.T) {
	tags := parsePy(t, "_CACHE_PERSIST_VERSION = 4\n")
	if !hasTag(tags, "_CACHE_PERSIST_VERSION", "variable") {
		t.Errorf("missing variable def for _CACHE_PERSIST_VERSION; got %+v", tags)
	}
}

// TestPythonFunctionLocalAssignmentsDropped: assignments inside a def are
// locals and must NOT produce definition tags (Class A fix).
func TestPythonFunctionLocalAssignmentsDropped(t *testing.T) {
	src := "import time\n" +
		"\n" +
		"def f():\n" +
		"    _t0 = time.perf_counter()\n" +
		"    _ = foo()\n"
	tags := parsePy(t, src)
	if hasDefTag(tags, "_t0") {
		t.Errorf("function-local _t0 must not be a definition; got %+v", tags)
	}
	if hasDefTag(tags, "_") {
		t.Errorf("function-local _ must not be a definition; got %+v", tags)
	}
	// The call inside the local assignment is still a reference.
	if !hasTag(tags, "foo", "ref") {
		t.Errorf("expected ref foo; got %+v", tags)
	}
	// The def itself is still a function definition.
	if !hasTag(tags, "f", "function") {
		t.Errorf("expected function def f; got %+v", tags)
	}
}

// TestPythonClassBodyAttributeDef: a class-body assignment is a class
// attribute and keeps Kind "variable".
func TestPythonClassBodyAttributeDef(t *testing.T) {
	tags := parsePy(t, "class C:\n    _registry = {}\n")
	if !hasTag(tags, "_registry", "variable") {
		t.Errorf("missing variable def for class attribute _registry; got %+v", tags)
	}
	if !hasTag(tags, "C", "class") {
		t.Errorf("expected class def C; got %+v", tags)
	}
}

// TestPythonNestedFunctionAssignmentDropped: an assignment inside a def
// nested in a def is still local and must be dropped.
func TestPythonNestedFunctionAssignmentDropped(t *testing.T) {
	src := "def outer():\n" +
		"    def inner():\n" +
		"        _nested = 1\n"
	tags := parsePy(t, src)
	if hasDefTag(tags, "_nested") {
		t.Errorf("nested-function-local _nested must not be a definition; got %+v", tags)
	}
	if !hasTag(tags, "outer", "function") || !hasTag(tags, "inner", "function") {
		t.Errorf("expected function defs outer and inner; got %+v", tags)
	}
}

// TestPythonCallbackArgumentRef: a bare identifier passed as a positional
// call argument (callback) is a reference (Class B fix).
func TestPythonCallbackArgumentRef(t *testing.T) {
	tags := parsePy(t, "task.add_done_callback(_propagate)\n")
	if !hasTag(tags, "_propagate", "ref") {
		t.Errorf("expected ref _propagate; got %+v", tags)
	}
}

// TestPythonDictValueRef: a bare identifier used as a dict literal value is
// a reference (Class B fix).
func TestPythonDictValueRef(t *testing.T) {
	tags := parsePy(t, "_EXTRACTOR_REGISTRY = {\"qwen3_5\": _qwen35_extract_queries}\n")
	if !hasTag(tags, "_qwen35_extract_queries", "ref") {
		t.Errorf("expected ref _qwen35_extract_queries; got %+v", tags)
	}
	if !hasTag(tags, "_EXTRACTOR_REGISTRY", "variable") {
		t.Errorf("expected variable def _EXTRACTOR_REGISTRY; got %+v", tags)
	}
}

// TestPythonAttributeAssignmentRHSRef: the RHS identifier of an attribute
// assignment (obj.step = _patched_step) is a reference (Class B fix).
func TestPythonAttributeAssignmentRHSRef(t *testing.T) {
	tags := parsePy(t, "obj.step = _patched_step\n")
	if !hasTag(tags, "_patched_step", "ref") {
		t.Errorf("expected ref _patched_step; got %+v", tags)
	}
	// Neither side of an attribute assignment is a variable definition.
	if hasDefTag(tags, "step") || hasDefTag(tags, "obj") {
		t.Errorf("attribute assignment must not create variable defs; got %+v", tags)
	}
}

// TestPythonDefaultParamRef: a bare identifier used as a default parameter
// value is a reference (Class B fix).
func TestPythonDefaultParamRef(t *testing.T) {
	tags := parsePy(t, "def f(cb=_default_cb):\n    pass\n")
	if !hasTag(tags, "_default_cb", "ref") {
		t.Errorf("expected ref _default_cb; got %+v", tags)
	}
	if !hasTag(tags, "f", "function") {
		t.Errorf("expected function def f; got %+v", tags)
	}
}

// TestPythonDecoratorRef: a bare identifier used as a decorator is a
// reference (Class B fix).
func TestPythonDecoratorRef(t *testing.T) {
	tags := parsePy(t, "@_my_decorator\ndef f():\n    pass\n")
	if !hasTag(tags, "_my_decorator", "ref") {
		t.Errorf("expected ref _my_decorator; got %+v", tags)
	}
}

// TestPythonSelfClsRefsDropped: `self` / `cls` attribute-object refs are
// dropped (they can never resolve to a module-level definition); other
// attribute objects are still refs.
func TestPythonSelfClsRefsDropped(t *testing.T) {
	src := "class C:\n" +
		"    def m(self):\n" +
		"        self.x\n" +
		"        cls.y\n" +
		"        obj.x\n"
	tags := parsePy(t, src)
	if hasTag(tags, "self", "ref") {
		t.Errorf("self must not produce a ref tag; got %+v", tags)
	}
	if hasTag(tags, "cls", "ref") {
		t.Errorf("cls must not produce a ref tag; got %+v", tags)
	}
	if !hasTag(tags, "obj", "ref") {
		t.Errorf("expected ref obj for obj.x; got %+v", tags)
	}
}

// TestPythonExistingBehaviorPreserved: the pre-existing capture behavior
// (call refs, attribute-call refs, function/class defs) is unchanged.
func TestPythonExistingBehaviorPreserved(t *testing.T) {
	src := "def x():\n" +
		"    foo()\n" +
		"    obj.bar()\n" +
		"\n" +
		"class Y:\n" +
		"    pass\n"
	tags := parsePy(t, src)
	for _, want := range []struct{ name, kind string }{
		{"foo", "ref"},
		{"bar", "ref"},
		{"x", "function"},
		{"Y", "class"},
	} {
		if !hasTag(tags, want.name, want.kind) {
			t.Errorf("expected %s tag %q; got %+v", want.kind, want.name, tags)
		}
	}
}

// TestPythonKeywordArgumentNameNotRef: the NAME part of a keyword argument
// is not a reference; only its value can be.
func TestPythonKeywordArgumentNameNotRef(t *testing.T) {
	tags := parsePy(t, "f(timeout=3)\n")
	if hasTag(tags, "timeout", "ref") {
		t.Errorf("keyword argument name timeout must not be a ref; got %+v", tags)
	}
	if !hasTag(tags, "f", "ref") {
		t.Errorf("expected ref f; got %+v", tags)
	}
}

// TestPythonDictKeyRef: a bare identifier used as a dict literal KEY is a
// reference (GH-2; only pair value: was captured before).
func TestPythonDictKeyRef(t *testing.T) {
	tags := parsePy(t, "d = {_KEY_FN: 1}\n")
	if !hasTag(tags, "_KEY_FN", "ref") {
		t.Errorf("expected ref _KEY_FN for dict key; got %+v", tags)
	}
}

// TestPythonExceptRaiseRefs: except targets (bare, tuple, as-pattern) and
// raise targets (bare, from-cause) are references (GH-2).
func TestPythonExceptRaiseRefs(t *testing.T) {
	src := "try:\n" +
		"    pass\n" +
		"except _ErrA:\n" +
		"    pass\n" +
		"except (_ErrB, _ErrC) as _e:\n" +
		"    pass\n" +
		"raise _ErrD\n" +
		"raise _ErrE from _cause\n"
	tags := parsePy(t, src)
	for _, name := range []string{"_ErrA", "_ErrB", "_ErrC", "_ErrD", "_ErrE", "_cause"} {
		if !hasTag(tags, name, "ref") {
			t.Errorf("expected ref %q; got %+v", name, tags)
		}
	}
	// The as-alias is a definition position, not a ref.
	if hasTag(tags, "_e", "ref") {
		t.Errorf("except as-alias _e must not be a ref; got %+v", tags)
	}
}

// TestPythonAnnotationRefs: type annotations (variable, parameter, return,
// including the `type` wrapper node) are references (GH-2).
func TestPythonAnnotationRefs(t *testing.T) {
	src := "x: _T = 1\n" +
		"y: _T2\n" +
		"def f(a: _T3, b: _T4 = _dflt) -> _R:\n" +
		"    z: _T5 = 2\n" +
		"    return z\n"
	tags := parsePy(t, src)
	for _, name := range []string{"_T", "_T2", "_T3", "_T4", "_dflt", "_R", "_T5"} {
		if !hasTag(tags, name, "ref") {
			t.Errorf("expected ref %q; got %+v", name, tags)
		}
	}
}

// TestPythonComprehensionRefs: comprehension iterables, element bodies and
// conditions are references; the for-target is not (GH-2).
func TestPythonComprehensionRefs(t *testing.T) {
	src := "a = [g(_el1) for _tg1 in _items if _pred]\n" +
		"b = {_el2 for _tg2 in _items2 if _pred2}\n" +
		"c = (_el3 for _tg3 in _items3)\n" +
		"d = {_el4: _el4b for _tg4 in _items4 if _pred4}\n"
	tags := parsePy(t, src)
	for _, name := range []string{"g", "_el1", "_items", "_pred", "_el2", "_items2", "_pred2", "_el3", "_items3", "_el4", "_el4b", "_items4", "_pred4"} {
		if !hasTag(tags, name, "ref") {
			t.Errorf("expected ref %q; got %+v", name, tags)
		}
	}
	// The for-targets are definition positions, not refs.
	for _, name := range []string{"_tg1", "_tg2", "_tg3", "_tg4"} {
		if hasTag(tags, name, "ref") {
			t.Errorf("comprehension for-target %s must not be a ref; got %+v", name, tags)
		}
	}
}

// TestPythonSplatAwaitAugmentedRefs: splats in calls and literals, await,
// and augmented-assignment RHS are references (GH-2).
func TestPythonSplatAwaitAugmentedRefs(t *testing.T) {
	src := "f(*_args, **_kw)\n" +
		"l = [*_args2, 1]\n" +
		"d = {**_kw2}\n" +
		"async def h():\n" +
		"    await _coro\n" +
		"total += _STEP\n"
	tags := parsePy(t, src)
	for _, name := range []string{"_args", "_kw", "_args2", "_kw2", "_coro", "_STEP"} {
		if !hasTag(tags, name, "ref") {
			t.Errorf("expected ref %q; got %+v", name, tags)
		}
	}
}

// TestPythonElifSliceDelParenSubscriptIndexRefs: elif conditions, slice
// bounds, del targets, parenthesized expressions, and subscript indices are
// references (GH-2).
func TestPythonElifSliceDelParenSubscriptIndexRefs(t *testing.T) {
	src := "if _c1:\n" +
		"    pass\n" +
		"elif _c2:\n" +
		"    pass\n" +
		"b = a[_lo:_hi]\n" +
		"del _x\n" +
		"p = (_p1)\n" +
		"c = _cache[_key]\n"
	tags := parsePy(t, src)
	for _, name := range []string{"_c2", "_lo", "_hi", "_x", "_p1", "_key"} {
		if !hasTag(tags, name, "ref") {
			t.Errorf("expected ref %q; got %+v", name, tags)
		}
	}
}

// TestPythonNonCallAttributeReadRef: a class attribute read via
// self._cache (never called) is a reference, keeping the class-attribute
// definition alive (GH-2).
func TestPythonNonCallAttributeReadRef(t *testing.T) {
	src := "class C:\n" +
		"    _cache = {}\n" +
		"    _registry = {}\n" +
		"    def m(self):\n" +
		"        self._cache[1]\n" +
		"        cls._registry\n" +
		"D._x\n"
	tags := parsePy(t, src)
	if !hasTag(tags, "_cache", "variable") {
		t.Errorf("missing variable def _cache; got %+v", tags)
	}
	if !hasTag(tags, "_cache", "ref") {
		t.Errorf("expected ref _cache from self._cache read; got %+v", tags)
	}
	if !hasTag(tags, "_registry", "ref") {
		t.Errorf("expected ref _registry from cls._registry read; got %+v", tags)
	}
	if !hasTag(tags, "_x", "ref") {
		t.Errorf("expected ref _x from D._x read; got %+v", tags)
	}
	if hasTag(tags, "self", "ref") || hasTag(tags, "cls", "ref") {
		t.Errorf("self/cls must not be refs; got %+v", tags)
	}
}

// TestPythonParamAndKwargNamesNotRefs: parameter names and keyword-argument
// names are definition positions, never refs (GH-2 regression guard).
func TestPythonParamAndKwargNamesNotRefs(t *testing.T) {
	tags := parsePy(t, "def f(_pname, _kwonly=_val):\n    g(_kwname=1)\n")
	if hasTag(tags, "_pname", "ref") {
		t.Errorf("parameter name _pname must not be a ref; got %+v", tags)
	}
	if hasTag(tags, "_kwname", "ref") {
		t.Errorf("keyword argument name _kwname must not be a ref; got %+v", tags)
	}
	if !hasTag(tags, "_val", "ref") {
		t.Errorf("expected ref _val for default value; got %+v", tags)
	}
}

// TestPythonValuePositionRefs: every additional value position added to
// pyQuery produces a reference, and definition positions (for-loop target,
// with-alias) do not.
func TestPythonValuePositionRefs(t *testing.T) {
	src := "def f():\n" +
		"    a = [_la, _lt, _ls]\n" + // list elements
		"    b = (_tb,)\n" + // tuple element
		"    c = {_se, _sf}\n" + // set elements
		"    _REGISTRY.get(\"k\")\n" + // attribute object
		"    _cache[_key]\n" + // subscript value
		"    x = _a + _b\n" + // binary operands
		"    y = _c > _d\n" + // comparison operands
		"    z = _e and _f\n" + // boolean operands
		"    w = not _g\n" + // not operand
		"    v = -_h\n" + // unary operand
		"    if _i1:\n" + // if condition
		"        pass\n" +
		"    while _i2:\n" + // while condition
		"        pass\n" +
		"    for _i3 in _iter:\n" + // for iterable (target is a def position)
		"        pass\n" +
		"    with _cm as _alias:\n" + // with item value (alias is a def position)
		"        pass\n" +
		"    msg = f\"v={_fv}\"\n" + // f-string interpolation
		"    return _r1, _r2\n" + // return value list
		"    yield _y1\n" + // yield value
		"    assert _ok\n" + // assert expression
		"    t = _c1 if _c2 else _c3\n" + // conditional expression
		"    l = lambda: _lb\n" // lambda body
	tags := parsePy(t, src)
	for _, name := range []string{
		"_la", "_lt", "_ls",
		"_tb",
		"_se", "_sf",
		"_REGISTRY", "get",
		"_cache",
		"_a", "_b",
		"_c", "_d",
		"_e", "_f",
		"_g",
		"_h",
		"_i1",
		"_i2",
		"_iter",
		"_cm",
		"_fv",
		"_r1", "_r2",
		"_y1",
		"_ok",
		"_c1", "_c2", "_c3",
		"_lb",
	} {
		if !hasTag(tags, name, "ref") {
			t.Errorf("expected ref %q; got %+v", name, tags)
		}
	}
	// Definition positions must not become refs.
	if hasTag(tags, "_i3", "ref") {
		t.Errorf("for-loop target _i3 must not be a ref; got %+v", tags)
	}
	if hasTag(tags, "_alias", "ref") {
		t.Errorf("with-alias _alias must not be a ref; got %+v", tags)
	}
	// Locals assigned in the function body are not variable defs.
	if hasDefTag(tags, "a") || hasDefTag(tags, "x") || hasDefTag(tags, "l") {
		t.Errorf("function-local assignments must not be variable defs; got %+v", tags)
	}
}

// TestTSFunctionLocalVariableDefsDropped: variable_declarators inside
// function bodies (declaration, expression, arrow, method, generator) are
// locals and must NOT produce definition tags (Class A, GH-2).
func TestTSFunctionLocalVariableDefsDropped(t *testing.T) {
	src := "function f() { const _x1 = 1; let _x2 = 2; }\n" +
		"const g = function () { const _x3 = 3; };\n" +
		"const h = () => { const _x4 = 4; };\n" +
		"class C {\n" +
		"    method() { const _x5 = 5; }\n" +
		"}\n" +
		"function* gen() { const _x6 = 6; }\n"
	tags := parseTS(t, src)
	for _, name := range []string{"_x1", "_x2", "_x3", "_x4", "_x5", "_x6"} {
		if hasDefTag(tags, name) {
			t.Errorf("function-local %s must not be a definition; got %+v", name, tags)
		}
	}
	// The enclosing function is still defined.
	if !hasTag(tags, "f", "function") {
		t.Errorf("expected function def f; got %+v", tags)
	}
}

// TestTSModuleLevelAndClassFieldDefsKept: package-level variable_declarators
// and class fields keep their definition tags (Class A, GH-2).
func TestTSModuleLevelAndClassFieldDefsKept(t *testing.T) {
	src := "const _MODULE_VAR = 1;\n" +
		"let _MODULE_LET = 2;\n" +
		"class C {\n" +
		"    _field = 3;\n" +
		"}\n"
	tags := parseTS(t, src)
	for _, name := range []string{"_MODULE_VAR", "_MODULE_LET", "_field"} {
		if !hasTag(tags, name, "variable") {
			t.Errorf("missing variable def %s; got %+v", name, tags)
		}
	}
	if !hasTag(tags, "C", "class") {
		t.Errorf("expected class def C; got %+v", tags)
	}
}

// TestTSNestedFunctionLocalDropped: a declarator inside an arrow function
// nested in a function is still local (Class A, GH-2).
func TestTSNestedFunctionLocalDropped(t *testing.T) {
	src := "function outer() {\n" +
		"    const inner = () => { const _nested = 1; return _nested; };\n" +
		"    return inner;\n" +
		"}\n"
	tags := parseTS(t, src)
	if hasDefTag(tags, "_nested") {
		t.Errorf("nested-function-local _nested must not be a definition; got %+v", tags)
	}
	if !hasTag(tags, "outer", "function") {
		t.Errorf("expected function def outer; got %+v", tags)
	}
}

// TestGoFunctionLocalVarConstDefsDropped: var/const specs inside function
// and method bodies are locals and must NOT produce definition tags
// (Class A, GH-2).
func TestGoFunctionLocalVarConstDefsDropped(t *testing.T) {
	src := "package p\n" +
		"\n" +
		"func f() {\n" +
		"\tvx int\n" +
		"\tconst cy = 1\n" +
		"\t_ = vx\n" +
		"\t_ = cy\n" +
		"}\n" +
		"\n" +
		"type R struct{}\n" +
		"\n" +
		"func (r R) m() {\n" +
		"\tvar vm int\n" +
		"\tconst cm = 2\n" +
		"\t_ = vm\n" +
		"\t_ = cm\n" +
		"}\n"
	tags := parseGo(t, src)
	for _, name := range []string{"vx", "cy", "vm", "cm"} {
		if hasDefTag(tags, name) {
			t.Errorf("function-local %s must not be a definition; got %+v", name, tags)
		}
	}
	if !hasTag(tags, "f", "function") || !hasTag(tags, "m", "method") {
		t.Errorf("expected function def f and method def m; got %+v", tags)
	}
}

// TestGoPackageLevelVarConstDefsKept: package-level var/const declarations
// keep their definition tags (Class A, GH-2).
func TestGoPackageLevelVarConstDefsKept(t *testing.T) {
	src := "package p\n" +
		"\n" +
		"var GV int\n" +
		"\n" +
		"const KC = 1\n"
	tags := parseGo(t, src)
	if !hasTag(tags, "GV", "variable") {
		t.Errorf("missing variable def GV; got %+v", tags)
	}
	if !hasTag(tags, "KC", "constant") {
		t.Errorf("missing constant def KC; got %+v", tags)
	}
}

// TestGoFuncLiteralLocalDropped: a var spec inside a func literal nested in
// a function is still local (Class A, GH-2).
func TestGoFuncLiteralLocalDropped(t *testing.T) {
	src := "package p\n" +
		"\n" +
		"func outer() {\n" +
		"\th := func() {\n" +
		"\t\tvar nested int\n" +
		"\t\t_ = nested\n" +
		"\t}\n" +
		"\t_ = h\n" +
		"}\n"
	tags := parseGo(t, src)
	if hasDefTag(tags, "nested") {
		t.Errorf("func-literal-local nested must not be a definition; got %+v", tags)
	}
	if !hasTag(tags, "outer", "function") {
		t.Errorf("expected function def outer; got %+v", tags)
	}
}

// TestGoValuePositionRefs: identifiers in Go value positions (call
// arguments, composite-literal values, assignment/short-var RHS, return
// values, selector operands and non-call selector reads) are references
// (Class B, GH-2).
func TestGoValuePositionRefs(t *testing.T) {
	src := "package p\n" +
		"\n" +
		"func use() {\n" +
		"\thandler(_cb, _val)\n" + // call arguments
		"\tv := T{F: _a, G: 2}\n" + // composite literal values (keyed + positional)
		"\tw := _b\n" + // assignment RHS
		"\tz := _c\n" + // short var declaration RHS
		"\tvar u = _d\n" + // var spec RHS
		"\treturn v + w + z + u\n" + // return values
		"\t_ = pkg.X\n" + // selector operand (non-call read)
		"\t_ = obj.Y\n" + // selector operand, second
		"\t_ = v\n" +
		"}\n"
	tags := parseGo(t, src)
	for _, name := range []string{"_cb", "_val", "_a", "_b", "_c", "_d", "pkg", "X", "obj", "Y"} {
		if !hasTag(tags, name, "ref") {
			t.Errorf("expected ref %q; got %+v", name, tags)
		}
	}
	// The callee is still a call reference.
	if !hasTag(tags, "handler", "ref") {
		t.Errorf("expected ref handler (callee); got %+v", tags)
	}
}

// TestGoTypeRefs: type identifiers in non-definition positions (parameter,
// result, field, var/const, pointer/slice/map/channel elements, composite
// literal type, type assertion, generic type args) are references (Class B,
// GH-2).
func TestGoTypeRefs(t *testing.T) {
	src := "package p\n" +
		"\n" +
		"func t(a _T1, b *_T2) []map[string]chan _T3 {\n" + // param + result types
		"\tvar pv *_T4\n" + // pointer element
		"\tvar sv []_T5\n" + // slice element
		"\tvar mv map[string]_T6\n" + // map key+value
		"\tvar cv chan _T7\n" + // channel value
		"\tvar fv func(int) _T8\n" + // function type
		"\tcl := _T9{}\n" + // composite literal type
		"\tasserted := anyVal.(_T10)\n" + // type assertion
		"\tgen[_T11]()\n" + // generic type argument
		"\t_ = pv\n" +
		"\t_ = sv\n" +
		"\t_ = mv\n" +
		"\t_ = cv\n" +
		"\t_ = fv\n" +
		"\t_ = cl\n" +
		"\t_ = asserted\n" +
		"\treturn nil\n" +
		"}\n" +
		"\n" +
		"type S struct{ F _T12 }\n"
	tags := parseGo(t, src)
	for _, name := range []string{
		"_T1", "_T2", "_T3", "_T4", "_T5", "_T6", "_T7", "_T8",
		"_T9", "_T10", "_T11", "_T12",
	} {
		if !hasTag(tags, name, "ref") {
			t.Errorf("expected type ref %q; got %+v", name, tags)
		}
	}
	// The struct and its field type def are still definitions.
	if !hasTag(tags, "S", "type") {
		t.Errorf("expected type def S; got %+v", tags)
	}
}

// TestGoUnusedTypeStillReported: a Go type defined and never used in one
// file must NOT self-reference: exactly one def tag and zero ref tags with
// its name (Class B regression, GH-2).
func TestGoUnusedTypeStillReported(t *testing.T) {
	src := "package p\n" +
		"\n" +
		"type _Unused struct{}\n"
	tags := parseGo(t, src)
	if n := countTags(tags, "_Unused", "def"); n != 1 {
		t.Errorf("def tags for _Unused = %d, want exactly 1; got %+v", n, tags)
	}
	if n := countTags(tags, "_Unused", "ref"); n != 0 {
		t.Errorf("ref tags for _Unused = %d, want 0 (no self-reference); got %+v", n, tags)
	}
}

// TestTSValuePositionRefs: identifiers in TS value positions (call
// arguments, object-literal pair values, declarator values, assignment
// RHS, array elements, return values, member objects and non-call property
// reads, subscript objects, new constructors) are references (Class B,
// GH-2).
func TestTSValuePositionRefs(t *testing.T) {
	src := "f(_a);\n" + // call argument
		"const o = { k: _b };\n" + // object literal pair value
		"let v = _c;\n" + // variable declarator value
		"v = _d;\n" + // assignment RHS
		"const arr = [_e];\n" + // array element
		"function g(): number { return _f; }\n" + // return value
		"obj.prop;\n" + // member object + non-call property read
		"arr[_g];\n" + // subscript object + index
		"new Ctor(_h);\n" // new constructor + argument
	tags := parseTS(t, src)
	for _, name := range []string{"_a", "_b", "_c", "_d", "_e", "_f", "obj", "prop", "_g", "Ctor", "_h"} {
		if !hasTag(tags, name, "ref") {
			t.Errorf("expected ref %q; got %+v", name, tags)
		}
	}
	// The callee is still a call reference.
	if !hasTag(tags, "f", "ref") {
		t.Errorf("expected ref f (callee); got %+v", tags)
	}
}

// TestTSTypeRefs: type identifiers in type annotations and generic
// arguments are references; a declaration's own name is never a ref
// (Class B, GH-2).
func TestTSTypeRefs(t *testing.T) {
	src := "function f(x: _T1): _T2 { return x; }\n" + // param + return annotations
		"const a: _T3 = b;\n" + // variable annotation
		"const g = foo<_T4>(_T5);\n" + // generic type argument + call arg
		"const h: Array<_T6> = [];\n" + // generic type in annotation
		"type Alias = _T7;\n" + // type alias value
		"const i: _T8 | null = null;\n" // union type
	tags := parseTS(t, src)
	for _, name := range []string{"_T1", "_T2", "_T3", "_T4", "_T5", "_T6", "_T7", "_T8"} {
		if !hasTag(tags, name, "ref") {
			t.Errorf("expected type ref %q; got %+v", name, tags)
		}
	}
	// The alias's own name is a definition, not a self-reference.
	if n := countTags(tags, "Alias", "ref"); n != 0 {
		t.Errorf("ref tags for Alias = %d, want 0 (no self-reference); got %+v", n, tags)
	}
	if !hasTag(tags, "Alias", "type") {
		t.Errorf("expected type def Alias; got %+v", tags)
	}
}

// TestTSUnusedTypeStillReported: a TS type defined and never used in one
// file must NOT self-reference: exactly one def tag and zero ref tags with
// its name (Class B regression, GH-2).
func TestTSUnusedTypeStillReported(t *testing.T) {
	src := "interface _Unused {}\n"
	tags := parseTS(t, src)
	if n := countTags(tags, "_Unused", "def"); n != 1 {
		t.Errorf("def tags for _Unused = %d, want exactly 1; got %+v", n, tags)
	}
	if n := countTags(tags, "_Unused", "ref"); n != 0 {
		t.Errorf("ref tags for _Unused = %d, want 0 (no self-reference); got %+v", n, tags)
	}
}

// endLineOf returns the EndLine of the first def tag with the given name and
// kind, or -1 if absent.
func endLineOf(tags []Tag, name, kind string) int {
	for _, t := range tags {
		if t.Name == name && t.Kind == kind {
			return t.EndLine
		}
	}
	return -1
}

// TestPythonFunctionDefEndLine: a def spanning several lines gets an EndLine
// equal to the function's last line (GH-4).
func TestPythonFunctionDefEndLine(t *testing.T) {
	src := "def f():\n" + // 1
		"    a = 1\n" + // 2
		"    b = 2\n" + // 3
		"    return a + b\n" // 4
	tags := parsePy(t, src)
	if got := endLineOf(tags, "f", "function"); got != 4 {
		t.Errorf("function f EndLine = %d, want 4 (last body line); got %+v", got, tags)
	}
}

// TestPythonClassEndLineCoversMethod: a class's EndLine extends to the end of
// its last member (>= the method's EndLine) (GH-4).
func TestPythonClassEndLineCoversMethod(t *testing.T) {
	src := "class C:\n" + // 1
		"    def m(self):\n" + // 2
		"        return 1\n" // 3
	tags := parsePy(t, src)
	classEnd := endLineOf(tags, "C", "class")
	// Python methods parse as function_definition, so Kind is "function".
	methodEnd := endLineOf(tags, "m", "function")
	if classEnd != 3 {
		t.Errorf("class C EndLine = %d, want 3; got %+v", classEnd, tags)
	}
	if methodEnd != 3 {
		t.Errorf("method m EndLine = %d, want 3; got %+v", methodEnd, tags)
	}
	if classEnd < methodEnd {
		t.Errorf("class EndLine %d < method EndLine %d; class span must contain the method", classEnd, methodEnd)
	}
}

// TestGoFunctionDefEndLine: a Go function's EndLine is the closing brace line
// (GH-4).
func TestGoFunctionDefEndLine(t *testing.T) {
	src := "package p\n" + // 1
		"\n" + // 2
		"func f() int {\n" + // 3
		"\tx := 1\n" + // 4
		"\treturn x\n" + // 5
		"}\n" // 6
	tags := parseGo(t, src)
	if got := endLineOf(tags, "f", "function"); got != 6 {
		t.Errorf("function f EndLine = %d, want 6; got %+v", got, tags)
	}
}

// TestTSMethodDefEndLine: a TS class method's EndLine is the method's closing
// brace line (GH-4).
func TestTSMethodDefEndLine(t *testing.T) {
	src := "class C {\n" + // 1
		"    m() {\n" + // 2
		"        return 1;\n" + // 3
		"    }\n" + // 4
		"}\n" // 5
	tags := parseTS(t, src)
	if got := endLineOf(tags, "m", "method"); got != 4 {
		t.Errorf("method m EndLine = %d, want 4; got %+v", got, tags)
	}
	if got := endLineOf(tags, "C", "class"); got != 5 {
		t.Errorf("class C EndLine = %d, want 5; got %+v", got, tags)
	}
}

// TestModuleLevelVariableEndLineEqualsLine: a bare module-level variable has
// no declaration ancestor, so EndLine == Line (GH-4).
func TestModuleLevelVariableEndLineEqualsLine(t *testing.T) {
	tags := parsePy(t, "x = 1\n")
	for _, tag := range tags {
		if tag.Name == "x" && tag.Kind == "variable" {
			if tag.EndLine != tag.Line {
				t.Errorf("module-level variable x EndLine = %d, want Line %d", tag.EndLine, tag.Line)
			}
			return
		}
	}
	t.Fatal("missing variable def x")
}

// TestRefsKeepEndLineZero: reference tags never carry a span (GH-4).
func TestRefsKeepEndLineZero(t *testing.T) {
	src := "def f():\n" +
		"    return g()\n"
	tags := parsePy(t, src)
	for _, tag := range tags {
		if tag.Kind == "ref" && tag.EndLine != 0 {
			t.Errorf("ref %s EndLine = %d, want 0; got %+v", tag.Name, tag.EndLine, tags)
		}
	}
	if !hasTag(tags, "g", "ref") {
		t.Fatalf("expected ref g; got %+v", tags)
	}
}

// pyBenchmarkSource is a representative Python module used by
// BenchmarkParseFilePython: defs, methods, calls, comprehensions, f-strings.
const pyBenchmarkSource = `
import os
from collections import defaultdict

_CACHE = {}

def build_index(root):
    index = defaultdict(list)
    for dirpath, dirnames, filenames in os.walk(root):
        for name in filenames:
            if name.endswith(".py"):
                index[name].append(os.path.join(dirpath, name))
    return dict(index)

class Registry:
    def __init__(self, name):
        self.name = name
        self._entries = []

    def register(self, key, value):
        self._entries.append((key, value))
        return len(self._entries)

    def lookup(self, key):
        for k, v in self._entries:
            if k == key:
                return v
        return None

def summarize(index):
    return {name: len(paths) for name, paths in index.items() if paths}
`

// TestParseFileQueryCacheTransparent: two ParseFile calls for the same
// language yield identical tags, proving the per-language compiled-query
// cache (GH-5) does not change extraction results.
func TestParseFileQueryCacheTransparent(t *testing.T) {
	src := "def f(a, b):\n" +
		"    total = a + b\n" +
		"    return [x * total for x in (a, b)]\n" +
		"MOD = f(1, 2)\n"
	first := parsePy(t, src)
	second := parsePy(t, src)
	if len(first) != len(second) {
		t.Fatalf("tag count differs across calls: %d != %d", len(first), len(second))
	}
	for i := range first {
		if first[i] != second[i] {
			t.Fatalf("tag %d differs across calls: %+v != %+v", i, first[i], second[i])
		}
	}
}

// BenchmarkParseFilePython measures end-to-end ParseFile cost for a Python
// file, including the (now cached) query compile path (GH-5).
func BenchmarkParseFilePython(b *testing.B) {
	dir, err := os.MkdirTemp("", "repomap-bench")
	if err != nil {
		b.Fatalf("mkdir temp: %v", err)
	}
	b.Cleanup(func() { os.RemoveAll(dir) })
	rel := "bench.py"
	if err := os.WriteFile(filepath.Join(dir, rel), []byte(pyBenchmarkSource), 0o644); err != nil {
		b.Fatalf("write fixture: %v", err)
	}
	b.ResetTimer()
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		if _, err := ParseFile(dir, rel); err != nil {
			b.Fatalf("ParseFile: %v", err)
		}
	}
}

// TestPythonNoExactDuplicateTags: overlapping captures on one line (an
// attribute that is both called and read) must yield exactly one ref tag per
// (Name, Kind, Line) (GH-5).
func TestPythonNoExactDuplicateTags(t *testing.T) {
	src := "obj.method()\n"
	tags := parsePy(t, src)

	var methodRefs int
	for _, tag := range tags {
		if tag.Kind == "ref" && tag.Name == "method" {
			methodRefs++
			if tag.Line != 1 {
				t.Errorf("ref method Line = %d, want 1; got %+v", tag.Line, tags)
			}
		}
	}
	if methodRefs != 1 {
		t.Fatalf("expected exactly 1 ref tag named method, got %d; tags: %+v", methodRefs, tags)
	}

	// Invariant across the whole file: no exact (Name, Kind, Line) repeats.
	seen := map[string]int{}
	for _, tag := range tags {
		key := tag.Name + "\x00" + tag.Kind + "\x00" + strconv.Itoa(tag.Line)
		seen[key]++
	}
	for key, n := range seen {
		if n > 1 {
			t.Errorf("exact duplicate tag %q appears %d times; tags: %+v", key, n, tags)
		}
	}
}

// TestPythonSameNameDifferentLinesKept: dedupe must not merge the same name
// referenced on different lines (SearchIdentifiers lists caller lines).
func TestPythonSameNameDifferentLinesKept(t *testing.T) {
	src := "a = helper()\n" +
		"b = helper()\n"
	tags := parsePy(t, src)
	if n := countTags(tags, "helper", "ref"); n != 2 {
		t.Fatalf("expected 2 ref tags for helper (one per line), got %d; tags: %+v", n, tags)
	}
}
