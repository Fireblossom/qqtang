import random
import struct
import types
import unittest

from qqtclient import pet as pet_host
from qqtclient.net import pet

CONFIG = """[public]
version = 1

[PetExperience]
ExpTable = 0,180,580,1380,2920,5230,8570,16490,29810,51410

[SkillName]
skill1 = 糖币之光10%
skill51 = 长名字长名字长名字长名字

[Pet25001]
Level1 = 100
Level4 = 1
Level7 = 2

[pet25010]
Level1=100
Level4=19
"""


def pet_bytes(pet_id, type_id=25001, state=2, name="小糖", skills=b"\x01\x33", level=5, experience=200):
    return (struct.pack(">IIIIHHH", pet_id, type_id, experience, 900, level, 95, state)
            + name.encode("gbk").ljust(12, b"\0") + struct.pack(">I", len(skills)) + skills)


def pets_payload(base, extra=()):
    return bytes([len(base)]) + b"".join(base) + bytes([len(extra)]) + b"".join(extra)


class PetTest(unittest.TestCase):
    def setUp(self) -> None:
        self.sent, self.calls = [], []
        namespace = {"Win_IsVisible": lambda widget: 0}
        for name in ("UpdatePetInfo", "ui_msgBox"):
            namespace[name] = lambda *args, name=name: self.calls.append((name,) + args)
        runtime = types.SimpleNamespace(namespace=namespace, current_screen="selRoom", find=lambda path: object(),
                                        show_msgbox=lambda *args: self.calls.append(("msgbox", args[0])))
        item = types.SimpleNamespace(item_id=26001, quantity=3, status=0, role_id=0, effect=0, color=0, buy_time=0, available_period=0)
        self.session = types.SimpleNamespace(uin=1000005, handlers={}, profile=types.SimpleNamespace(items=[item]),
                                             send=lambda command, payload, route=2: self.sent.append((command, payload, route)) or True)
        resources = types.SimpleNamespace(read=lambda path: CONFIG.encode("gbk"))
        self.natives = pet_host.build_pet_natives(runtime, self.session, resources)

    def deliver(self, command, payload):
        self.session.handlers[command](types.SimpleNamespace(payload=payload))

    def test_config_queries(self) -> None:
        rate = self.natives["GetPetLevelRateInfo"]
        self.assertEqual((rate(0).m_fRate, rate(0).m_unNextLevelExp), (0.0, 180))
        self.assertEqual((rate(180).m_fRate, rate(180).m_unNextLevelExp), (0.0, 580))
        self.assertEqual((rate(60000).m_fRate, rate(60000).m_unNextLevelExp), (1.0, 51410))
        self.assertAlmostEqual(rate(380).m_fRate, 0.5)
        res = self.natives["GetPetResId"]
        self.assertEqual((res(25010, 5), res(25001, 2), res(25001, 9), res(99999, 5), res(25001, 0)), (19, 100, 2, 0, 0))
        self.assertEqual(self.natives["GetSkillNameById"](1), "糖币之光10%")
        self.assertEqual(self.natives["GetSkillNameById"](51), "长名字长名字长名字")  # 19 bytes, half character dropped
        self.assertEqual(self.natives["GetSkillDescription"](9), "")

    def test_list_reply_fills_the_cache(self) -> None:
        self.assertEqual(self.natives["GetPlayerAllPetsId"](), [])
        self.assertEqual(self.natives["GetCurActivePetInfo"]().m_dwPetId, 0)
        self.deliver(pet.PLAYER_PETS_COMMAND, pets_payload([pet_bytes(7), pet_bytes(8, name="二号")], [pet_bytes(30)]))
        self.assertEqual(self.natives["GetPlayerAllPetsId"](), [7, 8, 30])
        self.assertEqual(self.calls[-1], ("UpdatePetInfo",))
        # Nothing carried: the original hands back the last pet.
        self.assertEqual(self.natives["GetCurActivePetInfo"]().m_dwPetId, 30)
        self.assertIsNone(self.session.pets.active())
        info = self.natives["GetPetBaseInfo"](8)
        self.assertEqual((info.m_szPetName, info.m_dwSkillCount, ord(info.m_szSkills[1]), len(info.m_szSkills)), ("二号", 2, 0x33, 20))
        self.assertEqual(self.natives["GetPetBaseInfo"](99).m_dwPetId, 0)
        self.assertEqual(self.natives["GetPetAllSkillId"](7), [1, 0x33])

    def test_take_reply_replaces_and_rests_the_others(self) -> None:
        self.deliver(pet.PLAYER_PETS_COMMAND, pets_payload([pet_bytes(7, state=1), pet_bytes(8)]))
        self.natives["TakePet"](8)
        command, payload, route = self.sent[-1]
        self.assertEqual((command, route), (pet.HANDLE_PET_COMMAND, 4))
        self.assertEqual(struct.unpack_from(">I", payload, 0)[0], 1000005)
        self.assertEqual(struct.unpack_from(">II", payload, 8) + (payload[16:],), (2, 8, b"\0" * 20))
        self.deliver(pet.HANDLE_PET_COMMAND, struct.pack(">hIH", 0, 2, 0) + pet_bytes(8, state=1))
        self.assertEqual(self.session.pets.active().pet_id, 8)
        self.assertEqual(self.natives["GetPetBaseInfo"](7).m_wPetState, 2)
        self.assertEqual(self.calls[-1], ("UpdatePetInfo",))

    def test_failure_refreshes_then_shows_the_reason(self) -> None:
        self.deliver(pet.PLAYER_PETS_COMMAND, pets_payload([pet_bytes(7)]))
        reason = "道具数量不足".encode("gbk")
        self.deliver(pet.HANDLE_PET_COMMAND, struct.pack(">hIH", 3, 1, len(reason)) + reason + bytes(38))
        self.assertEqual(self.calls[-3:], [("UpdatePetInfo",), ("msgbox", "道具数量不足"), ("ui_msgBox", 3)])
        self.assertEqual(self.natives["GetPetBaseInfo"](7).m_dwPetExperience, 200)

    def test_free_and_adopt_wait_for_the_list(self) -> None:
        self.deliver(pet.PLAYER_PETS_COMMAND, pets_payload([pet_bytes(7)]))
        self.deliver(pet.HANDLE_PET_COMMAND, struct.pack(">hIH", 0, 4, 0) + pet_bytes(7, experience=0))
        self.assertEqual(self.natives["GetPetBaseInfo"](7).m_dwPetExperience, 200)

    def test_raise_pet_picks_the_event_from_the_item(self) -> None:
        for item, event in ((26001, 1), (27005, 6), (28001, 7)):
            self.natives["RaisePet"](item, 7)
            self.assertEqual(struct.unpack_from(">I", self.sent[-1][1], 8)[0], event)
            self.assertEqual(self.sent[-1][1][16:].rstrip(b"\0"), str(item).encode())
        count = len(self.sent)
        self.natives["RaisePet"](25001, 7)
        self.assertEqual(len(self.sent), count)

    def test_rename_skips_the_same_name_and_cuts_at_19_bytes(self) -> None:
        self.deliver(pet.PLAYER_PETS_COMMAND, pets_payload([pet_bytes(7)]))
        self.natives["ChangePetName"](7, "小糖")
        self.assertEqual(self.sent, [])
        self.natives["ChangePetName"](7, "一二三四五六七八九十")
        parameter = self.sent[-1][1][16:]
        self.assertEqual((len(parameter), parameter.rstrip(b"\0").decode("gbk")), (20, "一二三四五六七八九"))

    def test_pet_items(self) -> None:
        items = self.natives["GetAllMyPetItems"]()
        self.assertEqual([(item.m_nItemID, item.m_iNumOfItem) for item in items], [(26001, 3)])



class RoomPetTest(unittest.TestCase):
    def owner(self, x=200, y=200, walking=False, direction=3):
        return types.SimpleNamespace(x=x, y=y, walking=walking, direction=direction)

    def test_far_owner_pulls_the_pet_beside_it(self) -> None:
        from qqtclient.roomwalk import RoomPet
        owner = self.owner()
        room_pet = RoomPet(19, owner, random.Random(1))
        self.assertTrue(20 <= abs(room_pet.x - 200) < 70 and abs(room_pet.y - 200) <= 20)
        owner.x, owner.y = 450, 450
        room_pet.update(owner, 16)
        self.assertLess((room_pet.x - 450) ** 2 + (room_pet.y - 450) ** 2, 90000)

    def test_pet_follows_a_walking_owner(self) -> None:
        from qqtclient.roomwalk import RoomPet
        owner = self.owner()
        room_pet = RoomPet(19, owner, random.Random(2))
        room_pet.x, room_pet.y = 100.0, 200.0
        owner.walking = True
        room_pet.update(owner, 100)  # d = 10000: 300 px/s
        self.assertEqual((round(room_pet.x), room_pet.direction, room_pet.walking), (130, 0, True))


if __name__ == "__main__":
    unittest.main()
