"""Decompile the original client's Python 2.3 UI scripts and convert them to Python 3.

Run through prepare-ui.sh, which pins the interpreter (lib2to3 was removed in
Python 3.13) and the decompiler versions. The output is derived from the
user's own client copy and must never be committed.

Pipeline per script:
  1. uncompyle6 turns the CPython 2.3 bytecode back into Python 2 source
     (scripts the host loads as .py are read as GBK source instead).
  2. lib2to3 rewrites Python 2 syntax (print statements, has_key, ...).
  3. An AST pass restores Python 2 semantics that 2to3 leaves alone:
     GBK byte strings become text, true division becomes qqt_py2_div,
     list.sort(cmp) uses qqt_py2_cmp_key and single <, <=, >, >= go through
     qqt_py2_compare (Python 2 orders None below numbers) and len() becomes
     qqt_py2_len (GBK byte length for text). The host provides
     the helpers (no leading "__": class bodies would mangle such names).
     2to3's list(...) wrappers become [*...] in functions that use "list" as
     a local variable.
"""

from __future__ import annotations

import argparse
import ast
import hashlib
import io
import json
import pathlib
import sys
import warnings

# Load order of the original host: uiMain.pyc runs MyExecFile for the support
# modules, and UI.AllScreen lists the screen modules loaded on demand. uiMain
# names the three data tables by their .py source; their .pyc files are stale
# leftovers from 2007 (the patched client updates only the sources).
SOURCES = (
    "res/uiRes/uiMain.pyc",
    "res/uiRes/uiConst.pyc",
    "res/uiRes/uiTemplate.pyc",
    "object/itemCFG.py",
    "object/commodityCFG.py",
    "map/mapDesc.py",
    "res/uiRes/uiFunc.pyc",
    "res/uiRes/levelCFG.pyc",
    "res/uiRes/uiLogo.pyc",
    "res/uiRes/uiLogin.pyc",
    "res/uiRes/uiSelSect.pyc",
    "res/uiRes/uiSelRoom.pyc",
    "res/uiRes/uiRoom.pyc",
    "res/uiRes/uiShop.pyc",
    "res/uiRes/uiWeb.pyc",
    "res/uiRes/uiGame.pyc",
)

DIVISION_HELPER = "qqt_py2_div"
CMP_KEY_HELPER = "qqt_py2_cmp_key"
COMPARE_HELPER = "qqt_py2_compare"
LEN_HELPER = "qqt_py2_len"
ORDERING = (ast.Lt, ast.LtE, ast.Gt, ast.GtE)
DECOMPILER_FAILURE_MARKERS = ("Parse error", "--- This code section failed", "Deparsing stopped")


def disable_python2_constant_collections() -> None:
    """Work around uncompyle6 3.9.3 printing constant indexes for long lists.

    For five or more constants in a row the scanner folds LOAD_CONSTs into a
    constant collection, and n_const_list writes each element's `attr`. The
    Python 2 scanner keeps the co_consts index there (the value is in `pattr`),
    so ['random', 'boy', ...] came out as [12, 13, ...]. Without the folding
    the list is decompiled element by element, which is correct.
    """
    from uncompyle6.scanner import Scanner

    if getattr(Scanner.bound_collection_from_tokens, "qqt_patched", False):
        return
    original = Scanner.bound_collection_from_tokens

    def bound_collection_from_tokens(self, tokens, t, i, collection_type):
        if self.version[0] < 3:
            return None
        return original(self, tokens, t, i, collection_type)

    bound_collection_from_tokens.qqt_patched = True
    Scanner.bound_collection_from_tokens = bound_collection_from_tokens


def decompile(path: pathlib.Path) -> str:
    from uncompyle6.main import decompile_file

    disable_python2_constant_collections()
    out = io.StringIO()
    decompile_file(str(path), out)
    source = out.getvalue()
    for marker in DECOMPILER_FAILURE_MARKERS:
        if marker in source:
            raise RuntimeError(f"{path}: decompiler reported {marker!r}")
    return source


def to_python3_syntax(source: str, name: str) -> str:
    with warnings.catch_warnings():
        warnings.simplefilter("ignore", DeprecationWarning)
        from lib2to3.refactor import RefactoringTool, get_fixers_from_package

        tool = RefactoringTool(get_fixers_from_package("lib2to3.fixes"))
        return str(tool.refactor_string(source + "\n", name))


class Python2Semantics(ast.NodeTransformer):
    def __init__(self) -> None:
        self.undecodable: list[bytes] = []

    def visit_Constant(self, node: ast.Constant) -> ast.AST:
        if not isinstance(node.value, bytes):
            return node
        try:
            text = node.value.decode("gbk")
        except UnicodeDecodeError:
            self.undecodable.append(node.value)
            text = node.value.decode("latin-1")
        return ast.copy_location(ast.Constant(text), node)

    def visit_BinOp(self, node: ast.BinOp) -> ast.AST:
        self.generic_visit(node)
        if not isinstance(node.op, ast.Div):
            return node
        call = ast.Call(ast.Name(DIVISION_HELPER, ast.Load()), [node.left, node.right], [])
        return ast.copy_location(call, node)

    def visit_AugAssign(self, node: ast.AugAssign) -> ast.AST:
        self.generic_visit(node)
        if not isinstance(node.op, ast.Div):
            return node
        if not isinstance(node.target, ast.Name):
            raise RuntimeError(f"line {node.lineno}: '/=' on a non-name target is not supported")
        load = ast.Name(node.target.id, ast.Load())
        call = ast.Call(ast.Name(DIVISION_HELPER, ast.Load()), [load, node.value], [])
        return ast.copy_location(ast.Assign([node.target], call), node)

    def visit_Compare(self, node: ast.Compare) -> ast.AST:
        # Python 2 orders mixed types (None < 1); a single <, <=, >, >= goes
        # through the host's qqt_py2_compare. Chained comparisons are left
        # alone.
        self.generic_visit(node)
        if len(node.ops) != 1 or not isinstance(node.ops[0], ORDERING):
            return node
        call = ast.Call(ast.Name(COMPARE_HELPER, ast.Load()), [node.left, node.comparators[0]], [])
        return ast.copy_location(ast.Compare(call, node.ops, [ast.Constant(0)]), node)

    def visit_FunctionDef(self, node: ast.FunctionDef) -> ast.AST:
        # 2to3 wraps dict views in list(...), which breaks in a function that
        # uses "list" as a local variable; build those lists with [*x].
        self.generic_visit(node)
        assigned = {target.id for statement in ast.walk(node) if isinstance(statement, ast.Assign)
                    for target in statement.targets if isinstance(target, ast.Name)}
        if "list" in assigned:
            ShadowedList().visit(node)
        return node

    def visit_Call(self, node: ast.Call) -> ast.AST:
        self.generic_visit(node)
        # Python 2 strings are GBK bytes: len() counts bytes, which the scripts
        # use as the text width in half-width cells (e.g. len(name) * 6).
        if isinstance(node.func, ast.Name) and node.func.id == "len" and len(node.args) == 1 and not node.keywords:
            node.func = ast.copy_location(ast.Name(LEN_HELPER, ast.Load()), node.func)
            return node
        # Python 2 list.sort(cmp) takes a comparison function positionally.
        if isinstance(node.func, ast.Attribute) and node.func.attr == "sort" and len(node.args) == 1 and not node.keywords:
            key = ast.Call(ast.Name(CMP_KEY_HELPER, ast.Load()), [node.args[0]], [])
            node.args = []
            node.keywords = [ast.keyword("key", key)]
        return node


class ShadowedList(ast.NodeTransformer):
    def visit_Call(self, node: ast.Call) -> ast.AST:
        self.generic_visit(node)
        if isinstance(node.func, ast.Name) and node.func.id == "list" and len(node.args) == 1 and not node.keywords:
            return ast.copy_location(ast.List([ast.Starred(node.args[0], ast.Load())], ast.Load()), node)
        return node


def convert(path: pathlib.Path, relative: str) -> tuple[str, list[bytes]]:
    python2 = path.read_bytes().decode("gbk") if path.suffix.lower() == ".py" else decompile(path)
    python3 = to_python3_syntax(python2, relative)
    tree = ast.parse(python3, relative)
    # uncompyle6 spells out the module code object's implicit "return None".
    tree.body = [statement for statement in tree.body if not (isinstance(statement, ast.Return) and statement.value is None)]
    semantics = Python2Semantics()
    tree = ast.fix_missing_locations(semantics.visit(tree))
    output = (
        f"# Generated from {relative} by client/native/tools/prepare_ui.py. Do not edit or commit.\n"
        + ast.unparse(tree)
        + "\n"
    )
    compile(output, relative, "exec")
    return output, semantics.undecodable


def find(client_root: pathlib.Path, relative: str) -> pathlib.Path:
    candidate = client_root / relative
    if candidate.is_file():
        return candidate
    # The client's file system is case-insensitive; match the same way.
    current = client_root
    for part in pathlib.PurePosixPath(relative).parts:
        matches = [child for child in current.iterdir() if child.name.lower() == part.lower()]
        if not matches:
            raise FileNotFoundError(f"{relative} not found under {client_root}")
        current = matches[0]
    return current


def main() -> int:
    parser = argparse.ArgumentParser(description=__doc__.splitlines()[0])
    parser.add_argument("--client-root", required=True, type=pathlib.Path, help="client directory containing Client.exe")
    parser.add_argument("--output", required=True, type=pathlib.Path, help="directory for the generated Python 3 scripts")
    args = parser.parse_args()

    if sys.version_info[:2] != (3, 12):
        parser.error("run through prepare-ui.sh (Python 3.12 is required for uncompyle6 and lib2to3)")
    client_root = args.client_root.resolve()
    if not (client_root / "Client.exe").is_file():
        parser.error(f"{client_root} does not contain Client.exe")

    import uncompyle6
    import xdis

    manifest = {
        "client_root": str(client_root),
        "uncompyle6": uncompyle6.__version__,
        "xdis": xdis.__version__,
        "scripts": [],
    }
    for relative in SOURCES:
        source = find(client_root, relative)
        output, undecodable = convert(source, relative)
        target = args.output / pathlib.PurePosixPath(relative).with_suffix(".py")
        target.parent.mkdir(parents=True, exist_ok=True)
        target.write_text(output, encoding="utf-8")
        manifest["scripts"].append({
            "source": relative,
            "sha256": hashlib.sha256(source.read_bytes()).hexdigest(),
            "output": target.relative_to(args.output).as_posix(),
            "lines": output.count("\n"),
            "undecodable_strings": len(undecodable),
        })
        print(f"{relative}: {output.count(chr(10))} lines" + (f", {len(undecodable)} non-GBK strings" if undecodable else ""))
    (args.output / "manifest.json").write_text(json.dumps(manifest, ensure_ascii=False, indent=2) + "\n", encoding="utf-8")
    return 0


if __name__ == "__main__":
    sys.exit(main())
