"""Execute PVE pixel view, target selection and battle-state transitions.

Real initialized NPC, bounds, enmity tree, role-vector copy, selection,
scene-seeded RNG and battle switching run unchanged. Role registration and
role state getters are controlled dependencies, not complete player actors.
"""

import argparse
import json
import pathlib
import struct

from unicorn.x86_const import UC_X86_REG_ECX
from probe_npc_motion import signed
from probe_pve_remote import create_pve_actor


def run(root):
    e, world, npc, remote, witness, patch = create_pve_actor(root, initial_cell=(7, 6), region_bounds=(0, 0, 14, 12))
    e.handlers['abs'] = (lambda e: abs(signed(e.arg(0)))), 0
    patch(0x534AD0, 'perception_is_npc', lambda e: int((e.arg(0) & 65535) >= 30300), 1)
    states = {}
    patch(0x5C0BDA, 'perception_role_state', lambda e: states[e.uc.reg_read(UC_X86_REG_ECX)], 0)
    actors = {}
    for player in list(range(1, 9)) + [30301]:
        actor, controller = e.malloc(0x1000), e.malloc(32)
        e.write(actor + 0x328, struct.pack('<H', player))
        e.write(actor + 0x396, bytes([player - 1 if player < 9 else 8]))
        e.w32(actor + 0x36C, controller)
        actors[player] = actor, controller
        states[controller] = 2
    role_vector, route_vector, seat = e.malloc(64), e.malloc(64), e.malloc(4)
    e.write(npc + 0x8B0, struct.pack('<4i', 0, 0, 14, 12))
    e.write(npc + 0x338, struct.pack('<ff', 300, 260))
    print(json.dumps(dict(kind='init', witness=witness, bounds=[0, 0, 14, 12])))

    def read_enmity():
        head = e.r32(npc + 0x890)
        result = {}

        def visit(node):
            if node == head or node == e.r32(0x85DAB8):
                return
            visit(e.r32(node))
            result[str(e.r32(node + 12))] = signed(e.r32(node + 16))
            visit(e.r32(node + 8))

        visit(e.r32(head + 4))
        return result

    def configure(roles, enmity):
        e.call(0x5BBBD0, ecx=npc)
        pointers = []
        for role in roles:
            actor, controller = actors[role['id']]
            states[controller] = role['mode']
            e.write(actor + 0x338, struct.pack('<ff', *role['position']))
            pointers.append(actor)
        e.write(role_vector, struct.pack('<' + 'I' * len(pointers), *pointers))
        e.w32(world + 0x133C, role_vector)
        e.w32(world + 0x1340, role_vector + 4 * len(pointers))
        e.w32(world + 0x1344, role_vector + 64)
        for key, value in enmity.items():
            e.w32(seat, int(key))
            pointer = e.call(0x5BD976, (seat,), ecx=npc + 0x88C)
            e.w32(pointer, value)
        assert read_enmity() == enmity

    player = actors[1][0]
    for position in ((299.75, 259.75), (300, 260), (300.25, 260.25), (319.75, 279.75)):
        e.write(npc + 0x338, struct.pack('<ff', *position))
        for view in (0, 1, 3):
            e.write(npc + 0x8C0, struct.pack('<H', view))
            for direction, (dx, dy) in enumerate(((1, 0), (0, -1), (-1, 0), (0, 1))):
                for distance in (0, 1, 39, 40, 79, 80, 159, 160, 400):
                    target = (300 + dx * distance, 260 + dy * distance)
                    e.write(player + 0x338, struct.pack('<ff', *target))
                    result = e.call(0x5BAD0C, (player,), ecx=npc) & 255
                    print(json.dumps(dict(kind='view', position=position, view=view, target=target, result=bool(result))))

    def roles_for(ids):
        return [dict(id=player, mode=2, position=[300, 260]) for player in ids]

    groups = []
    for count in range(9):
        for reverse in (False, True):
            ids = list(range(1, count + 1))
            groups.append(('count-' + str(count) + ('-reverse' if reverse else ''), roles_for(ids[::-1] if reverse else ids)))
    for name, player, mode, position in (('dead', 2, 7, [300, 260]), ('mode12', 1, 12, [300, 260]),
                                          ('outside', 3, 2, [600, 260]), ('edge', 1, 2, [599, 519])):
        roles = roles_for([3, 1, 2, 30301])
        for role in roles:
            if role['id'] == player:
                role.update(mode=mode, position=position)
        groups.append((name, roles))
    for name, roles in groups:
        for enmity in ({}, {'0': 10, '1': 20, '2': 20}, {'0': 0, '1': 0, '2': 0}):
            for strategy in (0, 1, 2, 3):
                for now in ((0, 1, 20, 65535, 0xFFFFFFFF) if strategy == 2 else (20,)):
                    configure(roles, enmity)
                    e.w32(npc + 0x8AC, strategy)
                    e.w32(world + 0x3838, now)
                    result = e.call(0x5BC134, ecx=npc)
                    chosen = next((player for player, (actor, controller) in actors.items() if actor == result), 0)
                    assert result == 0 or chosen != 0
                    print(json.dumps(dict(kind='target', name=name, roles=roles, enmity=enmity,
                                          strategy=strategy, now=now, chosen=chosen, after=read_enmity())))
    for name, roles in groups[-4:] + groups[:2]:
        for enmity in ({}, {'0': 10, '1': 20, '2': 20}):
            for battle in (0, 1):
                configure(roles, enmity)
                e.w32(npc + 0x8A8, battle)
                e.w32(npc + 0x8A0, 4 if battle else 3)
                e.w32(npc + 0x8A4, 5)
                e.w32(npc + 0x8F4, 1000)
                e.write(route_vector, bytes([7, 6, 0, 8, 6, 9]))
                for offset, value in ((0x900, route_vector), (0x904, route_vector + 6), (0x908, route_vector + 64)):
                    e.w32(npc + offset, value)
                e.call(0x5BADE5, ecx=npc)
                print(json.dumps(dict(kind='battle', name=name, roles=roles, enmity=enmity, before=battle,
                                      battle=e.r32(npc + 0x8A8), strategy=e.r32(npc + 0x8A0), saved=e.r32(npc + 0x8A4),
                                      elapsed=e.r32(npc + 0x8F4), route_size=(e.r32(npc + 0x904) - e.r32(npc + 0x900)) // 3,
                                      total=signed(e.r32(npc + 0x89C)), after=read_enmity())))


if __name__ == '__main__':
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('client_root', type=pathlib.Path)
    run(parser.parse_args().client_root)
