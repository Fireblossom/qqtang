"""Trace UiScript SetGameType -> QQTSection getter -> native scene clamp.

MFC module-state bookkeeping and room-list refresh are controlled dependencies.
The registered client wrapper, DLL COM setter/getter and scene instructions run.
"""

import argparse
import json
import pathlib
import struct

from unicorn.x86_const import UC_X86_REG_ECX, UC_X86_REG_ESI
from win32emu import Win32Emu


def run(root):
    section = Win32Emu(root / "QQTSection.dll", root)
    lobby = section.malloc(0xD000)
    module = section.malloc(0x12000)
    thread_state = section.malloc(16)
    section.w32(module + 0x11FDC, lobby)

    def patch(e, address, name, function, pop):
        stub = e.add_stub(name)
        e.handlers[name] = function, pop
        e.write(address, b"\xe9" + struct.pack("<i", stub - address - 5))

    def mfc_guard(e):
        guard = e.uc.reg_read(UC_X86_REG_ECX)
        e.w32(guard, 0)
        e.w32(guard + 4, thread_state)
        return guard

    patch(section, 0x10049DEC, "mfc_guard", mfc_guard, 1)
    patch(section, 0x1003D12D, "list_refresh", lambda e: 0, 0)
    setter = section.r32(0x10054928 + 0x31C)
    getter = section.r32(0x1005419C + 0x68)
    client = Win32Emu(root / "Client.exe", root)
    service = client.make_object("service", 8)
    ui = client.make_object("ui", 200)
    client.w32(0x7FAF04, service)
    queries, writes = [], []

    def query(e):
        queries.append(e.read(e.arg(1), 16).hex())
        e.w32(e.arg(2), ui)
        return 0

    client.handlers["service+0x14"] = query, 3
    client.handlers["ui+0x31c"] = (lambda e: writes.append(e.arg(1) & 255) or 0), 2
    client.w32(0x85DA74, client.r32(0x85DA74) | 1)
    room_module = client.make_object("room_module", 30)
    client.w32(0x806A78 + 0x7D0, room_module)
    scene = client.malloc(0x6000)
    # End immediately after the original 005C862B..005C864F clamp block.
    client.write(0x5C8650, b"\xc3")
    for value in (0, 1, 2, 3, 4, 255, 256, 257):
        queries.clear()
        writes.clear()
        client.call(0x48BCE0, (value,))
        section.call(setter, (lobby + 0xBF30, writes[0]))
        raw = section.call(getter, ecx=module)
        client.handlers["room_module+0x68"] = (lambda e: raw), 0
        client.uc.reg_write(UC_X86_REG_ESI, scene)
        client.call(0x5C862B)
        print(json.dumps(dict(input=value, query=queries[0], script_value=writes[0],
                              stored=section.read(lobby + 0xC8B4, 1)[0],
                              room_list_value=section.read(lobby + 0x9B90, 1)[0],
                              getter=raw & 255, scene_mode=client.r32(scene + 0x51A4))))


if __name__ == "__main__":
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("client_root", type=pathlib.Path)
    run(parser.parse_args().client_root)
