"""Original shop crafting lists, recipe learning, forge/egg requests and callbacks.

Tables are read from the user's client, never copied into this repository.
Inventory remains authoritative from 0x00D9; a result alone does not debit it.
"""

from __future__ import annotations

import configparser
import dataclasses
import time
import types
import xml.etree.ElementTree as ET
import zlib
from typing import Any, Callable

from .net import craft, shop
from .props import EMPTY_ITEM, item_view
from .script import call_script, log


@dataclasses.dataclass
class Material:
    item: int
    name: str
    quantity: int


@dataclasses.dataclass
class Recipe:
    category: str
    name: str
    book: int
    product: int
    description: str
    materials: list[Material]


def decode_table(raw: bytes) -> bytes:
    """QQTSection 0x1000BEC8: backwards XOR, then zlib."""
    data = bytearray(raw)
    if data:
        data[-1] ^= data[0]
        for index in range(len(data) - 2, -1, -1):
            data[index] ^= data[index + 1]
    return zlib.decompress(data).split(b"\0", 1)[0]


class CraftCatalog:
    def __init__(self, forge: bytes | None, combine: bytes | None):
        self.types: list[str] = []
        self.items: list[set[int]] = []
        self.materials: set[int] = set()
        self.costs: dict[str, list] = {}
        self.recipes: dict[int, Recipe] = {}
        if forge:
            ini = configparser.ConfigParser(interpolation=None, strict=False)
            ini.read_string(forge.decode("gbk"))

            def ids(section):
                return {int(word) for key, row in ini[section].items() if key.startswith("item.")
                        for word in row.split()}

            for index in range(1, ini.getint("forgetype", "count") + 1):
                section = f"forgetype.{index}"
                self.types.append(ini.get(section, "typename"))
                self.items.append(ids(section))
            self.materials = ids("material")
            for name in ("split", "revert"):
                self.costs[name] = [ini.getint(name, "itemid"), ini.getint(name, "itemnum"), ini.get(name, "itemname")]
        if combine:
            # Decode GB2312 before ElementTree (Expat does not support it).
            root = ET.fromstring(decode_table(combine).decode("gbk"))
            for category in root.find("合成"):
                for entry in category:
                    recipe = Recipe(category.tag, entry.tag, int(entry.attrib["bookid"]),
                                    int(entry.attrib["propid"]), entry.attrib.get("description", ""),
                                    [Material(int(m.attrib["matid"]), m.attrib["matname"], int(m.attrib["matnum"])) for m in entry])
                    self.recipes[recipe.product] = recipe


def build_craft_natives(runtime, session, resources, sprites=None) -> dict[str, Callable[..., Any]]:
    catalog: list[CraftCatalog] = []
    last_combine = -2.0

    def table() -> CraftCatalog:
        if not catalog:
            catalog.append(CraftCatalog(resources.read("config/avatarforge.ini"), resources.read("config/combineforge.ini")))
            # uiShop asks for item20051..57.img; the supplied client keeps
            # these icons under forge/forge1..7.img as itemCFG specifies.
            if sprites is not None:
                for item in catalog[0].materials:
                    desc = runtime.namespace.get("itemList", {}).get(item)
                    if desc:
                        sprites.aliases[f"res/uires/icon/item/item{item}.img"] = f"res/uires/icon/{desc[0]}/{desc[0]}{desc[1]}.img"
            log(f"[craft] {len(catalog[0].types)} forge types, {len(catalog[0].recipes)} recipes")
        return catalog[0]

    def script(name, *args):
        function = runtime.namespace.get(name)
        if callable(function):
            return call_script(function, *args)

    def inventory():
        return session.profile.items if session.profile else []

    def find(item):
        return next((row for row in inventory() if row.item_id == int(item)), EMPTY_ITEM)

    def quantity(item):
        row = find(item)
        return row.quantity if row.status != shop.STATUS_RECYCLED else 0

    def storage_view(row):
        desc = runtime.namespace.get("itemList", {}).get(row.item_id, ())
        return types.SimpleNamespace(m_stItem=item_view(row), m_szName=desc[2] if desc else "",
                                     m_szDescrip=desc[3] if len(desc) > 3 else "")

    def learned(recipe):
        return find(recipe.book).status == shop.STATUS_LEARNED

    def categories():
        # 0x10036497 counts categories even when none of their recipes were learned.
        return list(dict.fromkeys(recipe.category for recipe in table().recipes.values()))

    def recipes_for(index):
        groups = categories()
        if not 0 <= int(index) < len(groups):
            return []
        return [r for r in table().recipes.values() if r.category == groups[int(index)] and learned(r)]

    def BeginComForge():
        table()  # the original rebuilds its cached lists here; ours read current inventory

    def GetCombineType():
        return categories()

    def GetCombineItem(index):
        return [types.SimpleNamespace(iPropID=r.product, szPropName=r.name, szPropDescription=r.description,
                                     iCanBeNum=min((quantity(m.item) // m.quantity for m in r.materials), default=0))
                for r in recipes_for(index)]

    def GetCombineMeterial(product):
        recipe = table().recipes.get(int(product))
        return [types.SimpleNamespace(iMaterialID=m.item, szMaterialName=m.name,
                                     unHadMatNum=quantity(m.item), unNeedMatlNum=m.quantity)
                for m in recipe.materials] if recipe else []

    def IsBook(item):
        return int(any(r.book == int(item) for r in table().recipes.values()))

    def RequestLearnScroll(item):
        if IsBook(item) and find(item).status != shop.STATUS_LEARNED:
            # XSection +0x29C/+0x2A0: learn locally, then persist status 4 via 0x85.
            row = find(item)
            session.send(shop.ITEM_STATUS_COMMAND, shop.build_item_status(session.uin, [(int(item), shop.STATUS_LEARNED, row.role_id)]))

    def RequestCombine(product):
        nonlocal last_combine
        recipe = table().recipes.get(int(product))
        if recipe is None or not learned(recipe):
            return
        now = time.monotonic()
        if now - last_combine < 2:
            script("ui_ReceiveComResult", -1, "不要着急，请休息一下再进行合成！^_^")
            return
        if session.send(craft.COMBINE_COMMAND, craft.build_combine(session.uin, recipe.product, [m.item for m in recipe.materials])):
            last_combine = now

    def GetAvatarForgeType():
        return list(table().types)

    def GetAvatarForgeItem(index):
        index = int(index)
        if not 0 <= index < len(table().items):
            return [[], []]
        rows = [r for r in inventory() if r.item_id in table().items[index] and r.quantity > 0 and r.status != shop.STATUS_RECYCLED]
        # QQTSection 0x10005E20: effect=0 OR (effect>0 AND color=0).
        return [[item_view(r) for r in rows if r.effect == 0 or r.color == 0],
                [item_view(r) for r in rows if r.effect > 0]]

    def GetAvatarForgeMaterial():
        return [storage_view(r) for r in inventory() if r.item_id in table().materials and quantity(r.item_id) > 0]

    def GetItemInfoInStorage(item):
        return storage_view(find(item))

    def GetSplitInfo():
        return list(table().costs.get("split", [0, 0, ""]))

    def GetRevertInfo():
        return list(table().costs.get("revert", [0, 0, ""]))

    def RequestAvatarForge(item, material, operation):
        session.send(craft.FORGE_COMMAND, craft.build_forge(session.uin, int(item), int(material), int(operation)))

    def RequestBreakEgg(egg, hammer):
        session.send(craft.BREAK_EGG_COMMAND, craft.build_break_egg(session.uin, int(egg), int(hammer)))

    def on_combine(packet):
        result = craft.parse_combine(packet.payload)
        log(f"[craft] combine {result.item}: {result.result} {result.reason}")
        script("ui_ReceiveComResult", result.result, result.reason)

    def on_forge(packet):
        result = craft.parse_forge(packet.payload)
        log(f"[craft] forge {result.operation}/{result.item}: {result.result}, effect={result.effect} color={result.color}: {result.reason}")
        # QQTSection forwards these fields even on failure. Inventory arrives separately.
        script("ui_UpdateAvatarEffect", result.item, result.effect, result.color, result.result, result.reason)

    def on_egg(packet):
        result, item, reason = craft.parse_break_egg(packet.payload)
        log(f"[craft] egg: {result}, reward={item}: {reason}")
        script("NotifyBreakEggResult", int(result == 0), item, reason)

    old_items = session.handlers[shop.ITEM_ADD_NOTIFY_COMMAND]

    def on_items(packet):
        old_items(packet)
        update = shop.parse_item_add(packet.payload)
        if update.uin != session.uin or runtime.current_screen != "shop":
            return
        # The authoritative absolute update may come after the forge result.
        # Rebuild after it, so consumed crystals and changed list membership redraw.
        script("ui_UpdateAvatarForge")
        if runtime.namespace["Win_IsVisible"]("UI.shop.refineDlg"):
            script("ui_showAvatar")
            script("ui_showMaterial")
        if runtime.namespace["Win_IsVisible"]("UI.shop.storageDlg"):
            script("ui_UpdateShopStorage")

    session.handlers.update({craft.COMBINE_COMMAND: on_combine, craft.FORGE_COMMAND: on_forge,
                             craft.BREAK_EGG_COMMAND: on_egg,
                             shop.ITEM_ADD_NOTIFY_COMMAND: on_items})
    return {name: value for name, value in locals().items() if callable(value) and name[0].isupper()}
