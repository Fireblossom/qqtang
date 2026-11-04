"""Execute 00602BAB/00881E00 and capture its 0x293 result-display input.

Controlled dependencies: profile lookup, app singleton and actor result
animation transition. Native field projection and experience arithmetic run.
The display constructor is an observation boundary; no network/server writes.
"""

import argparse
import json
import pathlib
import struct
import pefile

from win32emu import EmuStop, Win32Emu


def observe(root, points, gain, scores, rule_id=1, game_type=0, remark=0,
            players=None, local_id=1, name=None, pve_points=None):
    e = Win32Emu(root / "Client.exe", root)
    e.handlers["__dllonexit"] = (lambda e: e.arg(0)), 0
    e.w32(0x85DA74, e.r32(0x85DA74) | 1)
    app, scene, handler, actor, data = [e.malloc(n) for n in (0x60000, 0x60000, 0x100, 0x1000, 358)]
    inputs = players if players is not None else [dict(id=1, result=2, points=points, gain=gain,
                                                      scores=scores, remark=remark)]
    actors = {local_id: actor}
    for player in inputs:
        if player["id"] not in actors:
            actors[player["id"]] = e.malloc(0x1000)

    def patch(address, name, function, pop):
        stub = e.add_stub(name)
        e.handlers[name] = function, pop
        e.write(address, b"\xe9" + struct.pack("<i", stub - address - 5))

    patch(0x5A1DE1, "app", lambda e: app, 0)
    patch(0x5C09CD, "actor_result_state", lambda e: 0, 0)
    patch(0x5C0A36, "actor_loss_state", lambda e: 0, 0)

    def profile(e):
        destination = e.arg(1)
        e.write(destination, bytes(0x23E0))
        e.w32(destination, 1000005)
        player = next(player for player in inputs if player["id"] == e.arg(0))
        e.w32(destination + 0x34, player["points"])
        if pve_points is not None:
            e.w32(destination + 0x4F, pve_points)
        return 1

    patch(0x5A3BE2, "profile", profile, 2)
    if pve_points is not None:
        from decrypt_pve import SectionReader, cfg_sizes
        text = SectionReader(root)(b"config/pvedata.dat", cfg_sizes(root)["pvedata"])
        module = e.make_object("RoomModule", 26)
        e.w32(app + 0x7D0, module)

        def read_cfg(e):
            if e.cstr(e.arg(0)).lower() != b"config/pvedata.dat":
                raise RuntimeError("unexpected config")
            e.write(e.arg(2), text + b"\0")
            return 1

        e.handlers["RoomModule+0x64"] = read_cfg, 3
    e.w32(handler + 4, scene)
    e.w32(app + 0x37F8, scene)
    e.w32(scene + 8, app)
    e.w32(scene + 0x4B34, actor)
    e.w32(scene + 0x4B38, actor)
    e.w32(scene + 0x51A4, game_type)
    for index, player in enumerate(inputs):
        address = actors[player["id"]]
        e.write(address + 0x328, struct.pack("<H", player["id"]))
        e.write(address + 0x394, bytes([1, player.get("team", 1), index]))
    actor_vector = e.malloc(len(inputs) * 4)
    e.write(actor_vector, struct.pack(f"<{len(inputs)}I", *(actors[player["id"]] for player in inputs)))
    # 005A287F receives Scene+1310 (actor manager); its vector is +133C.
    e.w32(scene + 0x264C, actor_vector)
    e.w32(scene + 0x2650, actor_vector + len(inputs) * 4)
    rule = e.call(0x5F47A9, (scene, rule_id))
    e.w32(scene + 0x4EA4, rule)
    e.write(data, struct.pack("<IB", 1000, len(inputs)))
    for index, player in enumerate(inputs):
        offset = data + 5 + index * 44
        e.write(offset, struct.pack("<HBIIB", player["id"], player["result"], player.get("remark", 0),
                                    player["gain"], len(player["scores"])))
        e.write(offset + 12, struct.pack("<4I", 2, 3, 100, 7))
        e.write(offset + 28, struct.pack("<4I", *(player["scores"] + [0] * 4)[:4]))
    captured = []

    def capture(e):
        raw = e.read(e.arg(1), 0x293)
        rows = []
        for index in range(raw[2]):
            row = raw[3 + index * 82:85 + index * 82]
            rows.append(dict(
                             player_id=struct.unpack_from("<H", row)[0], result=row[28],
                             remark=struct.unpack_from("<I", row, 29)[0],
                             score_total=struct.unpack_from("<I", row, 33)[0],
                             level=struct.unpack_from("<I", row, 37)[0],
                             progress=struct.unpack_from("<ff", row, 41), fields=row[49],
                             counts=struct.unpack_from("<4I", row, 50),
                             scores=struct.unpack_from("<4I", row, 66)))
            if players is not None:
                rows[-1]["index"] = row[6]
        header = dict(local_id=struct.unpack_from("<H", raw)[0], count=raw[2])
        captured.append(header | (dict(rows=rows) if players is not None else rows[0]))
        raise EmuStop("captured")

    patch(0x612C86, "capture", capture, 2)
    try:
        e.call(0x602BAB, (0xFBB, data), ecx=handler)
    except EmuStop:
        if not captured:
            raise
    if len(captured) != 1:
        raise RuntimeError("expected one display constructor")
    record = dict(points=points, gain=gain, scores=scores, rule=rule_id,
                  game_type=game_type, remark=remark, display=captured[0])
    if players is not None:
        record = dict(kind="order", case=name, players=inputs, rule=rule_id,
                      game_type=game_type, local_id=local_id, display=captured[0])
    if pve_points is not None:
        record["pve_points"] = pve_points
    print(json.dumps(record))


if __name__ == "__main__":
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("client_root", type=pathlib.Path)
    parser.add_argument("--orders", action="store_true")
    parser.add_argument("--pve", action="store_true")
    arguments = parser.parse_args()
    root = arguments.client_root
    if arguments.pve:
        import xml.etree.ElementTree as ET
        from decrypt_pve import SectionReader, cfg_sizes
        text = SectionReader(root)(b"config/pvedata.dat", cfg_sizes(root)["pvedata"])
        thresholds = sorted(int(node.attrib["point"]) for node in ET.fromstring(text.decode("gbk"))
                            if node.tag.startswith("lev_"))
        print(json.dumps(dict(kind="pve_thresholds", values=thresholds)))
        for pve_points in (0, 100, 572, 573, 600, 1288, 1289, 428326798, 428326799):
            for game_type in (0, 1, 2):
                observe(root, 950, 50, [10, 20, 30, 40], rule_id=12,
                        pve_points=pve_points, game_type=game_type)
        for scores in ([], [10], [0, 0, 30, 100]):
            observe(root, 950, 50, scores, rule_id=12, pve_points=100)
        raise SystemExit(0)
    if arguments.orders:
        players = [dict(id=player, result=result, points=1000 * player, gain=10,
                        scores=[score], team=team)
                   for player, result, score, team in ((4, 0, 400, 2), (1, 1, 100, 1),
                                                        (3, 2, 300, 2), (2, 2, 200, 1))]
        cases = [("mixed", players), ("reverse", list(reversed(players))),
                 ("all_win", [p | dict(result=2) for p in players]),
                 ("all_draw", [p | dict(result=1) for p in players]),
                 ("tied_scores", [p | dict(scores=[100]) for p in players]),
                 ("rank_by_score", [p | dict(result=2, scores=[500 - p["scores"][0]]) for p in players]),
                 ("eight_players", [dict(id=player, result=index % 3, points=player * 1000,
                                         gain=10, scores=[800 - index * 100], team=index % 2 + 1)
                                    for index, player in enumerate((8, 3, 7, 1, 6, 2, 5, 4))])]
        for name, inputs in cases:
            for local_id in (1, 2, 4):
                for rule in (1, 8):
                    observe(root, 0, 0, [], players=inputs, local_id=local_id, rule_id=rule, name=name)
        raise SystemExit(0)
    pe = pefile.PE(str(root / "Client.exe"))
    thresholds = struct.unpack("<181I", pe.get_data(0x7F7780 - pe.OPTIONAL_HEADER.ImageBase, 724))
    print(json.dumps(dict(kind="thresholds", values=thresholds)))
    for points, gain in ((0, 50), (123, 50), (950, 49), (950, 50), (950, 100),
                         (1000, 50), (4999, 5000), (9999, 1), (10000, 50),
                         (1631150000, 50), (1621150200, 10), (1621151520, 50)):
        observe(root, points, gain, [52, 63, 2000])
    for rule in (1, 2, 3, 4, 5, 6, 7, 8):
        for game_type in (0, 1, 2):
            observe(root, 950, 100, [10, 20, 30, 40], rule, game_type, 9)
