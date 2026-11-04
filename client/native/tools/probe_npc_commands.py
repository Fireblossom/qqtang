"""Execute 006112AF NPC movement command scheduling with a controlled AI.

Chat visibility, NPC classification, movement-state query and actor movement
setters are isolated dependencies. Counter/gates and AI virtual dispatch run.
"""

import argparse
import json
import pathlib
import struct

from win32emu import Win32Emu


def run(root):
    e = Win32Emu(root / "Client.exe", root)
    e.w32(0x85DA74, e.r32(0x85DA74) | 1)
    e.w32(0x806A78 + 0x56EE0, 0)
    graphics_ui = e.make_object("ui", 20)
    e.w32(0x7FA410, graphics_ui)
    e.handlers["ui+0x44"] = (lambda e: 0), 1
    actor = e.make_object("actor", 32, size=0x1000)
    command, scene, rule = [e.malloc(n) for n in (16, 0x6000, 16)]
    e.w32(command + 4, scene)
    e.w32(scene + 0x4EA4, rule)
    e.write(actor + 0x328, struct.pack("<H", 20001))
    state = dict(mode=1, direction=0, decisions=0, actions=[])

    def patch(address, name, function, pop):
        stub = e.add_stub(name)
        e.handlers[name] = function, pop
        e.write(address, b"\xe9" + struct.pack("<i", stub - address - 5))

    patch(0x534AD0, "is_npc", lambda e: 1, 1)
    patch(0x5C0BDA, "movement_mode", lambda e: state["mode"], 0)
    patch(0x5C07D6, "stop", lambda e: state["actions"].append(["stop"]) or 0, 0)
    patch(0x5C07FE, "move", lambda e: state["actions"].append(["move", e.arg(0) & 255]) or 0, 1)

    def decide(e):
        state["decisions"] += 1
        return state["direction"]

    e.handlers["actor+0x70"] = decide, 0
    for rule_id in (1, 8):
        e.w32(rule + 8, rule_id)
        for case, direction, mode, elapsed, actor_mode, initial in (
            ("move", 2, 1, 4000, 1, 0), ("stop", 9, 1, 4000, 1, 0),
            ("movement_mode7", 2, 7, 4000, 1, 11), ("movement_mode8", 2, 8, 4000, 1, 11),
            ("countdown2999", 2, 1, 2999, 1, 11), ("countdown3000", 2, 1, 3000, 1, 11),
            ("inactive", 2, 1, 4000, 0, 11), ("mode11", 2, 1, 4000, 11, 0),
        ):
            e.write(command + 8, bytes([initial]))
            e.write(actor + 0x3B7, bytes([actor_mode]))
            e.w32(scene + 0x4B48, elapsed)
            for frame in range(1, 27):
                state.update(mode=mode, direction=direction, decisions=0, actions=[])
                before = e.read(command + 8, 1)[0]
                e.call(0x6112AF, (actor,), ecx=command)
                print(json.dumps(dict(rule=rule_id, case=case, frame=frame, elapsed=elapsed,
                                      actor_mode=actor_mode, movement_mode=mode, direction=direction,
                                      before=before, after=e.read(command + 8, 1)[0],
                                      decisions=state["decisions"], actions=state["actions"])))


if __name__ == "__main__":
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("client_root", type=pathlib.Path)
    run(parser.parse_args().client_root)
