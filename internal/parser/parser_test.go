package parser

import (
	"os"
	"path/filepath"
	"regexp"
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
	dir := t.TempDir()
	rel := "fixture.py"
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
