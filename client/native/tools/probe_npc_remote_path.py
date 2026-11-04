"""Sample real 005D13C2 held paths, including corner phase and PC24 rounding.

One consumed record, no following packet or latency correction. Initial phase
0/1/2 samples explicitly control retained state; normal records start at -1.
"""

import argparse
import json
import pathlib
import struct

from probe_npc_remote import create_remote_actor


def run(root):
    e, scene, actor, rule, controller = create_remote_actor(root)
    e.handlers['abs'] = (lambda e: abs(struct.unpack('<i', struct.pack('<I', e.arg(0)))[0])), 0
    e.write(actor+0x328, struct.pack('<H', 20001))
    e.write(actor+0x394, bytes([27, 7]))
    e.w32(rule+8, 8)
    remote, message = e.malloc(0x80), e.malloc(0x40)

    for direction in range(4):
        sign = -1 if direction in (1, 2) else 1
        vertical = direction & 1

        def xy(primary, cross):
            return [cross, primary] if vertical else [primary, cross]

        cases = [('straight', xy(300, 260), [0, 0], xy(300+sign*40, 260), -1, True),
                 ('straight_off_axis', xy(300, 253.25), [0, 0], xy(300+sign*40, 280), -1, True),
                 ('stopped', xy(300.25, 260.5), xy(320, 260), xy(380, 280), -1, False)]
        corner = xy(300+sign*20, 260)
        end = xy(300+sign*80, 280)
        for delta in (-20, -2, -1.9, -1, -.1, 0, 1, 1.9, 2, 20):
            cases.append((f'corner_offset_{delta}', xy(corner[vertical]+sign*delta, 260), corner, end, -1, True))
        cases.extend([
            ('reverse_cross', xy(300, 280), xy(300+sign*20, 280), xy(300+sign*80, 260), -1, True),
            ('phase0', xy(300+sign*20, 270.1), corner, end, 0, True),
            ('phase0_off_primary', xy(300+sign*25, 270), corner, end, 0, True),
            ('phase1_behind', xy(300, 280), corner, end, 1, True),
            ('phase1_past', xy(300+sign*25, 280), corner, end, 1, True),
            ('phase2_at_corner', xy(300+sign*20, 280), corner, end, 2, True),
            ('missing_corner_phase0', xy(300, 253.5), [0, 0], xy(300+sign*40, 280), 0, True),
            ('cross_fraction', xy(300+sign*20, 279.25), corner, end, 0, True),
            ('cross_fraction_reverse', xy(300+sign*20, 260.75), xy(300+sign*20,280), xy(300+sign*80,260), 0, True),
        ])
        for rate in (0, 1, 2, 3, 10):
            for name, start, corner, end, phase, walking in cases:
                for schedule in ([20]*32, [1,17,25,100,3,7,250]*3):
                    e.write(actor+0x338, struct.pack('<ff', *start))
                    e.write(actor+0x3B7, b'\x01\x01')
                    e.call(0x5C06C9, (actor,), ecx=controller)
                    e.call(0x5D086B, (actor, scene), ecx=remote)
                    e.w32(actor+0x47C, remote)
                    e.w32(message+8, int(walking))
                    e.write(message+0x10, bytes([direction, rate]))
                    for offset, value in zip((0x24,0x28,0x2C,0x30), corner+end):
                        e.w32(message+offset, value)
                    e.call(0x5D1348, (message,), ecx=remote)
                    e.w32(remote+0x34, phase)
                    frames = []
                    for dt in schedule:
                        e.call(0x5D13C2, (dt,), ecx=remote)
                        frames.append(dict(dt=dt, position=struct.unpack('<ff', e.read(actor+0x338, 8)),
                                           phase=struct.unpack('<i',e.read(remote+0x34,4))[0]))
                    print(json.dumps(dict(case=name, direction=direction, rate=rate, start=start,
                                          corner=corner, end=end, phase=phase, walking=walking, frames=frames)))


if __name__ == '__main__':
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('client_root', type=pathlib.Path)
    run(parser.parse_args().client_root)
