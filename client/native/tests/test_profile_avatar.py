"""Player information dialog avatar (Client.exe 0x0054E8B0, 0x00442210)."""

import pathlib
import sys
import types
import unittest

sys.path.insert(0, str(pathlib.Path(__file__).resolve().parent.parent))
from qqtclient import profile_avatar
from qqtclient.avatar import REMOVED_PART


def item(item_id, role_id):
    return types.SimpleNamespace(item_id=item_id, role_id=role_id)


class ProfileAvatarTest(unittest.TestCase):
    def test_cap_and_hair_rules(self):
        avatar = profile_avatar.ProfileAvatar(role=1)
        profile_avatar.wear(avatar, "cap", 12)
        self.assertEqual(avatar.wear, {"cap": 12, "hair": REMOVED_PART})
        profile_avatar.wear(avatar, "hair", 30)
        self.assertEqual(avatar.wear, {"cap": 100, "hair": 30})
        profile_avatar.wear(avatar, "hair", 0)
        self.assertEqual(avatar.wear, {"cap": REMOVED_PART, "hair": REMOVED_PART})

    def test_dress_wears_items_of_the_players_role(self):
        used = []
        runtime = types.SimpleNamespace(namespace={"playerInfo_useCommItem": lambda item_id: used.append(item_id)})
        avatar, dress, natives = profile_avatar.build_profile_avatar(runtime)
        avatar.wear["cap"] = 5
        found = types.SimpleNamespace(game_info=types.SimpleNamespace(role_id=3),
                                      items=[item(101, 3), item(102, 0), item(103, 4), item(104, 3)])
        dress(found)
        self.assertEqual((avatar.role, avatar.wear, used), (3, {}, [101, 104]))
        natives["selRoom_playerWear"]("cloth", 7, 0, 0)
        self.assertEqual(avatar.wear, {"cloth": 7})
        dress(types.SimpleNamespace(game_info=types.SimpleNamespace(role_id=23), items=[]))
        self.assertEqual(avatar.role, 1)  # 0 or above 22 falls back to role 1


if __name__ == "__main__":
    unittest.main()
