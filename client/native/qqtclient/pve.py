"""Adventure (PVE) level data decrypted by tools/decrypt_pve.py into
generated/pve (config/pvedata.dat and config/battlefield_map16xx.dat)."""

from __future__ import annotations

import dataclasses
import functools
import pathlib
import xml.etree.ElementTree as ET

PVE_ROOT = pathlib.Path(__file__).resolve().parents[1] / "generated" / "pve"


@dataclasses.dataclass(frozen=True)
class PVELevel:
    """One <lev_N> of pvedata.xml: the adventure attributes from that many
    courage points on (Client.exe 0x603023 reads the same table)."""
    level: int
    point: int
    bomb_num: int
    bomb_pow: int
    re_speed: float
    hp: int
    speed: int


@functools.cache
def levels(root: pathlib.Path = PVE_ROOT) -> tuple[PVELevel, ...]:
    path = root / "pvedata.xml"
    if not path.is_file():
        return ()
    result = []
    for node in ET.fromstring(path.read_bytes().replace(b'encoding="gb2312"', b'encoding="utf-8"')):
        if not node.tag.startswith("lev_"):
            continue
        a = node.attrib
        result.append(PVELevel(int(node.tag[4:]), int(a.get("point", 0)), int(a.get("bombnum", 0)), int(a.get("bombpow", 0)),
                               float(a.get("resspeed", 0)), int(a.get("hp", 0)), int(a.get("speed", 0))))
    return tuple(sorted(result, key=lambda level: level.point))


def level_for(points: int, root: pathlib.Path = PVE_ROOT) -> PVELevel | None:
    """The highest level whose threshold the courage points reach."""
    chosen = None
    for level in levels(root):
        if points >= level.point:
            chosen = level
    return chosen
