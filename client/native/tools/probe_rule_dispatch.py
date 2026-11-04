"""Execute v848 rule registration, command dispatch and default actor frames.

Real constructors, category getters, 005F43B1 and 005ABE96 execute. The world
is empty, keyboard idle and movement mode 1. AI entry points are observed
instead of executing a planner; map queries, movement setters and UI/script
dependencies are controlled. This is not a full scene/graphics simulation.
"""

import argparse
import json
import pathlib
import struct

from unicorn.x86_const import UC_X86_REG_ESP

from win32emu import Win32Emu


def run(root):
    e = Win32Emu(root / "Client.exe", root)
    e.handlers["__dllonexit"] = (lambda e: e.arg(0)), 0
    e.w32(0x85DA74, e.r32(0x85DA74) | 1)
    scene, actor, human, empty = [e.malloc(n) for n in (0x6000, 0x1000, 0x1000, 16)]
    e.w32(0x806A78 + 0x7D4, scene)
    ui = e.make_object("ui", 80)
    e.w32(0x7FA410, ui)
    e.handlers["ui+0x44"] = (lambda e: 0), 1
    attrs = e.make_object("attrs", 48, size=0x400)
    e.w32(actor + 0x400, attrs)
    e.write(attrs + 0x2B0, b"\x02")
    e.w32(actor, 0x7A1D2C)
    e.write(actor + 0x328, struct.pack("<H", 20001))
    e.write(human + 0x328, struct.pack("<H", 1))
    e.write(actor + 0x338, struct.pack("<ff", 220, 260))
    e.write(actor + 0x344, struct.pack("<ff", 220, 260))
    e.write(actor + 0x3B4, b"\x03")
    e.write(actor + 0x3B7, b"\x01\x01")
    e.w32(actor + 0x3CC, -5001)
    e.w32(actor + 0x7F0, 2)
    vector = e.malloc(4)
    e.w32(vector, actor)
    commands, events = [], []
    phase = [0, "init"]

    def patch(address, name, function, pop):
        stub = e.add_stub(name)
        e.handlers[name] = function, pop
        e.write(address, b"\xe9" + struct.pack("<i", stub - address - 5))

    def register(e):
        command = e.arg(0)
        e.w32(command + 4, scene)
        commands.append(command)
        return 1

    patch(0x5F4350, "register", register, 1)
    for address, name, function, pop in (
        (0x5C378F, "width", lambda e: 15, 0),
        (0x5C3795, "height", lambda e: 13, 0),
        (0x534AD0, "is_npc", lambda e: int((e.arg(0) & 65535) >= 20000), 1),
        (0x534990, "is_local", lambda e: int((e.arg(0) & 65535) == 1), 1),
        (0x5C0BDA, "movement_mode", lambda e: 1, 0),
        (0x5C07D6, "stop", lambda e: 0, 0),
        (0x5C07FE, "move", lambda e: 0, 1),
        (0x5D865C, "map_element", lambda e: 0, 2),
        (0x5D3BD3, "bomb_vector", lambda e: empty, 2),
        (0x5D9B59, "item_vector", lambda e: empty, 2),
        (0x5E4DE4, "box_touch", lambda e: 0, 2),
        (0x617053, "special_touch", lambda e: 0, 1),
        (0x5B9361, "npc_count", lambda e: 1, 0),
        (0x61B9D0, "script", lambda e: 0, 0),
        (0x5A5391, "keymap", lambda e: 255, 1),
    ):
        patch(address, name, function, pop)
    for address, name in ((0x5B03D4, "combined"), (0x5B050C, "bubble"), (0x5B0C1D, "direction")):
        def observe(e, name=name):
            caller = e.r32(e.uc.reg_read(UC_X86_REG_ESP))
            events.append(dict(entry=name, frame=phase[0], phase=phase[1], caller=hex(caller)))
            return 3
        patch(address, name, observe, 0)

    # Native x87 truncation bridge, including preservation of the control word.
    code = e.malloc(64)
    e.write(code, bytes.fromhex("83 ec 10 d9 3c 24 66 8b 04 24 80 cc 0c 66 89 44 24 02 d9 6c 24 02 df 7c 24 08 d9 2c 24 8b 44 24 08 8b 54 24 0c 83 c4 10 c3"))
    e.write(0x6200CE, b"\xe9" + struct.pack("<i", code - 0x6200CE - 5))
    # Real pickup manager constructor creates its sentinel containers.
    e.call(0x5D3462, ecx=scene + 0x2250)
    e.w32(actor + 0x3FC, scene)
    for offset, value in ((0x37F8, scene), (0x3824, human), (0x3828, actor),
                          (0x3830, human), (0x4B34, human), (0x4B38, actor),
                          (0x4B40, human), (0x264C, vector), (0x2650, vector + 4)):
        e.w32(scene + offset, value)
    state = e.call(0x5C06EB)
    controller = e.malloc(32)
    e.w32(actor + 0x36C, controller)
    e.w32(controller, 0x7A2044)
    e.w32(controller + 4, state)
    e.w32(controller + 12, actor)

    for rule_id, setup in ((1, 0x5F4D57), (8, 0x609DF9)):
        commands.clear()
        rule = e.call(0x5F47A9, (scene, rule_id))
        e.w32(scene + 0x4EA4, rule)
        e.call(setup, ecx=rule)
        groups = {1: [], 2: [], 3: []}
        for command in commands:
            category = e.call(e.r32(e.r32(command) + 4), ecx=command)
            groups[category].append(command)
        manager = scene + 0x4EC0
        e.w32(manager + 4, scene)
        for category, pointers in groups.items():
            data = e.malloc(len(pointers) * 4)
            e.write(data, struct.pack(f"<{len(pointers)}I", *pointers))
            offset = {1: 12, 2: 28, 3: 44}[category]
            e.w32(manager + offset, data)
            e.w32(manager + offset + 4, data + len(pointers) * 4)
        for role in (25, 26, 27):
            for frames in (40, 200):
                events.clear()
                e.write(actor + 0x394, bytes([role, 7, 0]))
                e.w32(controller + 8, 0)
                for command in commands:
                    if e.r32(e.r32(command) + 8) == 0x6112AF:
                        e.write(command + 8, b"\0")
                for frame in range(frames):
                    now = 61000 + frame * 20
                    e.w32(scene + 0x3838, now)
                    e.w32(scene + 0x4B48, now)
                    e.write(actor + 0x3A8, struct.pack("<ff", 220, 260))
                    phase[:] = frame, "command"
                    e.call(0x5F43B1, (actor,), ecx=manager)
                    phase[:] = frame, "actor"
                    e.call(0x5ABE96, (20,), ecx=actor)
                print(json.dumps(dict(rule=rule_id, role=role, changed_cell=False,
                                      frames=frames, dt=20, events=events)))


if __name__ == "__main__":
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("client_root", type=pathlib.Path)
    run(parser.parse_args().client_root)
