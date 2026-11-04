"""Run PVE route validity with the actual CMapElement passage predicate.

World lookups and GridAttr lookup are controlled; 005BB7F6 and the real
005EF1AA direction decoder execute unchanged, including terminal direction9.
"""

import argparse
import json
import pathlib
import struct

from probe_pve_remote import create_pve_actor


def run(root):
    e, world, actor, remote, witness, patch = create_pve_actor(root, initial_cell=(7, 6))
    element, attribute, empty, vector = [e.malloc(size) for size in (64, 4, 16, 256)]
    e.w32(element, 0x7A369C)
    state = dict(cell=None, mask=0)
    queries = []

    def element_at(e):
        cell = e.arg(1) & 255, e.arg(0) & 255
        queries.append([1, *cell, 0])
        return element if cell == state['cell'] else 0

    def grid_attr(e):
        cell = e.arg(1) & 65535, e.arg(0) & 65535
        assert cell == state['cell']
        queries.append([2, *cell, state['mask']])
        e.w32(attribute, state['mask'])
        return attribute

    def bombs(e):
        queries.append([0, e.arg(1) & 255, e.arg(0) & 255, 0])
        return empty

    patch(0x5D865C, 'route_element_at', element_at, 2)
    patch(0x5EECAF, 'element_grid_attr', grid_attr, 2)
    patch(0x5D3BD3, 'route_empty_bombs', bombs, 2)
    e.w32(actor + 0x8A8, 0)
    e.write(actor + 0x338, struct.pack('<ff', 300, 260))
    print(json.dumps(dict(kind='init', witness=witness, element_vtable=hex(e.r32(element)),
                          predicate=hex(e.r32(e.r32(element) + 0x2C)))))
    for direction, (dx, dy) in enumerate(((1, 0), (0, -1), (-1, 0), (0, 1))):
        nodes = [[7, 6, direction], [7 + dx, 6 + dy, direction], [7 + 2 * dx, 6 + 2 * dy, 9]]
        for index in (-1, 0, 1, 2):
            for mask in (range(16) if index >= 0 else (0,)):
                state.update(cell=tuple(nodes[index][:2]) if index >= 0 else None, mask=mask)
                e.write(vector, b''.join(bytes(n) for n in nodes))
                e.w32(actor + 0x900, vector)
                e.w32(actor + 0x904, vector + 9)
                e.w32(actor + 0x908, vector + 256)
                queries.clear()
                valid = e.call(0x5BB7F6, ecx=actor) & 255
                if index < 0:
                    assert valid == 1, 'open route must retain its current-cell node'
                after = [list(e.read(p, 3)) for p in range(e.r32(actor + 0x900), e.r32(actor + 0x904), 3)]
                print(json.dumps(dict(kind='valid', direction=direction, nodes=nodes,
                                      element_index=index, mask=mask, valid=bool(valid), after=after, queries=list(queries))))


if __name__ == '__main__':
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('client_root', type=pathlib.Path)
    run(parser.parse_args().client_root)
