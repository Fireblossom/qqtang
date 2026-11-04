"""Emit v848 machine animation checkpoints from the real native component.

Usage: .venv/bin/python tools/probe_machine_animation.py CLIENT_ROOT
Runs constructor/update/draw, readiness and image animation in Unicorn.
Graphics calls are observed, while the DIMG frame-count query uses the actual
resources. No original file is changed and no graphics device is created.
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


def run(root):
    e = Win32Emu(root / "Client.exe", root)
    resources = Resources(root)
    # VC6 _ftol: x87 value -> signed EDX:EAX, rounding toward zero.
    code = e.malloc(64)
    e.write(code, bytes.fromhex("83 ec 10 d9 3c 24 66 8b 04 24 80 cc 0c 66 89 44 24 02 "
                               "d9 6c 24 02 df 7c 24 08 d9 2c 24 8b 44 24 08 8b 54 24 0c 83 c4 10 c3"))
    e.write(0x6200CE, b"\xe9" + struct.pack("<i", code - 0x6200CE - 5))
    e.w32(0x85DA74, e.r32(0x85DA74) | 1)
    machine, world, layer, prop, flags, actor = [e.malloc(n) for n in (0x100, 0x4000, 0x100, 0x30, 8, 0x500)]
    e.call(0x5EF372, (1, 1), ecx=machine)
    e.write(machine + 8, struct.pack("<hh", 6, 3))
    e.w32(machine + 0x1C, layer)
    e.w32(layer + 0x34, world)
    e.w32(machine + 0x14, prop)
    e.w32(prop + 0x18, flags)
    e.write(flags, struct.pack("<II", 0x1010, 0x1010))
    e.w32(world + 0x3824, actor)
    e.w32(world + 0x3828, actor)
    e.write(actor + 0x395, b"\x01")
    images, frame_counts, events, drawn = {}, {}, [], []
    for label, offset, number in (("idle", 0x6C, 163), ("ready", 0x50, 401), ("fire", 0x58, 167), ("arrow", 0x54, 400)):
        picture = e.make_object(label, 16)
        e.w32(machine + offset, picture)
        asset = e.malloc(0x48)
        e.call(0x487360, ecx=asset)  # real period 300ms and looping flag
        raw = resources.read(f"object/misc/misc{number}_stand.img")
        frame_counts[asset] = struct.unpack_from("<I", raw, 16)[0]
        host = e.malloc(0x80)
        e.w32(host + 0x1C, 1)
        e.w32(host + 0x44, asset)
        e.w32(host + 0x48, asset)  # v848 fire stand/trigger resources are identical
        e.w32(host + 0x38, 0xFFFFFFFF)
        images[label] = host
        e.handlers[f"{label}+0x14"] = (lambda e, label=label: events.append(("update", label, e.arg(0))), 1)
        e.handlers[f"{label}+0x20"] = (lambda e, label=label: events.append(("select", label, 2)), 0)
        e.handlers[f"{label}+0xc"] = (lambda e, label=label: drawn.append(label), 0)
    stub = e.add_stub("decoded_frame_count")
    e.handlers["decoded_frame_count"] = (lambda e: frame_counts[e.uc.reg_read(UC_X86_REG_ECX)], 0)
    e.write(0x427250, b"\xe9" + struct.pack("<i", stub - 0x427250 - 5))
    for offset in (0x5C, 0x60):
        projectile = e.malloc(0x28)
        e.call(0x60881F, ecx=projectile)
        e.w32(machine + offset, projectile)
    e.call(0x5EF91F, ecx=machine)  # real geometry/trajectory setup

    def process_images():
        for kind, label, value in events:
            if kind == "update":
                e.call(0x42A680, (value,), ecx=images[label])
            else:
                e.call(0x42A7E0, (value, 0xFFFFFFFF), ecx=images[label])

    def step(clock, delta):
        e.w32(world + 0x3838, clock)
        events.clear()
        drawn.clear()
        e.call(0x5EF7DE, (delta,), ecx=machine)
        updates = list(events)
        process_images()
        e.call(0x5EF91F, ecx=machine)
        return dict(clock=clock, delta=delta, updates=updates, draws=list(drawn),
                    frames={name: [e.r32(p + 0x2C), e.r32(p + 0x30)] for name, p in images.items()},
                    ready=e.read(machine + 0x39, 1)[0], arrow=e.read(machine + 0x38, 1)[0],
                    stored_clock=e.r32(machine + 0x64))

    def fire(clock):
        e.call(0x5EFB18, (clock,), ecx=machine)
        events.clear()
        e.call(0x5EFB2D, ecx=machine)
        process_images()

    checkpoints = {3000, 47980, 48000, 48020, 48300, 48400, 48420, 48440, 48500, 48700,
                   49100, 49200, 49220, 49240, 50800, 51800, 53500, 96840}
    for clock in range(3000, 96841, 20):
        if clock in (48420, 96840):
            fire(clock)
        row = step(clock, 20)
        if clock in checkpoints:
            print(json.dumps(row))
    for clock in (200000, 204000):
        print(json.dumps(step(clock, 4000)))


if __name__ == "__main__":
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("client_root", type=pathlib.Path)
    run(parser.parse_args().client_root)
