"""Run v848 NPC wander/speed and native escape/WF/candidate searches.

Usage: .venv/bin/python tools/probe_box_ai.py CLIENT_ROOT
RNG values and grid predicates/costs are controlled dependencies. The actual
selection, candidate enumeration, WF search, path extraction and vector writes
run in Client.exe. This is not a whole actor-update/physics emulation.
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

    def patch(address, name, function, pop):
        stub = e.add_stub(name)
        e.handlers[name] = function, pop
        e.write(address, b"\xe9" + struct.pack("<i", stub - address - 5))

    def short(value):
        return struct.unpack("<h", struct.pack("<H", value & 65535))[0]

    world, scene, actor = [e.malloc(n) for n in (0x4000, 0x6000, 0x1000)]
    e.w32(actor + 0x3FC, world)
    e.w32(world + 0x37F8, scene)
    rng = dict(value=0, calls=0)

    def random(e):
        rng["calls"] += 1
        return rng["value"]

    patch(0x4522F0, "random", random, 0)
    for mask in range(16):
        for facing in range(4):
            for value in (0, 1, 2, 3, 0xFFFFFFFF):
                e.w32(actor + 0x758, mask)
                e.write(actor + 0x3B4, bytes([facing]))
                rng.update(value=value, calls=0)
                direction = e.call(0x5B0ACA, ecx=actor)
                print(json.dumps(dict(kind="wander", mask=mask, facing=facing, random=value,
                                      direction=direction, calls=rng["calls"])))
    out = e.malloc(4)
    for level in range(11):
        # A new code address per immediate avoids stale JIT translations
        # when host-side mem_write rewrites a previously executed wrapper.
        wrapper = e.malloc(32)
        code = b"\x68" + struct.pack("<I", level)
        code += b"\xe8" + struct.pack("<i", 0x5C452B - (wrapper + 10))
        code += b"\x83\xc4\x04\xd9\x1d" + struct.pack("<I", out) + b"\xc3"
        e.write(wrapper, code)
        e.call(wrapper)
        print(json.dumps(dict(kind="speed", level=level, value=struct.unpack("<f", e.read(out, 4))[0])))

    code = e.malloc(64)
    e.write(code, bytes.fromhex("83 ec 10 d9 3c 24 66 8b 04 24 80 cc 0c 66 89 44 24 02 "
                               "d9 6c 24 02 df 7c 24 08 d9 2c 24 8b 44 24 08 8b 54 24 0c 83 c4 10 c3"))
    e.write(0x6200CE, b"\xe9" + struct.pack("<i", code - 0x6200CE - 5))

    attributes = e.make_object("attributes", 12, size=0x400)
    e.w32(actor + 0x400, attributes)
    # Execute 0087F0B5 unchanged: it returns a speed scalar on x87 and
    # its callers discard it. It is not a path-clear operation.
    e.write(attributes + 0x2B0, b"\x02")
    e.handlers["attributes+0x20"] = (lambda e: 2), 0
    finder = e.malloc(0x40)
    e.call(0x5DFA23, ecx=finder)
    e.call(0x5DFB07, (world,), ecx=finder)
    e.w32(actor + 0x694, finder)
    e.write(actor + 0x338, struct.pack("<ff", 220, 260))
    grid = dict(blocked=set(), mode="normal")
    queries = []

    def safe(e):
        row, col = short(e.arg(0)), short(e.arg(1))
        queries.append([row, col])
        return int(grid["mode"] != "none" and 0 <= row < 13 and 0 <= col < 15
                   and (row, col) not in grid["blocked"]
                   and (grid["mode"] != "unsafe_origin" or (row, col) != (6, 5)))

    def edge(e):
        col, row = e.arg(2) & 255, e.arg(3) & 255
        e.w32(e.arg(5), 0)
        return 9999 if (row, col) in grid["blocked"] else 1

    def entries():
        begin, end = e.r32(actor + 0x760), e.r32(actor + 0x764)
        return [list(struct.unpack("<BBhBB", e.read(p, 6))) for p in range(begin, end, 6)]

    patch(0x5B26AF, "safe", safe, 2)
    patch(0x5DFE32, "edge", edge, 6)
    for name, blocked in (
        ("open", []), ("right_blocked", [(6, 6)]), ("first_diagonal_blocked", [(7, 6)]),
        ("diagonal_blocks", [(5, 4), (5, 6), (7, 4), (7, 6)]),
        ("right_down_blocked", [(6, 6), (7, 5)]),
    ):
        grid["blocked"] = set(blocked)
        queries.clear()
        # Each controlled escape case starts with an empty input path.
        e.w32(actor + 0x764, e.r32(actor + 0x760))
        result = e.call(0x5B0598, ecx=actor)
        print(json.dumps(dict(kind="escape", case=name, blocked=blocked, result=result,
                              entries=entries(), first_queries=queries[:16], query_count=len(queries))))
    # A separate non-empty-input observation: the original helper appends
    # its escape to the vector, rather than clearing an existing route.
    grid["blocked"] = set()
    e.w32(actor + 0x764, e.r32(actor + 0x760))
    sample = e.malloc(6)
    for entry in ((6, 4, 9, 3, 0), (6, 5, 2, 0, 0)):
        e.write(sample, struct.pack("<BBhBB", *entry))
        e.call(0x5BD2C0, (sample,), ecx=actor + 0x75C)
    before = entries()
    result = e.call(0x5B0598, ecx=actor)
    print(json.dumps(dict(kind="escape_append", result=result, before=before, entries=entries())))
    grid.update(blocked=set(), mode="none")
    for role in (25, 27):
        e.write(actor + 0x394, bytes([role]))
        queries.clear()
        e.call(0x5B22FC, ecx=actor)
        print(json.dumps(dict(kind="safe_scan", role=role, queries=queries)))
    item_queries = []
    patch(0x5B275E, "item_query", lambda e: item_queries.append([short(e.arg(0)), short(e.arg(1))]) and 0, 2)
    e.call(0x5B13F5, ecx=actor)
    print(json.dumps(dict(kind="item_scan", queries=item_queries)))
    grid["mode"] = "unsafe_origin"
    for role in (25, 27):
        for value in (*range(16), 0xFFFFFFFF):
            e.write(actor + 0x394, bytes([role]))
            rng.update(value=value, calls=0)
            result = e.call(0x5B22FC, ecx=actor)
            print(json.dumps(dict(kind="flee", role=role, random=value, result=result,
                                  entries=entries(), calls=rng["calls"])))


if __name__ == "__main__":
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("client_root", type=pathlib.Path)
    run(parser.parse_args().client_root)
