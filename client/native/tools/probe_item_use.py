"""Execute v848's item predicate, Ctrl selection and keyboard input lock.

Usage: .venv/bin/python tools/probe_item_use.py CLIENT_ROOT
Uses the real actor/ordered-inventory fixture from probe_item_slots. The VM
predicate and selector run unchanged; final request dispatch is observed.
DirectInput data, Tier lookups and owner lookup are controlled dependencies.
Prepared mode/initial/reuse/deny logic and keyboard updates remain native.
"""

import argparse
import json
import pathlib
import struct

from probe_item_slots import fixture


def run(root):
    e, actor, attrs, world, actions, bar, hud = fixture(root)

    def patch(address, name, function, pop):
        stub = e.add_stub(name)
        e.handlers[name] = function, pop
        e.write(address, b"\xe9" + struct.pack("<i", stub - address - 5))

    for item in (63, 20020, 64, 41):
        e.call(0x87EC6D, (item, 1, 0xFFFFFFFF), ecx=actor, limit=300000)
    for state in (0, 1, 2, 3):
        e.write(actor + 0x3B7, bytes([state]))
        allowed = {str(item): e.call(0x87F9A8, (item,), ecx=actor, limit=300000) & 255
                   for item in (0, 41, 63, 64, 99, 20020)}
        print(json.dumps(dict(kind="predicate", state=state, allowed=allowed)))

    selected = []
    patch(0x6109E8, "dispatch_prepared", lambda e: selected.append(e.arg(1)), 2)
    patch(0x5A2F4C, "dispatch_world", lambda e: selected.append(e.r32(e.arg(1) + 6)), 2)
    command = e.malloc(8)
    e.w32(command + 4, world)
    e.w32(world + 0x4B34, 1)
    e.w32(world + 0x4B40, 2)  # do not locally echo the observed packet
    for state in (0, 1, 2, 3):
        e.write(actor + 0x3B7, bytes([state]))
        selected.clear()
        e.call(0x610516, (actor, 0xFFFFFFFF), ecx=command)
        print(json.dumps(dict(kind="ctrl", clock=e.r32(world + 0x3838), state=state, selected=selected)))

    app = 0x806A78
    keyboard = app + 0x5C8
    e.call(0x5A49B8, ecx=keyboard)
    device = e.make_object("keyboard_device", 10)
    e.w32(keyboard + 8, device)
    keys = set()

    def device_state(e):
        raw = bytearray(256)
        for key in keys:
            raw[key] = 0x80
        e.write(e.arg(2), raw)
        return 0

    e.handlers["keyboard_device+0x24"] = device_state, 3
    e.handlers["GetAsyncKeyState"] = (lambda e: 0), 1
    e.w32(app + 0x7D4, e.r32(world + 0x37F8))
    for enabled in (True, False, True):
        e.write(keyboard + 0x1FD, bytes([enabled]))
        keys.clear()
        e.call(0x5A4C52, ecx=keyboard)
        keys.update((0x1D, 2, 0x2C, 0xF))
        e.call(0x5A4C52, ecx=keyboard)
        print(json.dumps(dict(kind="keyboard", item_keys_enabled=enabled,
                              **{name: e.read(keyboard + 0xC + index, 1)[0]
                                 for name, index in (("ctrl", 6), ("number1", 8), ("z", 15), ("tab", 22))})))
    e.call(0x5AB1AF, ecx=actor)
    disabled = e.read(app + 0x7C5, 1)[0]
    e.call(0x5AB1D9, ecx=actor)
    print(json.dumps(dict(kind="input_lock", disabled=disabled, enabled=e.read(app + 0x7C5, 1)[0])))

    # Real PickedUpState permission code. Only its Tier registry and owner
    # lookups are replaced; mode, time comparisons and actor deny vectors run.
    prop, state, payload = [e.malloc(n) for n in (0x100, 0x40, 32)]
    e.w32(state + 4, prop)
    e.w32(state + 0xC, 20000)
    e.w32(prop + 4, 20020)
    e.w32(prop + 0x28, 3)
    e.w32(prop + 0x60, world)
    lookup = dict(last=0, interval=15000, actor=actor)
    patch(0x5F7E49, "last_use_lookup", lambda e: lookup["last"], 0)  # cdecl
    patch(0x5F7E20, "interval_lookup", lambda e: lookup["interval"], 0)
    patch(0x5DBC3E, "owner_lookup", lambda e: lookup["actor"], 0)
    rule = e.r32(e.r32(world + 0x37F8) + 0x4EA4)
    cases = (
        ("initial_before", 19999, 0, 0, 1, 0, True),
        ("initial_at", 20000, 0, 0, 1, 0, True),
        ("reuse_before", 34999, 20000, 0, 1, 0, True),
        ("reuse_at", 35000, 20000, 0, 1, 0, True),
        ("adventure_only_reject", 40000, 0, 2, 1, 0, True),
        ("adventure_only_accept", 40000, 0, 2, 12, 0, True),
        ("normal_only_reject", 40000, 0, 1, 12, 0, True),
        ("normal_only_accept", 40000, 0, 1, 1, 0, True),
        ("deny_unrelated", 40000, 0, 0, 1, 20058, True),
        ("deny_specific", 40000, 0, 0, 1, 20020, True),
        ("deny_all", 40000, 0, 0, 1, 0xFFFFFFFF, True),
        ("no_owner", 40000, 0, 0, 1, 0, False),
    )
    for name, now, last, mode, rule_id, deny, has_owner in cases:
        e.w32(world + 0x3838, now)
        e.w32(prop + 0x54, mode)
        e.w32(rule + 8, rule_id)
        e.write(payload, struct.pack("<HI", 1, now))
        lookup.update(last=last, actor=actor if has_owner else 0)
        if deny:
            e.call(0x5AB161, (deny,), ecx=actor)
        allowed = e.call(0x5F7CC5, (payload,), ecx=state) & 255
        if deny:
            e.call(0x5AF41B, (deny,), ecx=actor)
        print(json.dumps(dict(kind="prepared", case=name, clock=now, last_use=last,
                              mode=mode, rule=rule_id, deny=deny, allowed=allowed)))


if __name__ == "__main__":
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("client_root", type=pathlib.Path)
    run(parser.parse_args().client_root)
