"""Execute v848 005B1EA6 path consumption with controlled collision answers.

Position writes, the attribute speed getter and the world collision predicate
are intercepted. Vector operations, thresholds and lookahead arithmetic run
unchanged, including the actor's 0087E6CA getter wrapper.
"""

import argparse
import json
import pathlib
import struct

from win32emu import Win32Emu


def run(root):
    e = Win32Emu(root / "Client.exe", root)
    e.handlers["__dllonexit"] = (lambda e: e.arg(0)), 0
    e.handlers["abs"] = (lambda e: abs(struct.unpack("<i", struct.pack("<I", e.arg(0)))[0])), 0
    e.w32(0x85DA74, e.r32(0x85DA74) | 1)

    def patch(address, name, handler, pop):
        stub = e.add_stub(name)
        e.handlers[name] = handler, pop
        e.write(address, b"\xe9" + struct.pack("<i", stub - address - 5))

    # Preserve x87 stack behavior of the original MSVC truncation helper.
    code = e.malloc(64)
    e.write(code, bytes.fromhex("83 ec 10 d9 3c 24 66 8b 04 24 80 cc 0c 66 89 44 24 02 "
                               "d9 6c 24 02 df 7c 24 08 d9 2c 24 8b 44 24 08 8b 54 24 0c 83 c4 10 c3"))
    e.write(0x6200CE, b"\xe9" + struct.pack("<i", code - 0x6200CE - 5))
    actor, world, scene = [e.malloc(size) for size in (0x1000, 0x4000, 0x6000)]
    attributes = e.make_object("attributes", 12, size=0x400)
    e.w32(actor + 0x400, attributes)
    e.w32(actor + 0x3FC, world)
    e.w32(world + 0x37F8, scene)
    observations = dict(allowed=True, level=2, queries=[], snaps=[], random=0, random_calls=0)

    def collision(e):
        observations["queries"].append([e.arg(i) for i in range(6)])
        return int(observations["allowed"])

    def set_position(e):
        position = struct.unpack("<ff", struct.pack("<II", e.arg(0), e.arg(1)))
        observations["snaps"].append(list(position))
        e.write(actor + 0x338, struct.pack("<ff", *position))
        return 0

    patch(0x5B7999, "collision", collision, 6)
    patch(0x5AD322, "set_position", set_position, 2)
    # 005AA06B's non-FF flag is a fixed level-2 fallback, not an override
    # value. Control its result explicitly to exercise all table entries.
    patch(0x5AA06B, "speed_level", lambda e: observations["level"], 0)
    controller = e.malloc(16)
    e.w32(controller + 4, actor)

    def random(e):
        observations["random_calls"] += 1
        return observations["random"]

    patch(0x4522F0, "random", random, 0)
    sample = e.malloc(6)

    def entries():
        begin, end = e.r32(actor + 0x760), e.r32(actor + 0x764)
        return [list(struct.unpack("<BBhBB", e.read(p, 6))) for p in range(begin, end, 6)]

    # Native storage is goal -> root; each entry's direction points toward
    # its successor in forward traversal. Goal direction is the sentinel 9.
    route = [[7, 6, 9, 3, 0], [6, 6, 3, 0, 0], [6, 5, 0, 0, 0]]

    def observe(name, path=route, position=(220, 260), counter=0, allowed=True,
                standing=False, facing=0, level=2, dt=25, ai=None):
        e.w32(actor + 0x764, e.r32(actor + 0x760))
        for entry in path:
            e.write(sample, struct.pack("<BBhBB", *entry))
            e.call(0x5BD2C0, (sample,), ecx=actor + 0x75C)
        e.write(actor + 0x338, struct.pack("<ff", *position))
        e.w32(actor + 0x7AC, counter)
        e.write(actor + 0x3B4, bytes([facing]))
        e.write(actor + 0x3B8, bytes([1 if standing else 2]))
        e.write(attributes + 0x2B0, bytes([level]))
        e.w32(scene + 4, dt)
        observations.update(allowed=allowed, level=level, queries=[], snaps=[], random_calls=0)
        before = entries()
        if ai is not None:
            observations.update(random=ai["random"], danger=ai["danger"], gate=ai["gate"])
            e.write(actor + 0x7CC, struct.pack("<hh", *ai["cache"]))
            e.w32(actor + 0x7C8, ai["blocked"])
            e.w32(actor + 0x7D8, ai["cached_direction"])
            e.w32(actor + 0x758, ai["mask"])
        result = e.call(0x5DEF29 if ai is not None else 0x5B1EA6,
                        ecx=controller if ai is not None else actor)
        print(json.dumps(dict(kind="wander_follow" if ai is not None else "follow",
                              case=name, before=before, position=position, counter=counter,
                              allowed=allowed, standing=standing, facing=facing, level=level, dt=dt,
                              result=result & 255 if ai is not None else result,
                              entries=entries(), after_counter=e.r32(actor + 0x7AC),
                              after_position=struct.unpack("<ff", e.read(actor + 0x338, 8)),
                              queries=observations["queries"], snaps=observations["snaps"],
                              ai=ai, after_cache=struct.unpack("<hh", e.read(actor + 0x7CC, 4)),
                              after_cached_direction=e.r32(actor + 0x7D8),
                              random_calls=observations["random_calls"])))

    observe("empty", path=[], counter=7)
    observe("singleton", path=route[:1], counter=7)
    for offset in (-10.9, -10, -9.9, 0, 9.9, 10, 10.9):
        observe(f"offset_x_{offset}", position=(220 + offset, 260), counter=4)
        observe(f"offset_y_{offset}", position=(220, 260 + offset), counter=4)
    for name, position in (("inside_next_cell", (245, 260)), ("next_centre", (260, 260)),
                           ("goal", (260, 300)), ("off_route", (300, 300))):
        observe(name, position=position, counter=2)
    for counter in (9, 10, 0xFFFFFFFF):
        observe(f"mismatch_counter_{counter}", position=(245, 260), counter=counter)
    for standing in (False, True):
        observe(f"blocked_standing_{standing}", position=(230, 260), allowed=False,
                standing=standing, counter=4)
    for direction in range(4):
        path = [route[0], route[1], [6, 5, direction, 0, 0]]
        for level in (1, 2, 3, 9, 10):
            observe(f"lookahead_dir{direction}_level{level}", path=path,
                    position=(230.5, 260.5), facing=(direction + 1) % 4, level=level, dt=17)
    # A route appended by 005B0598 is examined from the new root first.
    observe("appended_escape", path=[[6, 4, 9, 3, 0], [6, 5, 2, 0, 0]] + route)
    observe("long_mismatch", path=[[0, i, 0, 0, 0] for i in range(15)], counter=0)

    # Exercise the real AIType2 controller around the unchanged follower.
    # Danger and time-gate predicates are inputs here, separately probed below.
    patch(0x5B2110, "danger", lambda e: observations["danger"], 0)
    patch(0x5B0A88, "gate", lambda e: observations["gate"], 0)
    patch(0x5B214D, "flee", lambda e: 3, 0)
    defaults = dict(cache=[6, 5], blocked=0, cached_direction=2, mask=15,
                    random=0, gate=1, danger=0)
    for name, options, path, position in (
        ("same_cell_cache", {}, [], (220, 260)),
        ("same_cell_changed_mask", dict(mask=8, random=3), [], (220, 260)),
        ("same_cell_empty_mask", dict(mask=0), [], (220, 260)),
        ("changed_cell", dict(cache=[6, 4]), [], (220, 260)),
        ("blocked_invalidates_cache", dict(blocked=1), [], (220, 260)),
        ("blocked_empty_mask", dict(blocked=1, mask=0), [], (220, 260)),
        ("path_centre_falls_back", {}, route, (220, 260)),
        ("path_outside_centre_follows", {}, route, (230, 260)),
        ("path_collision_falls_back", {}, route, (230, 260)),
        ("gate_keeps_facing", dict(gate=0), route, (230, 260)),
        ("danger_bypasses_gate", dict(gate=0, danger=1), route, (230, 260)),
    ):
        observe(name, ai=defaults | options, path=path, position=position,
                allowed=name != "path_collision_falls_back", facing=1)


def run_gate(root):
    e = Win32Emu(root / "Client.exe", root)
    e.handlers["abs"] = (lambda e: abs(struct.unpack("<i", struct.pack("<I", e.arg(0)))[0])), 0
    actor = e.make_object("actor", 27, size=0x1000)
    world = e.malloc(0x4000)
    e.w32(actor + 0x3FC, world)
    state = dict(last=0, writes=[])
    e.handlers["actor+0x64"] = (lambda e: state["last"]), 0

    def set_last(e):
        state["last"] = e.arg(0)
        state["writes"].append(e.arg(0))
        return 0

    e.handlers["actor+0x68"] = set_last, 1
    for now, last in ((0, 0), (499, 0), (500, 0), (501, 0), (1000, 501),
                      (1001, 501), (0, 499), (0, 500), (20, 0xFFFFFF00)):
        state.update(last=last, writes=[])
        e.w32(world + 0x3838, now)
        result = e.call(0x5B0A88, ecx=actor)
        print(json.dumps(dict(kind="gate", now=now, last=last, result=result & 255,
                              after_last=state["last"], writes=state["writes"])))


if __name__ == "__main__":
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("client_root", type=pathlib.Path)
    root = parser.parse_args().client_root
    run(root)
    run_gate(root)
