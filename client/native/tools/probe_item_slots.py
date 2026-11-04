"""Run v848's real slot initializer, grant, erase and page functions.

Usage: .venv/bin/python tools/probe_item_slots.py CLIENT_ROOT
Only emulator memory is patched. Graphics, the section's external data and
TerSafe's storage wrapper are isolated; the ordering/vector/VM code is native.
"""

import argparse
import configparser
import json
import pathlib
import struct
import xml.etree.ElementTree as ET

from decrypt_pve import read_cfg
from win32emu import EmuStop, Win32Emu
from unicorn.x86_const import UC_X86_REG_ECX


def fixture(root):
    e = Win32Emu(root / "Client.exe", root)

    def patch(address, name, function, pop):
        stub = e.add_stub(name)
        e.handlers[name] = function, pop
        e.write(address, b"\xe9" + struct.pack("<i", stub - address - 5))

    # VC6 _ftol: preserve the x87 control word, truncate and return EDX:EAX.
    code = e.malloc(64)
    e.write(code, bytes.fromhex("83 ec 10 d9 3c 24 66 8b 04 24 80 cc 0c 66 89 44 24 02 "
                               "d9 6c 24 02 df 7c 24 08 d9 2c 24 8b 44 24 08 8b 54 24 0c 83 c4 10 c3"))
    e.write(0x6200CE, b"\xe9" + struct.pack("<i", code - 0x6200CE - 5))
    for address in (0x5BDADA, 0x5BDF18, 0x5BDFA9, 0x5CA492):
        patch(address, f"plain_init_{address:x}", lambda e: 0, 0)
    for address in (0x5BDBC4, 0x5BDF40, 0x5BDFD1, 0x5CA4BA):
        patch(address, f"plain_access_{address:x}", lambda e: 0, 2)
    e.handlers["__dllonexit"] = (lambda e: e.arg(0), 0)
    e.w32(0x85DA74, e.r32(0x85DA74) | 1)  # application singleton already set up
    actor, world, scene, rule = [e.malloc(n) for n in (0x2000, 0x60000, 0x6000, 0x100)]
    e.w32(world + 0x37F8, scene)
    e.w32(scene + 0x4EA4, rule)
    e.w32(rule + 8, 12)
    e.write(world + 0x3820, struct.pack("<H", 1))
    e.w32(world + 0x3838, 3000)

    def player_info(e):
        p = e.arg(1)
        e.write(p, bytes(0x23E0))
        e.w32(p, 1000007)
        e.write(p + 4, b"fixture\0")
        e.write(p + 0x18, struct.pack("<HBBBB", 1, 1, 1, 0, 0))
        e.write(p + 0x54, struct.pack("<H", 3))
        for index, item in enumerate((20020, 20058, 20059)):
            e.write(p + 0x56 + index * 18, struct.pack("<HIBBBBII", item, 5, 1, 0, 0, 0, 0, 0xFFFFFFFF))
        return 1

    def attributes(e):
        p = e.uc.reg_read(UC_X86_REG_ECX)
        for offset, value in ((0x2EC, 3), (0x2F0, 100), (0x2FC, 300), (0x300, 3)):
            e.w32(p + offset, value)
        e.write(p + 0x304, struct.pack("<f", 2.0))  # controlled PVE level-zero stats

    def avatar(e):
        p = e.malloc(0x330)
        for offset, value in ((0, 0x797020), (0x258, e.arg(0)), (0x25C, e.arg(1)), (0x268, 1), (0x26C, 270)):
            e.w32(p + offset, value)
        e.write(p + 0x274, b"\x01")
        return p

    def protection(e):
        e.w32(e.uc.reg_read(UC_X86_REG_ECX) + 0x2C, e.arg(0))

    def item_kind(e):
        e.write(e.arg(1), b"item\0")  # all controlled profile entries have this category

    def script_number(e):
        expression = e.cstr(e.arg(0)).decode("ascii")
        if expression.startswith("GetSubIDByItemID("):
            return int(expression.split("(")[1].split(")")[0])
        raise EmuStop("unprovided script query: " + expression)

    for address, name, function, pop in (
        (0x5A3BE2, "player_info", player_info, 2),
        (0x5A85AF, "attribute_config", attributes, 1),
        (0x534990, "local_player", lambda e: int(e.arg(0) & 65535 == 1), 1),
        (0x534AD0, "npc", lambda e: 0, 1),
        (0x5C4CDC, "attribute_recorder", lambda e: 0, 2),
        (0x5CB15C, "avatar", avatar, 2),
        (0x5B5202, "protection", protection, 1),
        (0x5C46DB, "image_init", lambda e: 1, 0),
        (0x5D2F2B, "name_label", lambda e: 1, 0),
        (0x533ED0, "item_kind", item_kind, 0),
        (0x61BA0A, "script_number", script_number, 0),
        (0x61B9BF, "script_text", lambda e: 0, 0),
        (0x61B9D0, "script_format", lambda e: 0, 0),
        (0x622390, "error_log", lambda e: 0, 0),
    ):
        patch(address, name, function, pop)
    hud = []
    patch(0x5DAC82, "slot_writer", lambda e: hud.append([e.arg(i) for i in range(4)]), 4)
    e.call(0x5B985C, (world, 1, 1, 1, 0), ecx=actor, limit=2000000)
    if e.call(0x88338C, ecx=actor + 0x318, limit=2000000) & 255 != 1:
        raise EmuStop("native PVE actor initialization failed")
    attributes_pointer = e.r32(actor + 0x400)
    action_list = attributes_pointer + 0x19C
    bar = e.malloc(28)
    section = e.make_object("section", 24)
    e.handlers["section+0x58"] = (lambda e: bar, 0)
    e.w32(0x806A78 + 0x7D0, section)
    cfg = configparser.ConfigParser()
    cfg.read_string((root / "config/GameCFG.ini").read_bytes().decode("latin-1"))
    raw = read_cfg((root / "config/errorPPP.ini").read_bytes(), cfg.getint("prop", "propCfgSize"))
    tiers = {}
    for node in ET.fromstring(raw.decode("gbk")).iter():
        if node.tag.startswith("ID") and node.tag[2:].isdigit():
            tiers.setdefault(int(node.tag[2:]), int(node.get("Tier", "0")))

    def prop_definition(e):
        item = e.arg(0)
        if item not in tiers:
            return 0
        p = e.malloc(0x100)
        e.w32(p + 4, item)
        e.w32(p + 0x28, tiers[item])
        return p

    patch(0x5E98F9, "prop_factory", lambda e: 0, 0)
    patch(0x5EA0D2, "prop_definition", prop_definition, 1)
    patch(0x5F020F, "prop_state", lambda e: 1, 1)
    patch(0x5DBA3B, "register_prop", lambda e: 0, 2)
    return e, actor, attributes_pointer, world, action_list, bar, hud


def entries(e, action_list):
    return [[e.r32(p), e.r32(p + 0x14), e.r32(p + 0x28)]
            for p in range(e.r32(action_list + 0xC), e.r32(action_list + 0x10), 60)]


def run(root, name, wire, order):
    e, actor, attributes, world, action_list, bar, hud = fixture(root)
    payload = e.malloc(max(16, len(wire) * 6))
    e.write(payload, b"".join(struct.pack("<Ih", *item) for item in wire))
    e.write(bar, struct.pack("<7I", *order))
    if name == "world_first":
        e.call(0x87EC6D, (41, 1, 0xFFFFFFFF), ecx=actor, limit=300000)
    e.call(0x5B974A, (payload, len(wire), actor), ecx=world, limit=2000000)
    result = dict(case=name, wire=wire, bar=order, prepared=entries(e, action_list), hud=hud[-7:])
    if name == "holes":
        for item in (41, 42, 43, 44, 45, 46, 63, 64, 65):
            e.call(0x87EC6D, (item, 1, 0xFFFFFFFF), ecx=actor, limit=300000)
        result["with_world_items"] = entries(e, action_list)
        e.call(0x5A9E9D, (20020, 5), ecx=attributes)
        result["after_exhaustion"] = entries(e, action_list)
        e.call(0x87EC6D, (41, 255, 0xFFFFFFFF), ecx=actor, limit=300000)
        result["stack_after_1_plus_255"] = next(row for row in entries(e, action_list) if row[0] == 41)
        e.call(0x5C9EC9, ecx=action_list)
        result["broken_native_page"] = dict(offset=e.r32(action_list + 4),
            visible=[e.call(0x5C9EF1, (i,), ecx=action_list) for i in range(7)])
    if name == "world_first":
        e.call(0x5A9E9D, (41, 1), ecx=attributes)
        e.call(0x87EC6D, (41, 1, 0xFFFFFFFF), ecx=actor, limit=300000)
        result["after_reacquisition"] = entries(e, action_list)
    print(json.dumps(result))


if __name__ == "__main__":
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("client_root", type=pathlib.Path)
    args = parser.parse_args()
    base = [(20020, 5), (20058, 7), (20059, 9)]
    run(args.client_root, "holes", base, [20059, 0, 20020, 20058, 0, 0, 0])
    run(args.client_root, "empty", base, [0] * 7)
    run(args.client_root, "missing_definition", [(20046, 1), (20020, 5)], [20046, 0, 20020, 0, 0, 0, 0])
    run(args.client_root, "first_seven", base + [(i, 1) for i in range(20060, 20065)],
        [20064, 20063, 0, 20020, 0, 20058, 20059])
    run(args.client_root, "world_first", base, [20059, 0, 20020, 20058, 0, 0, 0])
