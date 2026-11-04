"""Execute v848 sculpture deposit/removal, image updates and draw placement.

Usage: .venv/bin/python tools/probe_sculpture_animation.py CLIENT_ROOT
Uses native element/image-wrapper constructors, default image-data constructor,
action changes and animation updates. Graphics initialization supplies hosts
with real asset frame counts. No original game file is changed.
"""

import argparse
import json
import pathlib
import struct
import sys

sys.path.insert(0, str(pathlib.Path(__file__).resolve().parents[1]))
from qqtclient.resources import Resources
from win32emu import Win32Emu
from unicorn.x86_const import UC_X86_REG_ECX


def picture_fixture(root, draw_snapshots=None):
    e = Win32Emu(root / "Client.exe", root)
    resources = Resources(root)

    def patch(address, name, function, pop):
        stub = e.add_stub(name)
        e.handlers[name] = function, pop
        e.write(address, b"\xe9" + struct.pack("<i", stub - address - 5))

    e.handlers["__dllonexit"] = (lambda e: e.arg(0)), 0
    e.w32(0x85DA74, e.r32(0x85DA74) | 1)
    asset = e.malloc(0x48)
    e.call(0x487360, ecx=asset)
    template = e.read(asset, 0x48)
    counts, pictures, draws = {}, {}, []

    def initialize_picture(e):
        picture = e.uc.reg_read(UC_X86_REG_ECX)
        number = (e.r32(picture + 8) >> 8) & 0xFFFF
        name = f"host{picture}"
        host = e.make_object(name, 32, size=0x80)
        e.w32(picture + 4, host)
        table = e.r32(host)
        e.w32(table + 4, 0x42A680)
        e.w32(table + 0xC, 0x42A7E0)
        e.w32(table + 0x10, 0x42A9E0)  # actual picture render-property setter
        e.handlers[name + "+0x0"] = (lambda e: 0), 1
        def draw(e):
            draws.append(picture)
            if draw_snapshots is not None:
                draw_snapshots.append(dict(number=number,
                    position=[struct.unpack("<i", e.read(host + offset, 4))[0] for offset in (8, 12)],
                    frame=e.r32(host + 0x2C), property=e.r32(host + 0x24), value=e.r32(host + 0x28)))

        e.handlers[name + "+0x8"] = draw, 0
        e.w32(host + 0x1C, 1)
        e.w32(host + 0x38, 0xFFFFFFFF)
        category = "bomb/bomb" if e.r32(picture + 8) >> 24 == 3 else "misc/misc"
        canvas = None
        for action, kind in ((1, "stand"), (2, "trigger")):
            raw = resources.read(f"object/{category}{number}_{kind}.img")
            if not raw:
                continue
            if action == 1:
                canvas = list(struct.unpack_from("<II", raw, 32))
            data = e.malloc(0x48)
            e.write(data, template)
            counts[data] = struct.unpack_from("<I", raw, 16)[0]
            e.w32(host + 0x40 + action * 4, data)
        pictures[picture] = dict(number=number, host=host, canvas=canvas)
        return int(e.r32(host + 0x44) != 0)

    patch(0x5C46DB, "initialize_picture", initialize_picture, 0)
    patch(0x427250, "frame_count", lambda e: counts[e.uc.reg_read(UC_X86_REG_ECX)], 0)
    return e, pictures, draws, patch


def run(root):
    e, pictures, draws, patch = picture_fixture(root)
    patch(0x5C37AF, "unused_config", lambda e: 0, 0)
    patch(0x5C3B44, "unused_element_definition", lambda e: 0, 1)

    def create(first):
        statue = e.malloc(0x90)
        e.call(0x5F5D4B, (first, 1), ecx=statue)
        e.call(0x5F5E09, ecx=statue)
        e.call(0x5F63C7, (11, 3), ecx=statue)
        return statue

    def emit(statue, case):
        draws.clear()
        e.call(0x5F639E, ecx=statue)

        def picture_state(picture):
            if not picture:
                return None
            metadata = pictures[picture]
            host = metadata["host"]
            return dict(number=metadata["number"], action=e.r32(host + 0x1C),
                        frame=e.r32(host + 0x2C), remainder=e.r32(host + 0x30),
                        anchor=[e.r32(picture + 0x1C), e.r32(picture + 0x20)],
                        position=[e.r32(host + 8), e.r32(host + 12)])

        print(json.dumps(dict(case=case, progress=e.r32(statue + 0x5C), state=e.r32(statue + 0xC),
                              left=e.r32(statue + 0x70),
                              images=[picture_state(e.r32(statue + offset)) for offset in (0x6C, 0x60, 0x64)],
                              draws=[pictures[p]["number"] for p in draws])))

    def deposit(statue, material, role=3):
        e.call(0x5F5EB8, (material, role), ecx=statue)

    def tick(statue, milliseconds, case):
        e.call(0x5F634C, (milliseconds,), ecx=statue)
        emit(statue, case)

    statue = create(True)
    deposit(statue, 12)
    emit(statue, "deposit1")
    for delta, case in ((299, "299"), (1, "300"), (300, "600"), (300, "900"), (1099, "1999"), (1, "2000")):
        tick(statue, delta, case)
    deposit(statue, 12)
    tick(statue, 600, "deposit2_600")
    e.call(0x5F61CA, ecx=statue)
    emit(statue, "remove_to1")
    tick(statue, 500, "remove_500")
    e.call(0x5F61CA, ecx=statue)
    tick(statue, 20, "remove_to0")
    for material in (12, 16, 13, 12):
        deposit(statue, material)
    emit(statue, "complete16")
    tick(statue, 300, "hero300")
    tick(statue, 300, "hero600")
    tick(statue, 1400, "complete2000")
    for case, materials, role in (
        ("normal_role1", [12] * 4, 1), ("normal_role9", [12] * 4, 9),
        ("normal_role18", [12] * 4, 18), ("normal_role21", [12] * 4, 21),
        ("material13", [12, 13, 16, 12], 3), ("material14", [12, 14, 13, 12], 3),
        ("material15", [15, 12, 16, 12], 3),
    ):
        statue = create(False)
        for material in materials:
            deposit(statue, material, role)
        emit(statue, case)


if __name__ == "__main__":
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("client_root", type=pathlib.Path)
    run(parser.parse_args().client_root)
