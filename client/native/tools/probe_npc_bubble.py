"""Execute AIType2 005DF009 eligibility, search and placement dispatch."""

import argparse
import json
import pathlib
import struct

from win32emu import Win32Emu


def run(root):
    e = Win32Emu(root / "Client.exe", root)
    actor, controller, vector = [e.malloc(size) for size in (0x1000, 16, 6)]
    e.w32(controller + 4, actor)
    state = dict(near=False, search=0, line_calls=0, searches=0, places=0)

    def patch(address, name, function):
        stub = e.add_stub(name)
        e.handlers[name] = function, 0
        e.write(address, b"\xe9" + struct.pack("<i", stub - address - 5))

    def line(e):
        state["line_calls"] += 1
        return int(state["near"])

    def search(e):
        state["searches"] += 1
        return state["search"]

    def place(e):
        state["places"] += 1
        return 0

    patch(0x5B0406, "line", line)
    patch(0x5B0598, "search", search)
    patch(0x5B08E0, "place", place)
    for present, kind, marker in ((False, 0, 0), (True, 0, 0), (True, 3, 0), (True, 4, 0), (True, 3, 4)):
        for near in (False, True):
            for result in (0, 1, 0xFFFFFFFF):
                e.write(vector, struct.pack("<BBhBB", 6, 5, 0, kind, marker))
                e.w32(actor + 0x760, vector if present else 0)
                e.w32(actor + 0x764, vector + 6 if present else 0)
                state.update(near=near, search=result, line_calls=0, searches=0, places=0)
                e.call(0x5DF009, ecx=controller)
                print(json.dumps(dict(present=present, kind=kind, marker=marker, near=near,
                                      search_result=result, line_calls=state["line_calls"],
                                      searches=state["searches"], places=state["places"])))


if __name__ == "__main__":
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("client_root", type=pathlib.Path)
    run(parser.parse_args().client_root)
