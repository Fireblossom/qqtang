"""Execute the v848 result controller and record screen drawing commands.

Usage: .venv/bin/python tools/probe_results.py CLIENT_ROOT
Result rows and profile/portrait interfaces are controlled; constructor,
initialization, movement, fade, row/statistic/number layout and image-wrapper
calls are native. Raw XP-bar textures and optional honor icons are excluded.
"""

import argparse
import json
import pathlib
import struct

from probe_sculpture_animation import picture_fixture
from win32emu import Win32Emu


def run(root, honors=False):
    snapshots, texts, portraits = [], [], []
    e, pictures, draws, patch = picture_fixture(root, snapshots)
    code = e.malloc(64)
    e.write(code, bytes.fromhex("83 ec 10 d9 3c 24 66 8b 04 24 80 cc 0c 66 89 44 24 02 "
                               "d9 6c 24 02 df 7c 24 08 d9 2c 24 8b 44 24 08 8b 54 24 0c 83 c4 10 c3"))
    e.write(0x6200CE, b"\xe9" + struct.pack("<i", code - 0x6200CE - 5))
    absolute = e.malloc(16)
    e.write(absolute, bytes.fromhex("dd442404d9e1c3"))
    e.write(0x62038E, b"\xe9" + struct.pack("<i", absolute - 0x62038E - 5))
    patch(0x5CB087, "resource_registry", lambda e: 0, 0)
    patch(0x61B9D0, "script", lambda e: 0, 0)
    graphics = e.make_object("graphics", 80)
    e.w32(0x7FA3C0, graphics)
    e.handlers["graphics+0xf0"] = (lambda e: texts.append(
        [e.arg(0), e.arg(1), e.cstr(e.arg(2)).decode("gbk"), e.arg(3), e.arg(4)])), 5
    portrait = e.make_object("portrait", 16)
    e.handlers["portrait+0xc"] = (lambda e: 0), 0
    e.handlers["portrait+0x18"] = (lambda e: 0), 1
    patch(0x5CB0E9, "portrait_factory", lambda e: portrait, 2)
    patch(0x5DD1C3, "portrait_position", lambda e: portraits.append([e.arg(0), e.arg(1)]), 2)
    patch(0x5DD3DD, "portrait_alpha", lambda e: 0, 1)
    patch(0x5DD365, "portrait_win", lambda e: 0, 0)
    patch(0x5DD38D, "portrait_lose", lambda e: 0, 0)
    patch(0x47F050, "raw_image", lambda e: 0, 0)

    profile_flags = dict(value=0)

    def profile(e):
        destination = e.arg(1)
        e.write(destination, bytes(0x23E0))
        e.w32(destination, 1000005)
        e.w32(destination + 0x20, profile_flags["value"])
        return 1

    patch(0x5A3BE2, "profile", profile, 2)

    def signed(address):
        return struct.unpack("<i", e.read(address, 4))[0]

    def create(rule_id, outcome, remark=0):
        world, scene, actor = [e.malloc(n) for n in (0x60000, 0x6000, 0x800)]
        rule = e.call(0x5F47A9, (scene, rule_id))
        e.w32(world + 0x37F8, scene)
        e.w32(scene + 0x4EA4, rule)
        e.write(actor + 0x394, b"\x01\x01")
        e.w32(world + 0x3824, actor)
        e.w32(world + 0x3828, actor)
        data = e.malloc(0x293)
        e.write(data, struct.pack("<HB", 1, 1))
        row = data + 3
        e.write(row, struct.pack("<H", 1))
        e.write(row + 7, "糖五".encode("gbk") + b"\0")
        e.write(row + 28, bytes([outcome]))
        e.w32(row + 29, remark)
        e.write(row + 49, b"\x04")
        e.write(row + 50, struct.pack("<8I", 2, 3, 100, 7, 52, 63, 2000, 17))
        result = e.malloc(0x354)
        e.call(0x612C86, (world, data), ecx=result)
        portraits.clear()
        if e.call(0x612F3B, ecx=result) & 255 != 1:
            raise RuntimeError("native result initialization failed")
        return result

    def emit(result, rule, outcome, elapsed, cadence, full=False, metadata=None):
        snapshots.clear()
        texts.clear()
        e.call(0x6137BB, ecx=result)
        record = dict(kind="draw" if full else "geometry", rule=rule, outcome=outcome,
                      elapsed=elapsed, cadence=cadence, phase=e.r32(result + 0x2A0),
                      wait=signed(result + 0x324), alpha=e.r32(result + 0x328),
                      lifetime=signed(result + 8))
        if full:
            record.update(images=[dict(number=r["number"], frame=r["frame"], position=r["position"],
                                       alpha=r["value"] if r["property"] == 1 else 255) for r in snapshots],
                          texts=texts, portrait_positions=portraits)
        else:
            picture = e.r32(result + 0x2A8)
            host = pictures[picture]["host"]
            record.update(table=pictures[picture]["number"], position=[signed(host + 8), signed(host + 12)])
        if metadata is not None:
            record.update(metadata)
        print(json.dumps(record))

    if honors:
        for remark, flags in ((101, 0), (201, 0), (701, 0), (101, 0x2000)):
            profile_flags["value"] = flags
            result = create(4, 2, remark)
            for _ in range(215):
                e.call(0x613560, (20,), ecx=result)
            emit(result, 4, 2, 4300, 20, True,
                 dict(kind="honor_draw", remark=remark, profile_flags=flags))
        return

    checkpoints = {0, 3000, 3020, 3040, 3200, 3400, 3600, 3740, 3760, 3800, 4000, 4260}
    for rule in (4, 7, 8, 12):
        result = create(rule, 2)
        emit(result, rule, 2, 0, 20)
        for elapsed in range(20, 4301, 20):
            e.call(0x613560, (20,), ecx=result)
            if elapsed in checkpoints:
                emit(result, rule, 2, elapsed, 20)
            if elapsed == 4000:
                emit(result, rule, 2, elapsed, 20, True)
        emit(result, rule, 2, 4300, 20, True)
    for rule, outcome in ((4, 0), (4, 1), (3, 2), (5, 2)):
        result = create(rule, outcome)
        for _ in range(215):
            e.call(0x613560, (20,), ecx=result)
        emit(result, rule, outcome, 4300, 20, True)
    result = create(4, 2)
    for elapsed in range(17, 4401, 17):
        e.call(0x613560, (17,), ecx=result)
        if elapsed in (3009, 3026, 3043, 3400, 3740, 3757, 4301):
            emit(result, 4, 2, elapsed, 17)
    scene = e.malloc(0x6000)
    for rule_id in (1, 2, 3, 4, 5, 6, 7, 8, 12, 13):
        rule = e.call(0x5F47A9, (scene, rule_id))
        table = e.r32(rule)
        stats = [e.call(e.r32(table + offset), ecx=rule) for offset in (0x34, 0x38)]
        print(json.dumps(dict(kind="rule_stats", rule=rule_id, types=stats)))
    ui = Win32Emu(root / "gameUI.dll", root)
    widget = ui.malloc(0x270)
    for value in (0.0, 0.5, 1.0):
        arguments = (*struct.unpack("<II", struct.pack("<d", value)), 42)
        ui.call(0x10027580, arguments, ecx=widget)
        print(json.dumps(dict(kind="widget_alpha", value=value,
                              stored=struct.unpack("<d", ui.read(widget + 0x158, 8))[0])))


if __name__ == "__main__":
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("client_root", type=pathlib.Path)
    parser.add_argument("--honors", action="store_true")
    arguments = parser.parse_args()
    run(arguments.client_root, arguments.honors)
