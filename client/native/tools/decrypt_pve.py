"""Decrypt the adventure-mode level data by running the original client's reader under Unicorn.

Usage: .venv/bin/python tools/decrypt_pve.py [--client DIR] [--out DIR] [--no-emu] [--verify-client]

Client.exe reads config/battlefield_map<N>.dat and config/pvedata.dat through
the room module (QQTSection.dll), method +0x64:
bool ReadCfg(const char *path, int size, char *out), where size comes from
config/GameCFG.ini ([battlefield_map<N>] mapCfgSize, [pvedata] pvedataCfgSize).
The reader unscrambles the file in place, inflates it with zlib into size
bytes, converts CR LF / CR to LF and copies the text to out.

By default this script runs that reader (QQTSection.dll 0x1002BCDF) under
Unicorn for every file and writes exactly the text Client.exe receives
(GB2312, LF line endings) to OUT/<name>.xml. The pure-Python equivalent
(read_cfg) is always run as well and must match; --no-emu uses only it.

--verify-client also runs Client.exe's own battlefield loader
(CBattleFieldMgr, 0x00892588) on every map, with the room module's +0x64
returning the text produced above, and prints whether the original XML
parser accepted it and the exit-door cell it stored.

Unicorn allocates executable memory, so run it outside the sandbox.
"""

from __future__ import annotations

import argparse
import pathlib
import sys
import zlib

sys.path.insert(0, str(pathlib.Path(__file__).resolve().parent))

ROOT = pathlib.Path(__file__).resolve().parent.parent
DEFAULT_CLIENT = pathlib.Path.home() / "Downloads/QQTang-Local-v1.3.4/QQTang-Local/runtime/client-patched"

SECTION_READ_CFG = 0x1002BCDF  # QQTSection.dll: bool ReadCfg(path, size, out), thiscall
SECTION_DECODE = 0x1000BEC8    # QQTSection.dll: bool decode(char *buf, int len), cdecl, in place

APP_SINGLETON = 0x00806A78     # Client.exe: app object returned by 0x005A1DE1
APP_GUARD = 0x0085DA74         # bit 0 set once the app object is constructed
APP_ROOM_MODULE = 0x7D0        # app + 0x7D0: room module interface (QQTSection.dll)
BFM_CTOR = 0x005E71AF          # CBattleFieldMgr::CBattleFieldMgr(path, scene), thiscall, 0xA8 bytes
BFM_LOAD = 0x00892588          # CBattleFieldMgr load (thiscall, arg = path) -> bool


def decode(data: bytes) -> bytes:
    """Same as QQTSection.dll 0x1000BEC8."""
    b = bytearray(data)
    n = len(b)
    if n:
        b[n - 1] ^= b[0]
        for i in range(n - 2, -1, -1):
            b[i] ^= b[i + 1]
    return bytes(b)


def encode(data: bytes) -> bytes:
    """Same as QQTSection.dll 0x1002BFFD (used when saving data sent by the server)."""
    b = bytearray(data)
    n = len(b)
    if n:
        for i in range(n - 1):
            b[i] ^= b[i + 1]
        b[n - 1] ^= b[0]
    return bytes(b)


def read_cfg(raw: bytes, size: int) -> bytes | None:
    """Same as QQTSection.dll 0x10035AE4: decode, inflate into size bytes, normalise newlines."""
    d = zlib.decompressobj()
    text = d.decompress(decode(raw), size)
    text = text.split(b"\0", 1)[0].replace(b"\r\n", b"\n").replace(b"\r", b"\n")
    return text if len(text) <= size else None


def cfg_sizes(client: pathlib.Path) -> dict[str, int]:
    sizes: dict[str, int] = {}
    section = None
    for line in (client / "config/GameCFG.ini").read_bytes().decode("latin-1").splitlines():
        line = line.strip()
        if line.startswith("["):
            section = line.strip("[]").strip()
        elif "=" in line and section:
            k, v = (s.strip() for s in line.split("=", 1))
            if k in ("mapCfgSize", "pvedataCfgSize"):
                sizes[section] = int(v)
    return sizes


class SectionReader:
    """QQTSection.dll's ReadCfg running under Unicorn."""

    def __init__(self, client: pathlib.Path):
        from win32emu import Win32Emu
        self.emu = Win32Emu(client / "QQTSection.dll", client)
        self.this = self.emu.malloc(0x100)

    def __call__(self, path: bytes, size: int) -> bytes | None:
        emu = self.emu
        p = emu.strdup(path)
        out = emu.malloc(size + 1)
        ok = emu.call(SECTION_READ_CFG, (p, size, out), ecx=self.this) & 0xFF
        text = emu.cstr(out, size + 1) if ok else None
        emu.free(p)
        emu.free(out)
        return text


def verify_client(client: pathlib.Path, maps: list[int], reader) -> int:
    from win32emu import Win32Emu

    emu = Win32Emu(client / "Client.exe", client)
    # Constructing the real app object pulls in the whole service registry; mark it built and
    # give it a room module whose +0x64 is the emulated QQTSection reader.
    emu.w32(APP_GUARD, emu.r32(APP_GUARD) | 1)
    emu.w32(APP_SINGLETON + APP_ROOM_MODULE, emu.make_object("RoomModule"))

    def room_read_cfg(e):
        path, size, out = e.cstr(e.arg(0)), e.arg(1), e.arg(2)
        text = reader(path, size)
        if text is None:
            return 0
        e.write(out, text + b"\0")
        return 1

    emu.handlers["RoomModule+0x64"] = (room_read_cfg, 3)
    scene = emu.malloc(0x10000)
    bad = 0
    for n in maps:
        path = emu.strdup(b"config/battlefield_map%d.dat" % n)
        obj = emu.malloc(0xA8)
        emu.call(BFM_CTOR, (path, scene), ecx=obj)
        ok = emu.call(BFM_LOAD, (path,), ecx=obj) & 0xFF
        bad += not ok
        print(f"  Client.exe CBattleFieldMgr map{n}: {'ok' if ok else 'FAILED'}  "
              f"door row(+0x8C)={emu.r32(obj + 0x8C)} col(+0x90)={emu.r32(obj + 0x90)}")
    return bad


def main() -> int:
    ap = argparse.ArgumentParser(description=__doc__.split("\n\n")[0])
    ap.add_argument("--client", type=pathlib.Path, default=DEFAULT_CLIENT)
    ap.add_argument("--out", type=pathlib.Path, default=ROOT / "generated" / "pve")
    ap.add_argument("--no-emu", action="store_true", help="skip Unicorn, use the pure-Python reader only")
    ap.add_argument("--verify-client", action="store_true", help="also run Client.exe's battlefield loader")
    args = ap.parse_args()

    client = args.client
    files = sorted(client.glob("config/battlefield_map*.dat"), key=lambda p: int(p.stem[len("battlefield_map"):]))
    files.append(client / "config/pvedata.dat")
    sizes = cfg_sizes(client)
    reader = None if args.no_emu else SectionReader(client)
    args.out.mkdir(parents=True, exist_ok=True)

    failures = 0
    texts: dict[bytes, bytes] = {}
    for f in files:
        raw = f.read_bytes()
        size = sizes.get(f.stem)
        if size is None:
            print(f"{f.name}: no size in GameCFG.ini")
            failures += 1
            continue
        text = read_cfg(raw, size)
        if reader is not None:
            emu_text = reader(f"config/{f.name}".encode(), size)
            if emu_text != text:
                print(f"{f.name}: QQTSection reader and pure-Python reader differ")
                failures += 1
                continue
        if text is None:
            print(f"{f.name}: reader failed")
            failures += 1
            continue
        full = len(zlib.decompress(decode(raw)))
        note = "" if full == size else f"  (inflates to {full} bytes; original reads only {size})"
        texts[f"config/{f.name}".encode()] = text
        (args.out / (f.stem + ".xml")).write_bytes(text)
        print(f"{f.name}: {len(raw)} -> {len(text)} bytes{note}")

    if args.verify_client:
        maps = [int(f.stem[len("battlefield_map"):]) for f in files if f.stem.startswith("battlefield")]
        failures += verify_client(client, maps, lambda path, size: texts.get(path))

    print(f"wrote {len(files) - failures} files to {args.out}" + (f", {failures} failures" if failures else ""))
    return 1 if failures else 0


if __name__ == "__main__":
    raise SystemExit(main())
