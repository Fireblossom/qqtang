"""Execution environment for the converted UI scripts.

The original host registered its native modules with boost.python and ran
``from UiScript import *`` (and friends) in one shared namespace before
executing res/uiRes/uiMain.pyc. new_namespace() plays that role: natives are
plain entries, and any name that is neither defined by a script, a native nor
a builtin resolves to a logging stub so unimplemented interfaces show up in
the missing-native report instead of aborting the UI.
"""

from __future__ import annotations

import builtins
import collections
import functools
import pathlib
import sys
import traceback
import types
from typing import Any, Callable

log = functools.partial(print, file=sys.__stderr__, flush=True)


def py2_div(left: Any, right: Any) -> Any:
    """Python 2 '/' semantics: floor division when both operands are integers."""
    if isinstance(left, int) and isinstance(right, int):
        return left // right
    return left / right


def py2_len(value: Any) -> int:
    """Python 2 len(): the scripts' strings were GBK bytes."""
    if isinstance(value, str):
        return len(value.encode("gbk", "replace"))
    return len(value)


def _py2_rank(value: Any) -> tuple:
    """Python 2's order for values of unrelated types: None first, then
    numbers, then everything else by type name."""
    if value is None:
        return (0,)
    if isinstance(value, (int, float)):
        return (1, value)
    return (2, type(value).__name__, id(value))


def py2_compare(left: Any, right: Any) -> int:
    """Python 2 ordering (-1, 0, 1), which also accepts mixed types such as
    None against an int."""
    try:
        return (left > right) - (left < right)
    except TypeError:
        a, b = _py2_rank(left), _py2_rank(right)
        return (a > b) - (a < b)


class MissingNative:
    """Placeholder for an unimplemented native; returns 0 like a void/int call."""

    def __init__(self, name: str, report: "MissingReport"):
        self.name = name
        self.report = report

    def __call__(self, *args: Any) -> int:
        self.report.record(self.name, args)
        return 0

    def __repr__(self) -> str:
        return f"<missing native {self.name}>"


class MissingReport:
    def __init__(self) -> None:
        self.calls: collections.Counter[str] = collections.Counter()
        self.examples: dict[str, str] = {}

    def record(self, name: str, args: tuple[Any, ...]) -> None:
        if name not in self.calls:
            log(f"[missing native] {name}{args!r}"[:300])
            self.examples[name] = repr(args)[:300]
        self.calls[name] += 1

    def as_dict(self) -> dict[str, Any]:
        return {name: {"calls": count, "first_args": self.examples[name]} for name, count in self.calls.most_common()}


class ScriptSys(types.ModuleType):
    """The scripts' view of sys: they replace stdout/stderr with a console
    writer, which must not capture the host's own output."""

    def __init__(self) -> None:
        super().__init__("sys")
        self.stdout = sys.__stdout__
        self.stderr = sys.__stderr__

    def __getattr__(self, name: str) -> Any:
        return getattr(sys, name)


class ScriptBuiltins(dict):
    """Builtins seen by the scripts; unknown names become MissingNative stubs.

    Module and class bodies resolve names with LOAD_NAME, which reads the
    globals dict directly but falls back to the builtins through the mapping
    protocol, so the fallback has to live here rather than on the globals.
    """

    def __init__(self, script_sys: ScriptSys, report: MissingReport):
        super().__init__(vars(builtins))
        self.report = report
        real_import = builtins.__import__

        def script_import(name: str, *args: Any, **kwargs: Any) -> Any:
            return script_sys if name == "sys" else real_import(name, *args, **kwargs)

        def script_print(*values: Any, sep: str = " ", end: str = "\n", file: Any = None, flush: bool = False) -> None:
            (file or script_sys.stdout).write(sep.join(str(value) for value in values) + end)

        self["__import__"] = script_import
        self["print"] = script_print

    def __missing__(self, name: str) -> Any:
        stub = MissingNative(name, self.report)
        self[name] = stub
        return stub


def new_namespace(report: MissingReport) -> dict[str, Any]:
    return {
        "__name__": "__main__",
        "__builtins__": ScriptBuiltins(ScriptSys(), report),
        "qqt_py2_div": py2_div,
        "qqt_py2_cmp_key": functools.cmp_to_key,
        "qqt_py2_compare": py2_compare,
        "qqt_py2_len": py2_len,
    }


def script_key(path: str) -> str:
    """Normalize a client script path: case-insensitive, extension-agnostic."""
    normalized = path.replace("\\", "/").lstrip("/").lower()
    for suffix in (".pyc", ".py"):
        if normalized.endswith(suffix):
            return normalized[: -len(suffix)]
    return normalized


class ScriptLoader:
    def __init__(self, scripts_root: pathlib.Path, namespace: dict[str, Any]):
        self.namespace = namespace
        self.scripts = {script_key(p.relative_to(scripts_root).as_posix()): p for p in scripts_root.rglob("*.py")}
        if not self.scripts:
            raise FileNotFoundError(f"no converted scripts under {scripts_root}; run tools/prepare-ui.sh first")

    def exec_file(self, path: str) -> None:
        script = self.scripts.get(script_key(path))
        if script is None:
            raise FileNotFoundError(f"converted script for {path!r} is missing")
        code = compile(script.read_text(encoding="utf-8"), path, "exec")
        exec(code, self.namespace)


def call_script(function: Callable[..., Any], *args: Any) -> Any:
    """Call a script function, passing only as many arguments as it declares.

    Event handlers are written as OnClick(this) or OnInit() depending on whether
    they need the widget path, so the host trims its arguments accordingly.
    """
    code = getattr(function, "__code__", None)
    if code is not None and not code.co_flags & 0x04:  # no *args
        args = args[: code.co_argcount]
    try:
        return function(*args)
    except Exception:
        log(f"[script error] {getattr(function, '__qualname__', function)}{args!r}"[:300])
        traceback.print_exc(file=sys.__stderr__)
        return None
