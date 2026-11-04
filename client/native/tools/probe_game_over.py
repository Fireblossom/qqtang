"""Execute 00602BAB/00881E00 and capture its 0x293 result-display input.

Controlled dependencies: actor/profile lookup, app singleton and actor result
animation transition. Native field projection and experience arithmetic run.
The display constructor is an observation boundary; no network/server writes.
"""

import argparse
import json
import pathlib
import struct
import pefile

from win32emu import EmuStop, Win32Emu


def observe(root, points, gain, scores, rule_id=1, section_mode=0, remark=0):
    e = Win32Emu(root / "Client.exe", root)
    e.handlers["__dllonexit"] = (lambda e: e.arg(0)), 0
    e.w32(0x85DA74, e.r32(0x85DA74) | 1)
    app, scene, handler, actor, data = [e.malloc(n) for n in (0x60000, 0x60000, 0x100, 0x1000, 358)]

    def patch(address, name, function, pop):
        stub = e.add_stub(name)
        e.handlers[name] = function, pop
        e.write(address, b"\xe9" + struct.pack("<i", stub - address - 5))

    patch(0x5A1DE1, "app", lambda e: app, 0)
    patch(0x5A287F, "actor_lookup", lambda e: actor, 1)
    patch(0x5C09CD, "actor_result_state", lambda e: 0, 0)

    def profile(e):
        destination = e.arg(1)
        e.write(destination, bytes(0x23E0))
        e.w32(destination, 1000005)
        e.w32(destination + 0x34, points)
        return 1

    patch(0x5A3BE2, "profile", profile, 2)
    e.w32(handler + 4, scene)
    e.w32(app + 0x37F8, scene)
    e.w32(scene + 8, app)
    e.w32(scene + 0x4B34, actor)
    e.w32(scene + 0x4B38, actor)
    e.w32(scene + 0x51A4, section_mode)
    e.write(actor + 0x328, struct.pack("<H", 1))
    e.write(actor + 0x394, b"\x01\x01")
    rule = e.call(0x5F47A9, (scene, rule_id))
    e.w32(scene + 0x4EA4, rule)
    e.write(data, struct.pack("<IBHBIIB", 1000, 1, 1, 2, remark, gain, len(scores)))
    e.write(data + 17, struct.pack("<4I", 2, 3, 100, 7))
    e.write(data + 33, struct.pack("<4I", *(scores + [0] * 4)[:4]))
    captured = []

    def capture(e):
        raw = e.read(e.arg(1), 0x293)
        row = raw[3:85]
        captured.append(dict(local_id=struct.unpack_from("<H", raw)[0], count=raw[2],
                             player_id=struct.unpack_from("<H", row)[0], result=row[28],
                             remark=struct.unpack_from("<I", row, 29)[0],
                             score_total=struct.unpack_from("<I", row, 33)[0],
                             level=struct.unpack_from("<I", row, 37)[0],
                             progress=struct.unpack_from("<ff", row, 41), fields=row[49],
                             counts=struct.unpack_from("<4I", row, 50),
                             scores=struct.unpack_from("<4I", row, 66)))
        raise EmuStop("captured")

    patch(0x612C86, "capture", capture, 2)
    try:
        e.call(0x602BAB, (0xFBB, data), ecx=handler)
    except EmuStop:
        if not captured:
            raise
    if len(captured) != 1:
        raise RuntimeError("expected one display constructor")
    print(json.dumps(dict(points=points, gain=gain, scores=scores, rule=rule_id,
                          section_mode=section_mode, remark=remark, display=captured[0])))


if __name__ == "__main__":
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("client_root", type=pathlib.Path)
    root = parser.parse_args().client_root
    pe = pefile.PE(str(root / "Client.exe"))
    thresholds = struct.unpack("<181I", pe.get_data(0x7F7780 - pe.OPTIONAL_HEADER.ImageBase, 724))
    print(json.dumps(dict(kind="thresholds", values=thresholds)))
    for points, gain in ((0, 50), (123, 50), (950, 49), (950, 50), (950, 100),
                         (1000, 50), (4999, 5000), (9999, 1), (10000, 50),
                         (1631150000, 50), (1621150200, 10), (1621151520, 50)):
        observe(root, points, gain, [52, 63, 2000])
    for rule in (1, 2, 3, 4, 5, 6, 7, 8):
        for section_mode in (0, 1, 2):
            observe(root, 950, 100, [10, 20, 30, 40], rule, section_mode, 9)
