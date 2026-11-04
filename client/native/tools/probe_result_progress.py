"""Execute result XP updates and raw graphics calls without a D3D device.

XP percentages are controlled inputs, not an inferred GAME_OVER conversion.
The original update and draw functions, including raw image-frame selection,
run unchanged; graphics calls and script notifications are recorded/stubbed.
"""

import argparse
import json
import pathlib
import struct
import sys

sys.path.insert(0, str(pathlib.Path(__file__).resolve().parents[1]))
from qqtclient.dimg import parse
from qqtclient.resources import Resources
from win32emu import Win32Emu


def run(root):
    e = Win32Emu(root / "Client.exe", root)
    resources = Resources(root)
    graphics = e.make_object("graphics", 80)
    e.w32(0x7FA3C0, graphics)
    quads = []
    e.handlers["graphics+0xf0"] = (lambda e: 0), 5

    def draw(e):
        quads.append(dict(texture=e.arg(1), color=e.arg(2), flags=e.arg(3),
                          values=struct.unpack("<20f", e.read(e.arg(0), 80))))
        return 1

    e.handlers["graphics+0x118"] = draw, 4
    stub = e.add_stub("script")
    e.handlers["script"] = (lambda e: 0), 0
    e.write(0x61B9D0, b"\xe9" + struct.pack("<i", stub - 0x61B9D0 - 5))
    result = e.malloc(0x354)
    e.w32(result + 0x2A0, 3)
    for number, offset in ((317, 0x2B8), (462, 0x2BC)):
        sprite = parse(resources.read(f"object/misc/misc{number}_stand.img"))
        frame = sprite.frames[0]
        height, width = frame.pixels.shape[:2]
        image, data = e.malloc(0x90), e.malloc(0x18)
        # The native frame-count getter traverses a 24-byte frame vector.
        e.w32(image + 0x48, data)
        e.w32(image + 0x4C, data + 0x18)
        e.w32(image + 0x84, 1)
        e.w32(data + 8, width)
        e.w32(data + 12, height)
        e.w32(data + 20, number)
        e.w32(result + offset, image)
    for base in (30, 130):
        e.w32(result + 0x350, base)
        for start, target in ((0, 0), (0, 100), (20, 80), (80, 20), (99, 1)):
            for cadence in (17, 20):
                e.write(result + 0x340, struct.pack("<ff", start, target))
                e.w32(result + 0x328, 0)
                for elapsed in range(0, cadence * 81, cadence):
                    if elapsed > 0:
                        e.call(0x613630, (cadence,), ecx=result)
                    if elapsed // cadence not in (0, 1, 25, 50, 80):
                        continue
                    quads.clear()
                    e.call(0x6137BB, ecx=result)
                    current = struct.unpack("<f", e.read(result + 0x340, 4))[0]
                    print(json.dumps(dict(base=base, start=start, target=target,
                                          elapsed=elapsed, cadence=cadence, current=current,
                                          alpha=e.r32(result + 0x328), quads=quads)))


if __name__ == "__main__":
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("client_root", type=pathlib.Path)
    run(parser.parse_args().client_root)
