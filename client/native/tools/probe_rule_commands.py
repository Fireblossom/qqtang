"""Run native rule command registration; capture actual command virtuals.

Registration storage is intercepted after constructors. Map dimensions are
controlled 15x13 inputs; constructors and category getters run unchanged.
"""

import argparse
import json
import pathlib
import struct

from win32emu import Win32Emu


def run(root):
    e = Win32Emu(root / "Client.exe", root)
    e.handlers["__dllonexit"] = (lambda e: e.arg(0)), 0
    e.w32(0x85DA74, e.r32(0x85DA74) | 1)
    scene = e.malloc(0x6000)
    e.w32(0x806A78 + 0x7D4, scene)

    def patch(address, name, function, pop):
        stub = e.add_stub(name)
        e.handlers[name] = function, pop
        e.write(address, b"\xe9" + struct.pack("<i", stub - address - 5))

    patch(0x5C378F, "width", lambda e: 15, 0)
    patch(0x5C3795, "height", lambda e: 13, 0)
    commands = []

    def register(e):
        command = e.arg(0)
        e.w32(command + 4, scene)
        commands.append(command)
        return 1

    patch(0x5F4350, "register", register, 1)
    for rule_id, setup in ((1, 0x5F4D57), (8, 0x609DF9)):
        commands.clear()
        rule = e.call(0x5F47A9, (scene, rule_id))
        e.w32(scene + 0x4EA4, rule)
        result = e.call(setup, ecx=rule) & 255
        records = []
        for command in commands:
            table = e.r32(command)
            category = e.call(e.r32(table + 4), ecx=command)
            records.append(dict(vtable=hex(table), category=category, execute=hex(e.r32(table + 8))))
        print(json.dumps(dict(rule=rule_id, result=result, commands=records)))


if __name__ == "__main__":
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("client_root", type=pathlib.Path)
    run(parser.parse_args().client_root)
