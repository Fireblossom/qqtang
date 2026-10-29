"""Map files for drawing a battle: map/mapDesc.py, map/*.map and
object/mapElem/mapElem.py.

The rules (collision, spawns, hidden items) come from the Go sidecar; this
module only reads what is needed to draw the field. Layouts follow
internal/game/mapdata/catalog.go (loadCompetitiveMapMetadata,
competitiveMapGeometry) and competitive_battlefield.go.
"""

from __future__ import annotations

import ast
import dataclasses
import struct

from ..resources import Resources

CELL = 40
V3_WIDTH, V3_HEIGHT = 15, 13
LAYERS = 3
# Layer order inside the file: 0 top objects, 1 walls/boxes/scenery, 2 floor.
LAYER_TOP, LAYER_WALL, LAYER_FLOOR = range(LAYERS)

# Element ID = family * 1000 + n -> object/mapElem/<family dir>/elem<n>_<action>.img
ELEMENT_FAMILIES = {
    1: "desert", 2: "snow", 3: "town", 4: "mine", 5: "water", 6: "field", 7: "bomb", 8: "bun", 9: "pig",
    10: "treasure", 11: "match", 12: "sculpture", 13: "machine", 14: "box", 15: "practice", 16: "exploration",
    17: "common", 18: "pve", 19: "tank",
}


@dataclasses.dataclass
class MapDescription:
    map_id: int
    family: str
    name: str
    players: int
    file_name: str
    thumbnail: str
    music: str


@dataclasses.dataclass
class MapFile:
    version: int
    rule: int
    width: int
    height: int
    layers: list[list[int]]  # LAYERS x (width * height), row-major

    def element(self, layer: int, row: int, col: int) -> int:
        return self.layers[layer][row * self.width + col]


@dataclasses.dataclass
class ElementInfo:
    offset: tuple[int, int] = (0, 0)  # how far the image reaches left of and above its cell
    size: tuple[int, int] = (1, 1)
    level: int = 256
    life_time: int = -1
    can_hide: bool = False


def load_descriptions(resources: Resources) -> dict[int, MapDescription]:
    """map/mapDesc.py: mapDesc = [(id, family, name, w, h, players, file, thumbnail, music, ...), ...]."""
    data = resources.read("map/mapDesc.py")
    if data is None:
        return {}
    tree = ast.parse(data.decode("gbk"))
    for node in tree.body:
        if isinstance(node, ast.Assign) and any(isinstance(target, ast.Name) and target.id == "mapDesc" for target in node.targets):
            rows = ast.literal_eval(node.value)
            return {row[0]: MapDescription(row[0], row[1], row[2], row[5], row[6], row[7], row[8]) for row in rows}
    return {}


def parse_map(data: bytes) -> MapFile:
    if len(data) < 12:
        raise ValueError("map header is truncated")
    version, rule = struct.unpack_from("<II", data, 0)
    if version == 4:
        width, height = struct.unpack_from("<II", data, 12)
        header = 20
    else:
        width, height, header = V3_WIDTH, V3_HEIGHT, 12
    cells = width * height
    if len(data) < header + LAYERS * cells * 4:
        raise ValueError("map layers are truncated")
    layers = [list(struct.unpack_from(f"<{cells}i", data, header + layer * cells * 4)) for layer in range(LAYERS)]
    return MapFile(version, rule, width, height, layers)


def load_elements(resources: Resources) -> dict[int, ElementInfo]:
    """object/mapElem/mapElem.py: class QQTMapElem<ID>(QQTMapElem) with literal attributes."""
    data = resources.read("object/mapElem/mapElem.py")
    if data is None:
        return {}
    elements: dict[int, ElementInfo] = {}
    for node in ast.parse(data.decode("utf-8", "replace")).body:
        if not isinstance(node, ast.ClassDef) or not node.name.startswith("QQTMapElem") or node.name == "QQTMapElem":
            continue
        values: dict[str, object] = {}
        for statement in node.body:
            if isinstance(statement, ast.Assign) and len(statement.targets) == 1 and isinstance(statement.targets[0], ast.Name):
                try:
                    values[statement.targets[0].id] = ast.literal_eval(statement.value)
                except ValueError:
                    pass
        element_id = int(values.get("imageID", node.name[len("QQTMapElem"):]))
        elements[element_id] = ElementInfo(
            offset=tuple(values.get("offset", (0, 0))), size=tuple(values.get("size", (1, 1))),
            level=int(values.get("Level", 256)), life_time=int(values.get("LifeTime", -1)),
            can_hide=bool(values.get("canHide", 0)),
        )
    return elements


def element_image(element_id: int, action: str = "stand") -> str | None:
    family = ELEMENT_FAMILIES.get(element_id // 1000)
    if family is None:
        return None
    return f"object/mapElem/{family}/elem{element_id % 1000}_{action}.img"
