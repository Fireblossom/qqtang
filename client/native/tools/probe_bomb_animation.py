"""Execute real v848 bubble initialization, update, landing and draw.

Usage: .venv/bin/python tools/probe_bomb_animation.py CLIENT_ROOT
Reuses the native image-wrapper fixture. Optional cosmetic effects and map
coverage/dimensions are controlled; flight motion is replaced by one landing
step, keeping the real bubble update/landing/picture code under observation.
"""

import argparse
import json
import pathlib

from probe_sculpture_animation import picture_fixture


def run(root):
    e, pictures, draws, patch = picture_fixture(root)
    patch(0x5CB087, "resource_registry", lambda e: 0, 0)
    patch(0x4255D0, "optional_effect", lambda e: e.write(e.arg(2), b"\0"), 0)
    patch(0x5D865C, "covering_element", lambda e: 0, 2)
    patch(0x5C378F, "columns", lambda e: 15, 0)
    patch(0x5C3795, "rows", lambda e: 13, 0)
    flight = e.make_object("flight", 6)

    def position(e):
        e.w32(e.arg(0), 140)
        e.w32(e.arg(1), 220)
        return 0

    e.handlers["flight+0x8"] = (lambda e: 0), 1
    e.handlers["flight+0xc"] = position, 2
    e.handlers["flight+0x14"] = (lambda e: 1), 0
    e.handlers["flight+0x0"] = (lambda e: 0), 1

    for number in (1, 2, 9, 11, 12, 13, 14):
        bubble = e.malloc(0x44)
        e.call(0x5DF3D1, (number, 0, 0), ecx=bubble)
        e.call(0x5DF496, ecx=bubble)
        e.call(0x5DF590, (5, 3), ecx=bubble)
        e.w32(bubble + 0x34, e.malloc(0x100))
        picture = e.r32(bubble + 0x14)
        host = pictures[picture]["host"]

        def emit(case):
            draws.clear()
            e.call(0x5DF543, ecx=bubble)
            print(json.dumps(dict(style=number, case=case, canvas=pictures[picture]["canvas"], frame=e.r32(host + 0x2C),
                                  remainder=e.r32(host + 0x30), state=e.read(bubble + 0x2C, 1)[0],
                                  anchor=[e.r32(picture + 0x1C), e.r32(picture + 0x20)],
                                  position=[e.r32(host + 8), e.r32(host + 12)],
                                  draws=[pictures[p]["number"] for p in draws])))

        emit("created")
        for delta, case in ((149, "149"), (150, "299"), (1, "300"), (300, "600")):
            e.call(0x5DF518, (delta,), ecx=bubble)
            emit(case)
        e.write(bubble + 0x2C, b"\x03")
        e.w32(bubble + 0x18, flight)
        e.call(0x5DF518, (300,), ecx=bubble)
        emit("landing_frame")
        e.call(0x5DF518, (300,), ecx=bubble)
        emit("after_landing")


if __name__ == "__main__":
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("client_root", type=pathlib.Path)
    run(parser.parse_args().client_root)
