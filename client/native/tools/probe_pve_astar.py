"""Execute initialized PVE A*, polygon, passage and route reconstruction.

The real finder is configured through 005F0C8A. Only world element/bomb
queries are controlled; tree operations, all passage gates and 500-expansion
termination execute unchanged. Cases use explicit synthetic geometry.
"""

import argparse
import json
import pathlib
import random
import struct

from unicorn import UC_HOOK_CODE
from probe_npc_motion import signed
from probe_pve_remote import create_pve_actor


def run(root):
    e, world, actor, remote, witness, patch = create_pve_actor(root)
    e.handlers['abs'] = (lambda e: abs(signed(e.arg(0)))), 0
    finder = e.r32(actor + 0x8F8)
    assert e.r32(finder) == 0x7A374C
    empty, occupied, token = [e.malloc(size) for size in (16, 16, 4)]
    e.w32(occupied + 4, token)
    e.w32(occupied + 8, token + 4)
    state = dict(elements={}, bombs=set())
    element = e.make_object('path_element', 16, size=32)
    current_element = [0, 0]

    def element_at(e):
        cell = e.arg(1) & 255, e.arg(0) & 255
        current_element[:] = cell
        return element if cell in state['elements'] else 0

    def pass_element(e):
        cell = e.arg(1) & 255, e.arg(0) & 255
        assert tuple(current_element) == cell
        return int(bool(state['elements'][cell] & (1 << (e.arg(2) & 255))))

    def bombs(e):
        cell = e.arg(1) & 255, e.arg(0) & 255
        return occupied if cell in state['bombs'] else empty

    patch(0x5D865C, 'path_elements', element_at, 2)
    patch(0x5D3BD3, 'path_bombs', bombs, 2)
    e.handlers['path_element+0x2c'] = pass_element, 3
    expanded, passages = [], []

    def expand(uc, address, size, user):
        expanded.append(list(e.read(e.arg(0), 2)))

    def passage(uc, address, size, user):
        passages.append([*e.read(e.arg(0), 2), e.read(e.arg(1), 1)[0], e.arg(2)])

    e.uc.hook_add(UC_HOOK_CODE, expand, begin=0x5F133E, end=0x5F133E)
    e.uc.hook_add(UC_HOOK_CODE, passage, begin=0x5F0FC1, end=0x5F0FC1)
    polygon, points, start_ptr, goal_ptr, output = [e.malloc(size) for size in (16, 12, 4, 4, 16)]
    print(json.dumps(dict(kind='init', witness=witness, finder_vtable=hex(e.r32(finder)))))

    def sample(name, bounds, start, goal, elements=None, bombs=()):
        left, top, right, bottom = bounds
        state.update(elements=elements or {}, bombs=set(bombs))
        vertices = [(left, top), (right, top), (right, bottom), (left, bottom)]
        e.write(points, b''.join(bytes([col, row, 0]) for col, row in vertices))
        e.w32(polygon + 4, points)
        e.w32(polygon + 8, points + 12)
        e.w32(polygon + 12, points + 12)
        e.call(0x5F0C8A, (polygon,), ecx=finder)
        e.call(0x5BD912, (e.r32(output + 4), e.r32(output + 8)), ecx=output)
        e.write(start_ptr, bytes([*start, 9, 0]))
        e.write(goal_ptr, bytes([*goal, 9, 0]))
        expanded.clear()
        passages.clear()
        result = e.call(0x5F10D3, (start_ptr, goal_ptr, output), ecx=finder, limit=100_000_000)
        route = [list(e.read(p, 3)) for p in range(e.r32(output + 4), e.r32(output + 8), 3)]
        print(json.dumps(dict(kind='search', name=name, bounds=bounds, start=start, goal=goal,
                              elements=[[*cell, mask] for cell, mask in sorted(state['elements'].items())],
                              bombs=sorted(state['bombs']), result=result, route=route,
                              expanded=list(expanded), passages=list(passages))))

    bounds = (0, 0, 8, 8)
    for start in ((0, 0), (4, 4), (8, 8)):
        for goal in ((0, 0), (1, 1), (4, 4), (7, 1), (8, 8), (9, 4)):
            sample('open', bounds, start, goal)
    for mask in (0, 1, 2, 4, 8, 5, 10, 15):
        for cell in ((4, 4), (5, 4), (6, 4)):
            sample('directional', bounds, (4, 4), (6, 4), {cell: mask})
    for bombs in (((4, 4),), ((5, 4),), ((6, 4),), ((5, 3), (5, 4), (5, 5)), ((5, 4), (7, 4), (6, 3), (6, 5))):
        sample('bombs', bounds, (4, 4), (6, 4), bombs=bombs)
    rng = random.Random(0x5F10D3)
    for index in range(48):
        elements = {(col, row): rng.choice((0, 0, 1, 2, 4, 8, 5, 10, 15))
                    for col in range(9) for row in range(9) if rng.random() < .24}
        bombs = [(rng.randrange(9), rng.randrange(9)) for _ in range(index % 4)]
        sample('mixed-' + str(index), bounds, (rng.randrange(9), rng.randrange(9)),
               (rng.randrange(9), rng.randrange(9)), elements, bombs)
    for goal in ((39, 39), (39, 0), (0, 39), (25, 25), (20, 20)):
        sample('large-open', (0, 0, 39, 39), (0, 0), goal)
    # A one-cell serpentine corridor exercises discovery on expansion 499/500
    # and failure immediately after the 500th expansion.
    corridor = []
    for row in range(0, 39, 2):
        cols = range(40) if row % 4 == 0 else range(39, -1, -1)
        corridor.extend((col, row) for col in cols)
        if row < 38:
            corridor.append((39 if row % 4 == 0 else 0, row + 1))
    allowed = set(corridor)
    walls = {(col, row): 0 for col in range(40) for row in range(40) if (col, row) not in allowed}
    for index in (498, 499, 500, 501, 600):
        sample('limit-' + str(index), (0, 0, 39, 39), corridor[0], corridor[index], walls)


if __name__ == '__main__':
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('client_root', type=pathlib.Path)
    run(parser.parse_args().client_root)
