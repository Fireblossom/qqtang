"""Run v848's battle keyboard poll, combo recognizer and charge classifier.

Usage: .venv/bin/python tools/probe_machine_skill.py CLIENT_ROOT > tests/fixtures/machine_skill_v848.jsonl

Executes the real input object (0x005A49B8 constructor, 0x005A4C52 poll with
its recognizer 0x005CAF0B / 0x005CAE1D at +0x184) and 0x005A53DE in Unicorn,
on a scene whose rule object says 7. Each case starts with the reset
0x005A5423 that the local player's 0x115B performs. Only DirectInput's device state,
GetAsyncKeyState and the scene clock (+0x4B48) are supplied. Each scripted
20 ms frame prints the classifier's value and the recognizer's skill (+0x1F8).
tests/test_machine_skill.py replays the same inputs through machine_skill.py.
"""

import argparse
import json
import pathlib
import struct
import sys

sys.path.insert(0, str(pathlib.Path(__file__).resolve().parent))
from win32emu import Win32Emu  # noqa: E402

# Keyboard-layout slots 1..5 and the DirectInput scan codes of the default keys.
SCAN = {1: 0xCB, 2: 0xCD, 3: 0xC8, 4: 0xD0, 5: 0x39}
DEFAULT_VK = {1: 0x25, 2: 0x27, 3: 0x26, 4: 0x28, 5: 0x20}
L, R, U, D = 1, 2, 3, 4


def scenarios():
    """(name, frames): each frame is (bubble held, direction keys held)."""
    def hold(frames, keys=()):
        return [(True, tuple(keys))] * frames

    def tap(key, down=2, up=2):
        return hold(down, (key,)) + hold(up)

    idle = [(False, ())] * 3
    out = []
    for number, combo in enumerate(((D, L, D, L), (D, R, D, R), (R, L), (L, R), (L, D, R), (R, D, L), (R, R), (L, L))):
        frames = idle + hold(103)  # past the 2000..2050 ms arming window
        for key in combo:
            frames += tap(key)
        out.append((f"combo{number}", frames + hold(10)))
    out.append(("early_keys", idle + hold(40) + tap(R) + tap(L) + hold(60) + tap(R) + tap(L) + hold(10)))
    out.append(("slow_gap", idle + hold(103) + tap(R) + hold(16) + tap(L) + tap(L) + hold(5)))
    out.append(("held_through_arming", idle + hold(98) + hold(10, (D,)) + tap(L) + tap(D) + tap(L) + hold(4)))
    out.append(("too_late", idle + hold(150) + tap(R) + tap(L) + hold(5) + [(False, ())] * 3 + hold(60)))
    out.append(("released_early", idle + hold(60) + [(False, ())] * 2 + hold(110) + tap(R) + tap(R) + hold(5)))
    out.append(("wrong_then_right", idle + hold(103) + tap(U) + tap(R) + tap(L) + tap(L) + hold(5)))
    return out


def run(root):
    e = Win32Emu(root / "Client.exe", root)
    e.handlers["__dllonexit"] = (lambda e: e.arg(0)), 0
    e.w32(0x85DA74, e.r32(0x85DA74) | 1)  # 0x005A1DE1 returns 0x806A78 as is
    keyboard = 0x806A78 + 0x5C8
    e.call(0x5A49B8, ecx=keyboard)
    for slot, vk in DEFAULT_VK.items():
        e.call(0x5A536D, (slot, vk), ecx=keyboard)
    scene, rule = e.malloc(0x6000), e.malloc(0x20)
    e.write(scene, bytes(0x6000))
    e.w32(rule + 8, 7)
    e.w32(scene + 0x4EA4, rule)
    e.w32(0x806A78 + 0x7D4, scene)
    device = e.make_object("keyboard", 16)
    e.w32(keyboard + 8, device)
    state = {"bubble": False, "keys": ()}

    def device_state(e):
        raw = bytearray(256)
        for key in state["keys"]:
            raw[SCAN[key]] = 0x80
        if state["bubble"]:
            raw[SCAN[5]] = 0x80
        e.write(e.arg(2), raw)
        return 0

    e.handlers["keyboard+0x24"] = device_state, 3
    e.handlers["GetAsyncKeyState"] = (lambda e: 0x8000 if state["bubble"] and e.arg(0) == DEFAULT_VK[5] else 0), 1
    now = 10_000
    for name, frames in scenarios():
        # A matched skill stays until 0x115B (or the sender) resets it:
        # 0x005A5423, as for the local player's notice.
        e.call(0x5A5423, ecx=keyboard)
        for bubble, keys in frames:
            now += 20
            e.w32(scene + 0x4B48, now)
            state["bubble"], state["keys"] = bubble, keys
            e.call(0x5A4C52, ecx=keyboard)
            value = struct.unpack("<i", struct.pack("<I", e.call(0x5A53DE, ecx=keyboard)))[0]
            skill = struct.unpack("<i", e.read(keyboard + 0x1F8, 4))[0]
            print(json.dumps(dict(case=name, time=now, bubble=bubble, keys=list(keys), value=value, skill=skill)))


if __name__ == "__main__":
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("client_root", type=pathlib.Path)
    run(parser.parse_args().client_root)
