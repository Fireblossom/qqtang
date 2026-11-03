import struct
import types
import unittest
import zlib

import pygame

from qqtclient.craft import CraftCatalog, build_craft_natives
from qqtclient.forge_preview import ForgePreview
from qqtclient.net import craft, shop
from qqtclient.net.player import ItemInfo


FORGE = b"""[forgetype]
count=1
[forgetype.1]
typename=cap
count=1
item.1=19 21 22 23
[material]
count=1
item.1=20057
[split]
itemid=1800
itemnum=50
itemname=gem
[revert]
itemid=1800
itemnum=25
itemname=gem
"""
XML = """<?xml version="1.0" encoding="gb2312"?>
<ComForgeFile version="110"><合成><药水>
<测试药水 bookid="24050" propid="20050" description="test">
<mat1 matid="30057" matname="铁" matnum="2"/>
<mat2 matid="30045" matname="灰" matnum="1"/>
<mat3 matid="30008" matname="草" matnum="3"/>
</测试药水></药水></合成></ComForgeFile>"""


def encoded_table():
    data = bytearray(zlib.compress(XML.encode("gbk")))
    for index in range(len(data) - 1):
        data[index] ^= data[index + 1]
    data[-1] ^= data[0]
    return bytes(data)


def item(item_id, count=1, status=0, effect=0, color=0):
    return ItemInfo(item_id, count, status, 0, effect, color, 0, 0xFFFFFFFF)


class CraftProtocolTest(unittest.TestCase):
    def test_requests_match_go_protocol_vectors(self):
        # Fields from internal/protocol/game/craft_test.go / break_egg_test.go.
        self.assertEqual(craft.build_combine(1000001, 20050, [30057, 30045, 30008], 0x2A5228BD), bytes.fromhex(
            "000f4241 2a5228bd 00 00004e52 00000000 0003 00007569 0000755d 00007538"))
        self.assertEqual(craft.build_forge(1000001, 300, 20057, 1, 0x12345678), bytes.fromhex(
            "000f4241 12345678 01 0000012c 00004e59"))
        self.assertEqual(craft.build_break_egg(1000001, 9003, 9013, 0x12345678), bytes.fromhex(
            "000f4241 12345678 00002335 0000232b"))

    def test_compact_gbk_responses(self):
        reason = "合成成功".encode("gbk")
        payload = bytes.fromhex("0000 00 00004e52 00000000 0003 00007569 0000755d 00007538 0008") + reason
        self.assertEqual((craft.parse_combine(payload).item, craft.parse_combine(payload).reason), (20050, "合成成功"))
        reason = "锻造成功".encode("gbk") + b"\0"
        result = craft.parse_forge(bytes.fromhex("0000 01 0000012c 00004e59 05 03 0009") + reason)
        self.assertEqual((result.effect, result.color, result.reason), (5, 3, "锻造成功"))
        self.assertEqual(craft.parse_break_egg(bytes.fromhex("0001 00000000 04") + "失败".encode("gbk")), (1, 0, "失败"))

    def test_truncated_counts_are_rejected(self):
        for parse, payload in ((craft.parse_combine, bytes.fromhex("0000 00 00004e52 00000000 0003 0000")),
                               (craft.parse_forge, bytes.fromhex("0000 01 0000012c 00004e59 05 03 0009")),
                               (craft.parse_break_egg, bytes.fromhex("0000 00000001 01"))):
            with self.subTest(parse=parse.__name__), self.assertRaises(ValueError):
                parse(payload)


class CraftHostTest(unittest.TestCase):
    def setUp(self):
        self.calls, self.sent = [], []
        self.runtime = types.SimpleNamespace(current_screen="shop", namespace={"Win_IsVisible": lambda path: True})
        for name in ("ui_UpdateAvatarForge", "ui_showAvatar", "ui_showMaterial", "ui_UpdateShopStorage",
                     "ui_ReceiveComResult", "ui_UpdateAvatarEffect", "NotifyBreakEggResult"):
            self.runtime.namespace[name] = lambda *args, name=name: self.calls.append((name, *args))
        self.session = types.SimpleNamespace(uin=1000001, handlers={shop.ITEM_ADD_NOTIFY_COMMAND: self.apply_items},
                                             profile=types.SimpleNamespace(items=[]),
                                             send=lambda c, p: self.sent.append((c, p)) or True)
        self.resources = types.SimpleNamespace(read=lambda path: FORGE if "avatar" in path else encoded_table())
        self.natives = build_craft_natives(self.runtime, self.session, self.resources)

    def apply_items(self, packet):
        update = shop.parse_item_add(packet.payload)
        if update.uin != self.session.uin:
            return
        for new in update.items:
            rows = self.session.profile.items
            index = next((i for i, row in enumerate(rows) if row.item_id == new.item_id), None)
            if index is None:
                rows.append(new)
            else:
                rows[index] = new

    def deliver(self, command, payload):
        self.session.handlers[command](types.SimpleNamespace(payload=payload))

    def test_learned_book_and_smallest_material_ratio(self):
        self.session.profile.items = [item(24050), item(30057, 8), item(30045, 9), item(30008, 7)]
        self.assertEqual(self.natives["GetCombineItem"](0), [])
        self.natives["RequestLearnScroll"](24050)
        self.assertEqual(self.sent[-1][0], shop.ITEM_STATUS_COMMAND)
        self.assertEqual(self.sent[-1][1][8:], bytes.fromhex("0001 00005df2 04 00"))
        self.assertEqual(self.natives["GetCombineItem"](0), [])  # wait for the inventory acknowledgement
        self.session.profile.items[0].status = shop.STATUS_LEARNED
        self.assertEqual(self.natives["GetCombineType"](), ["药水"])
        self.assertEqual(self.natives["GetCombineItem"](0)[0].iCanBeNum, 2)
        self.session.profile.items[-1].status = shop.STATUS_RECYCLED
        self.assertEqual(self.natives["GetCombineItem"](0)[0].iCanBeNum, 0)

    def test_forge_split_membership_and_crystal_consumption(self):
        self.session.profile.items = [item(19), item(21, effect=5, color=3), item(22, effect=5),
                                      item(23, status=2), item(20057, 0)]
        forge, revert = self.natives["GetAvatarForgeItem"](0)
        self.assertEqual([r.m_nItemID for r in forge], [19, 22])
        self.assertEqual([r.m_nItemID for r in revert], [21, 22])
        self.assertEqual(self.natives["GetAvatarForgeMaterial"](), [])
        self.session.profile.items[-1].quantity = 1
        self.assertEqual(self.natives["GetAvatarForgeMaterial"]()[0].m_stItem.m_iNumOfItem, 1)

    def test_inventory_is_applied_before_ui_refresh(self):
        new = item(19, effect=5, color=3)
        self.session.profile.items = [item(19)]
        self.deliver(shop.ITEM_ADD_NOTIFY_COMMAND, struct.pack(">IIIIH", 1000001, 0, 0, 0, 1) + struct.pack(
            shop.ITEM_INFO_FORMAT, *vars(new).values()))
        self.assertEqual([r.m_nItemID for r in self.natives["GetAvatarForgeItem"](0)[1]], [19])
        self.assertEqual([call[0] for call in self.calls], ["ui_UpdateAvatarForge", "ui_showAvatar", "ui_showMaterial", "ui_UpdateShopStorage"])

    def test_egg_success_boolean_and_result_do_not_change_inventory(self):
        self.session.profile.items = [item(9003), item(9013)]
        self.deliver(craft.BREAK_EGG_COMMAND, bytes.fromhex("0000 00000013 00"))
        self.assertEqual(self.calls[-1], ("NotifyBreakEggResult", 1, 19, ""))
        self.assertEqual([r.quantity for r in self.session.profile.items], [1, 1])
        self.deliver(craft.BREAK_EGG_COMMAND, bytes.fromhex("0001 00000000 00"))
        self.assertEqual(self.calls[-1], ("NotifyBreakEggResult", 0, 0, ""))


class ForgePreviewTest(unittest.TestCase):
    def setUp(self):
        self.red = pygame.Surface((1, 1), pygame.SRCALPHA)
        self.red.fill((255, 0, 0, 255))
        self.blue = pygame.Surface((1, 1), pygame.SRCALPHA)
        self.blue.fill((0, 0, 255, 255))

    def test_part_direction_interval_anchor_and_same_wear_keeps_time(self):
        path = "object/cap/cap1_stand.img"
        frames = [(None, 0, 0)] * 6 + [(self.red, 22, 11), (self.blue, 23, 12)]
        sprites = types.SimpleNamespace(get=lambda p: frames, directions={path: 4})
        preview = ForgePreview(types.SimpleNamespace(read=lambda p: None), sprites)
        preview.wear(19, ("cap", 1), "", 100)
        surface = pygame.Surface((200, 500), pygame.SRCALPHA)
        preview.draw(surface, 100)
        self.assertEqual(surface.get_at((72, 321)), (255, 0, 0, 255))
        surface.fill((0, 0, 0, 0))
        preview.wear(19, ("cap", 1), "", 300)
        preview.draw(surface, 300)
        self.assertEqual(surface.get_at((73, 322)), (0, 0, 255, 255))
        preview.wear(0, None, "", 400)
        surface.fill((0, 0, 0, 0))
        preview.draw(surface, 400)
        self.assertEqual(surface.get_bounding_rect().size, (0, 0))

    def test_bubble_interval_and_offset_are_independent_of_part_rules(self):
        sprites = types.SimpleNamespace(get=lambda p: [(self.red, 3, 17), (self.blue, 3, 17)], directions={})
        preview = ForgePreview(types.SimpleNamespace(read=lambda p: None), sprites)
        preview.wear(119, ("bomb", 3), "", 0)
        surface = pygame.Surface((200, 500), pygame.SRCALPHA)
        preview.draw(surface, 299)
        self.assertEqual(surface.get_at((83, 361)), (255, 0, 0, 255))
        preview.draw(surface, 300)
        self.assertEqual(surface.get_at((83, 361)), (0, 0, 255, 255))

    def test_positive_effect_layers_go_behind_the_part(self):
        # Both tracks and the base occupy the same pixel: the foreground
        # track must win, regardless of their order in the effect file.
        key = [[0, ((0, 0, 0), (1, 1), 0, (255, 255, 255), 1)],
               [1000, ((0, 0, 0), (1, 1), 0, (255, 255, 255), 1)]]
        track = lambda number, layer: ((number, ("", 0, 100, 0), (0,), (0,), layer, (0, 0)), key)
        raw = repr((9, [track(1, 0), track(2, 1)], [1, 0])).encode()
        frames = {"object/cap/cap1_stand.img": [(self.red, 50, 84)],
                  "object/magic/magic0001.img": [(self.blue, 0, 0)],
                  "object/magic/magic0002.img": [(self.red, 0, 0)]}
        preview = ForgePreview(types.SimpleNamespace(read=lambda p: raw),
                               types.SimpleNamespace(get=frames.get, directions={}))
        preview.wear(19, ("cap", 1), "test.eff", 0)
        surface = pygame.Surface((200, 500), pygame.SRCALPHA)
        preview.draw(surface, 100)
        self.assertEqual(surface.get_at((100, 394)), (0, 0, 255, 255))


if __name__ == "__main__":
    unittest.main()
