"""Report which host natives the converted UI scripts use but the host lacks.

Usage: .venv/bin/python tools/native_coverage.py [--json PATH]

A native is any name the scripts read but never define (not assigned, not a
function/class/parameter/import, not a builtin). It is implemented when
one of NATIVE_MODULES defines a function of that name.
"""

from __future__ import annotations

import argparse
import ast
import builtins
import collections
import json
import pathlib
import re

ROOT = pathlib.Path(__file__).resolve().parent.parent
SCRIPTS = ROOT / "generated" / "scripts"
NATIVE_MODULES = ("qqtclient/natives.py", "qqtclient/session.py", "qqtclient/lobby.py", "qqtclient/room.py", "qqtclient/chat.py", "qqtclient/shop.py",
                  "qqtclient/props.py", "qqtclient/friend.py")
HELPERS = {"qqt_py2_div", "qqt_py2_cmp_key", "qqt_py2_compare", "qqt_py2_len", "unicode", "long", "xrange"}
# Screens in the order a player reaches them; a native is listed under the first.
SCREENS = ("uiLogo", "uiLogin", "uiSelSect", "uiSelRoom", "uiRoom", "uiGame", "uiShop", "uiMain", "uiFunc", "levelCFG", "uiWeb")


def script_natives() -> dict[str, set[str]]:
    used: dict[str, set[str]] = collections.defaultdict(set)
    defined: set[str] = set()
    for path in sorted(SCRIPTS.rglob("*.py")):
        for node in ast.walk(ast.parse(path.read_text(encoding="utf-8"))):
            if isinstance(node, ast.Name):
                if isinstance(node.ctx, ast.Load):
                    used[node.id].add(path.stem)
                else:
                    defined.add(node.id)
            elif isinstance(node, (ast.FunctionDef, ast.ClassDef)):
                defined.add(node.name)
            elif isinstance(node, ast.arg):
                defined.add(node.arg)
            elif isinstance(node, (ast.Import, ast.ImportFrom)):
                defined.update((alias.asname or alias.name).split(".")[0] for alias in node.names)
    return {name: files for name, files in used.items() if name not in defined and name not in HELPERS and not hasattr(builtins, name)}


def implemented() -> set[str]:
    names = {"device"}
    for module in NATIVE_MODULES:
        names |= set(re.findall(r"^    def ([A-Za-z_]\w*)\(", (ROOT / module).read_text(encoding="utf-8"), re.M))
    return names


def main() -> int:
    parser = argparse.ArgumentParser(description=__doc__.splitlines()[0])
    parser.add_argument("--json", type=pathlib.Path, help="also write the missing natives grouped by screen")
    args = parser.parse_args()
    if not SCRIPTS.is_dir():
        parser.error(f"{SCRIPTS} is missing; run tools/prepare-ui.sh first")

    natives = script_natives()
    done = implemented()
    groups: dict[str, list[str]] = collections.defaultdict(list)
    for name, files in natives.items():
        if name in done:
            continue
        group = "Win_*" if name.startswith("Win_") else next((screen for screen in SCREENS if screen in files), sorted(files)[0])
        groups[group].append(name)
    missing = sum(len(names) for names in groups.values())
    print(f"{len(natives)} natives used by the scripts, {len(natives) - missing} implemented, {missing} missing")
    for group in ("Win_*", *SCREENS):
        if group in groups:
            print(f"\n{group} ({len(groups[group])}): {', '.join(sorted(groups[group]))}")
    if args.json:
        args.json.write_text(json.dumps({group: sorted(names) for group, names in groups.items()}, ensure_ascii=False, indent=2) + "\n", encoding="utf-8")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
