"""Prepare and control an isolated original-client reference on macOS.

CrossOver is a separately licensed runtime. --fetch-runtime downloads its
official trial; it does not purchase, register, or extend the trial.
"""

import argparse
import hashlib
import json
import os
from pathlib import Path
import re
import shutil
import subprocess
import sys
import time

NATIVE = Path(__file__).resolve().parents[1]
REPO = NATIVE.parents[1]
SUPPORT = Path(__file__).with_suffix("")
VERSION = "26.3.0"
URL = f"https://media.codeweavers.com/pub/crossover/cxmac/demo/crossover-{VERSION}.zip"
SHA256 = "8688e0848c4e5f79f1cc351cb52d32447da00c6c00cfd3b4bb2d164d44589a26"


def windows_path(path):
    return "Z:" + str(Path(path).resolve()).replace("/", "\\")


class Reference:
    def __init__(self, args):
        self.args = args
        self.root = args.root.resolve()
        self.app = (args.crossover_app or self.root / "tools/CrossOver.app").resolve()
        self.bin = self.app / "Contents/SharedSupport/CrossOver/bin"
        self.client = self.root / "client"
        self.logs = self.root / "logs"
        self.logs.mkdir(parents=True, exist_ok=True)
        self.env = os.environ | {
            "CX_BOTTLE_PATH": str(self.root / "bottles"),
            "LANG": "zh_CN.UTF-8", "LC_ALL": "zh_CN.UTF-8", "WINEDEBUG": "-all",
        }

    def wine_args(self, program, *args):
        return [str(self.bin / "wine"), "--bottle", "QQTangReference", str(program), *map(str, args)]

    def wine(self, program, *args, text=None, timeout=45):
        result = subprocess.run(self.wine_args(program, *args), env=self.env, input=text,
                                text=True, capture_output=True, timeout=timeout)
        if result.returncode:
            raise RuntimeError(result.stderr.strip() or f"Wine exited {result.returncode}")
        return result.stdout

    def session(self):
        return json.loads((self.root / "session.json").read_text())

    def window(self, session):
        output = self.wine(self.root / "helpers/window-control.exe", "-pid", session["pid"])
        return next((w for w in json.loads(output) if w["class"] == "QQTangWinClass"), None)

    def screen_info(self, request=False):
        env = self.env | {
            "CLANG_MODULE_CACHE_PATH": str(self.root / "cache/clang"),
            "SWIFT_MODULECACHE_PATH": str(self.root / "cache/swift"),
        }
        command = ["swift", str(SUPPORT / "screen_info.swift")]
        if request:
            command.append("--request")
        return json.loads(subprocess.check_output(command, env=env, text=True))

    def prepare(self):
        if self.args.fetch_runtime and not self.app.exists():
            archive = self.root / f"downloads/crossover-{VERSION}.zip"
            archive.parent.mkdir(parents=True, exist_ok=True)
            if not archive.exists():
                subprocess.run(["curl", "--fail", "--location", "--retry", "2", "--output", str(archive), URL], check=True)
            with archive.open("rb") as f:
                digest = hashlib.file_digest(f, "sha256").hexdigest()
            if digest != SHA256:
                raise RuntimeError("CrossOver archive does not match the pinned official package")
            self.app.parent.mkdir(parents=True, exist_ok=True)
            subprocess.run(["ditto", "-xk", str(archive), str(self.app.parent)], check=True)
        if not (self.bin / "wine").is_file():
            raise RuntimeError("CrossOver not found: supply --crossover-app or --fetch-runtime")
        if not self.args.client_root:
            raise RuntimeError("prepare requires --client-root pointing to client-patched")
        source = self.args.client_root.resolve()
        if source == self.client:
            raise RuntimeError("--client-root must be a separate source tree")
        if not self.client.exists():
            subprocess.run(["cp", "-cR", str(source), str(self.client)], check=True)
        self.patch_endpoints()
        shutil.copy2(self.client / "config/combineforge.ini", self.client / "config/combineforge.xml")
        config = self.client / "config/GameCFG.ini"
        data, count = re.subn(rb"(?m)^(limitfps\s*=\s*)\d+", rb"\g<1>60", config.read_bytes())
        if count != 1:
            raise RuntimeError("GameCFG.ini has no unique limitfps setting")
        config.write_bytes(data)
        go = self.args.go or shutil.which("go")
        if not go:
            candidate = Path.home() / "sdk/go1.26.10/bin/go"
            go = str(candidate) if candidate.is_file() else None
        if not go:
            raise RuntimeError("Go compiler not found; supply --go")
        helpers = self.root / "helpers"
        helpers.mkdir(exist_ok=True)
        env = self.env | {"GOOS": "windows", "GOARCH": "386", "CGO_ENABLED": "0",
                          "GOTOOLCHAIN": "local", "GOCACHE": str(self.root / "cache/go")}
        for target, source_path in (("qqt-launch.exe", "./cmd/qqt-launch"),
                                    ("qqt-login-immediate.exe", "./cmd/qqt-login-immediate"),
                                    ("window-control.exe", str(SUPPORT / "window_control.go"))):
            subprocess.run([go, "build", "-o", str(helpers / target), source_path], cwd=REPO, env=env, check=True)
        trace = self.args.trace or source.parent.parent / "configs/sso-message-trace-stable.json"
        shutil.copy2(trace, helpers / "sso-message-trace.json")
        if not (self.root / "bottles/QQTangReference/cxbottle.conf").exists():
            (self.root / "bottles").mkdir(exist_ok=True)
            with (self.logs / "bottle-create.txt").open("w") as f:
                subprocess.run([str(self.bin / "cxbottle"), "--bottle", "QQTangReference", "--create",
                                "--template", "win10_64"], env=self.env, stdout=f, stderr=f, check=True)
        print(json.dumps({"prepared": str(self.root), "runtime": str(self.app), "client_source": str(source)}))

    def patch_endpoints(self):
        endpoints = {"DirCfg.ini": ("DirIP",), "p2psvrInfo.ini": ("tcpip", "udpip", "stunip"),
                     "caserver.ini": ("ip", "ip2"), "webserver.ini": ("ip", "ip2")}
        ports = {18000: self.args.game_port, 18080: self.args.directory_port,
                 18443: self.args.compat_port, 17000: self.args.ca_port}
        for name, keys in endpoints.items():
            path = self.client / name
            data = path.read_bytes()
            for key in keys:
                data = re.sub(rb"(?mi)^" + key.encode() + rb"=[^\r\n]*", key.encode() + b"=127.0.0.1", data)
            for old, new in ports.items():
                data = re.sub(rb"(?mi)^(\w*port\w*=)" + str(old).encode() + rb"(?=\r?$)",
                              lambda m: m[1] + str(new).encode(), data)
            path.write_bytes(data)

    def start(self):
        if (self.root / "session.json").exists() and self.window(self.session()):
            raise RuntimeError("reference client already running; use status or close")
        attempt = self.logs / time.strftime("%Y%m%d-%H%M%S")
        attempt.mkdir()
        launch_file = attempt / "launch.json"
        command = self.wine_args(self.root / "helpers/qqt-launch.exe", "-exe", windows_path(self.client / "Client.exe"),
                                 "-cwd", windows_path(self.client), "-tp-free", "-high-resolution-timer", "-wait", "2s",
                                 "-out", windows_path(launch_file))
        with (attempt / "launch.stdout.txt").open("w") as out, (attempt / "launch.stderr.txt").open("w") as err:
            subprocess.run(command, env=self.env, stdout=out, stderr=err, check=True, timeout=60)
        result = json.loads(launch_file.read_text())
        if not result.get("running"):
            raise RuntimeError(f"client did not remain running; see {launch_file}")
        ready = attempt / "login.ready.json"
        command = self.wine_args(self.root / "helpers/qqt-login-immediate.exe", "-pid", result["pid"],
                                 "-trace", windows_path(self.root / "helpers/sso-message-trace.json"),
                                 "-auth-address", f"127.0.0.1:{self.args.game_port}", "-attach-timeout", "30s",
                                 "-duration", "0s", "-keep-until-client-exit", "-out", windows_path(attempt / "login.json"),
                                 "-ready", windows_path(ready))
        with (attempt / "login.stdout.txt").open("w") as out, (attempt / "login.stderr.txt").open("w") as err:
            subprocess.Popen(command, env=self.env, stdin=subprocess.DEVNULL, stdout=out, stderr=err, start_new_session=True)
        (self.root / "session.json").write_text(json.dumps({"pid": result["pid"], "ready": str(ready), "attempt": str(attempt)}))
        print(json.dumps({"pid": result["pid"], "logs": str(attempt)}))

    def status(self):
        result = {"screen": self.screen_info()}
        if (self.root / "session.json").exists():
            session = self.session()
            result["window"] = self.window(session)
            ready = Path(session["ready"])
            if ready.exists():
                raw = json.loads(ready.read_text())
                result["login"] = {k: raw.get(k) for k in ("stage", "authenticated", "authenticated_uin", "error")}
        print(json.dumps(result, ensure_ascii=False))

    def control(self, action):
        session = self.session()
        window = self.window(session)
        if not window:
            raise RuntimeError("original client window is not running")
        args = ["-pid", str(session["pid"]), "-hwnd", str(window["hwnd"])]
        if self.args.activate:
            args.append("-activate")
        text = None
        if action == "click":
            args += ["-click", f"{self.args.x},{self.args.y}"]
        elif action == "key":
            args += ["-key", str(self.args.key), "-count", str(self.args.count)]
        elif action == "text":
            args.append("-text-stdin")
            text = sys.stdin.read()
        elif action == "close":
            args.append("-close")
        elif action == "stop":
            args += ["-terminate", "-executable", windows_path(self.client / "Client.exe")]
        self.wine(self.root / "helpers/window-control.exe", *args, text=text)

    def capture(self):
        info = self.screen_info()
        if not info["screen_capture_authorized"]:
            raise RuntimeError("screen recording is not authorized for the application running this command")
        candidates = info["windows"]
        window_id = self.args.cg_window_id
        if window_id is None:
            if len(candidates) != 1:
                raise RuntimeError("use status and provide --cg-window-id to choose one original-client window")
            window_id = candidates[0]["id"]
        elif window_id not in {w["id"] for w in candidates}:
            raise RuntimeError("selected capture window is not a visible Client.exe window")
        output = (self.args.out or self.logs / "original-window.png").resolve()
        output.parent.mkdir(parents=True, exist_ok=True)
        subprocess.run(["screencapture", "-x", "-l", str(window_id), str(output)], check=True)
        print(json.dumps({"window_id": window_id, "output": str(output)}))


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("action", choices=("prepare", "start", "status", "click", "key", "text", "capture", "close", "stop", "request-screen-access"))
    parser.add_argument("--root", type=Path, default=NATIVE / "generated/wine-reference")
    parser.add_argument("--crossover-app", type=Path)
    parser.add_argument("--client-root", type=Path)
    parser.add_argument("--trace", type=Path)
    parser.add_argument("--go")
    parser.add_argument("--fetch-runtime", action="store_true")
    parser.add_argument("--game-port", type=int, default=58000)
    parser.add_argument("--directory-port", type=int, default=58080)
    parser.add_argument("--compat-port", type=int, default=58443)
    parser.add_argument("--ca-port", type=int, default=57000)
    parser.add_argument("--activate", action="store_true", help="also foreground the window and move the host cursor")
    parser.add_argument("--x", type=int, default=0)
    parser.add_argument("--y", type=int, default=0)
    parser.add_argument("--key", type=int, default=0)
    parser.add_argument("--count", type=int, default=1)
    parser.add_argument("--cg-window-id", type=int)
    parser.add_argument("--out", type=Path)
    args = parser.parse_args()
    reference = Reference(args)
    if args.action in ("click", "key", "text", "close", "stop"):
        reference.control(args.action)
    elif args.action == "request-screen-access":
        print(json.dumps(reference.screen_info(request=True)))
    else:
        getattr(reference, args.action)()


if __name__ == "__main__":
    main()
