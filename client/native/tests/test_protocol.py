"""Protocol tests that run without a server."""

from __future__ import annotations

import hashlib
import hmac
import pathlib
import struct
import sys
import unittest

sys.path.insert(0, str(pathlib.Path(__file__).resolve().parent.parent))

from qqtclient.net import accountauth, chat, directory, friend, game, lobby, match, player, qqtea, room, shop  # noqa: E402

KEY = bytes(range(0x10, 0x20))


class QQTEATest(unittest.TestCase):
    def test_round_trip_all_padding_lengths(self) -> None:
        for size in range(0, 40):
            plaintext = bytes(range(size))
            ciphertext = qqtea.encrypt(plaintext, KEY)
            self.assertEqual(len(ciphertext) % 8, 0)
            self.assertEqual(qqtea.decrypt(ciphertext, KEY), plaintext)

    def test_deterministic_with_entropy(self) -> None:
        entropy = bytes([0xA5, 0x5A, 0xC3, 0x3C] * 4)
        self.assertEqual(qqtea.encrypt(b"hello", KEY, entropy), qqtea.encrypt(b"hello", KEY, entropy))

    def test_rejects_corrupt_ciphertext(self) -> None:
        ciphertext = bytearray(qqtea.encrypt(b"hello world", KEY))
        ciphertext[-1] ^= 1
        with self.assertRaises(ValueError):
            qqtea.decrypt(bytes(ciphertext), KEY)


class AccountAuthTest(unittest.TestCase):
    def test_proof_matches_server_verification(self) -> None:
        # Mirror accountauth.VerifyProof: recover ClientKey from the proof and
        # compare its hash with the stored key derived from the password.
        password, salt, nonce, uin, iterations = b"123456", bytes(16), bytes(range(32)), 1000001, 100_000
        proof = accountauth.client_proof(password, salt, iterations, nonce, uin)
        salted = hashlib.pbkdf2_hmac("sha256", password, salt, iterations, 32)
        stored = hashlib.sha256(hmac.new(salted, b"Client Key", hashlib.sha256).digest()).digest()
        signature = hmac.new(stored, accountauth.SIGNATURE_LABEL + struct.pack(">I", uin) + nonce, hashlib.sha256).digest()
        client_key = bytes(a ^ b for a, b in zip(proof, signature))
        self.assertEqual(hashlib.sha256(client_key).digest(), stored)

    def test_frame_and_result(self) -> None:
        self.assertEqual(accountauth.frame(1, struct.pack(">I", 7)), struct.pack(">I", 17) + b"QQTAUTH1\x01" + struct.pack(">I", 7))
        name = "糖一".encode()
        data = struct.pack(">I", 0) + b"QQTAUTH1\x04" + bytes([1, 2]) + b"ok" + bytes([1, len(name)]) + name
        self.assertEqual(accountauth.parse_result(data), accountauth.AuthResult(True, "ok", "糖一", 1))


def hall_payload() -> bytes:
    """Same layout as directory.BuildLocalHallPayload with the default local config."""
    location = "东部电信".encode("gbk")
    section = "自由1区".encode("gbk")
    payload = struct.pack(">HII", 0, 0x004F5D31, 1)
    payload += bytes([1]) + struct.pack(">H", 0x32E9) + bytes([len(location)]) + location + bytes(4)
    payload += bytes([1]) + struct.pack(">I", 1) + struct.pack(">I", struct.unpack("<I", bytes([127, 0, 0, 1]))[0]) + struct.pack(">HH", 18000, 18000)
    payload += bytes([1]) + "自由频道".encode("gbk").ljust(20, b"\0") + struct.pack(">IH", 1, 1)
    payload += bytes([len(section)]) + section + struct.pack(">HIHH", 1, 1, 500, 100) + bytes(8) + struct.pack(">H", 0x32E9) + bytes(13)
    return payload


class DirectoryTest(unittest.TestCase):
    def test_parse_default_hall(self) -> None:
        payload = hall_payload()
        self.assertEqual(len(payload), 107)  # directory.LocalHallPayloadSize
        hall = directory.parse_hall(payload)
        self.assertEqual(hall.locations, [directory.Location(0x32E9, "东部电信")])
        self.assertEqual(hall.servers, [directory.Server(1, "127.0.0.1", 18000, 18000)])
        self.assertEqual(hall.channels[0].name, "自由频道")
        self.assertEqual(hall.channels[0].sections, [directory.Section("自由1区", 1, 1, 500, 100, 0x32E9)])


class GamePacketTest(unittest.TestCase):
    # Captured client room-list plaintext (internal/protocol/game/room_test.go).
    CAPTURED_ROOM_LIST = bytes.fromhex("00660000000000120002ffff0001000f42410d44eaa802000100080000")

    def test_room_list_request_matches_capture(self) -> None:
        payload = lobby.build_room_list_request(1_000_001, 1, 8, 0, now=0x0D44EAA8)
        packet = game.Packet(0x0479, 0x0433, 1_000_001, lobby.ROOM_LIST_COMMAND, 0, 0x12, game.ROUTE_PLAYER, game.MARKER, 1, payload)
        encoded = game.encode(packet)
        self.assertEqual(qqtea.decrypt(encoded[game.ENCRYPTED_OFFSET:], game.LOCAL_KEY), self.CAPTURED_ROOM_LIST)
        self.assertEqual(encoded[:18], bytes.fromhex(f"{len(encoded):08x}") + bytes.fromhex("000004790433ffff000f42410120"))
        self.assertEqual(game.decode(encoded), packet)

    def test_login_request_layout(self) -> None:
        payload = player.build_login_request(1_000_001, "糖一", 1, 1, 7, 0x004F5D31)
        self.assertEqual(len(payload), 53)  # loginRequestBasePayloadSize with no config files
        self.assertEqual(struct.unpack_from(">I", payload, 0)[0], 1_000_001)
        self.assertEqual(payload[8:28].rstrip(b"\0").decode("gbk"), "糖一")
        self.assertEqual((payload[28], struct.unpack_from(">H", payload, 32)[0], payload[38]), (1, 1, 7))
        self.assertEqual(struct.unpack_from(">IH", payload, 39), (0x004F5D31, 0))

    def test_login_response_round_trip(self) -> None:
        game_info = struct.pack(">6IHBI4I", 5, 4, 3, 0, 1000, 99, 12, 7, 0, 1, 2, 3, 4)
        item = struct.pack(">HIBBBBII", 99, 1, 0, 0, 0, 0, 0, 0xFFFFFFFF)
        payload = struct.pack(">HHIIHB", 0, 1, 1_000_001, 0x2000, 1, 0) + bytes(32) + struct.pack(">HH", 100, 1) + game_info
        payload += struct.pack(">H", 1) + item + struct.pack(">BIIB", 0, 0, 0, 0) + struct.pack(">IHHHHB", 0, 0, 0, 0, 0, 0)
        response = player.parse_login_response(payload)
        self.assertEqual((response.player_id, response.identity, response.room_count, response.minimum_room_id), (1, 0x2000, 100, 1))
        self.assertEqual((response.game_info.degree, response.game_info.role_id), (12, 7))
        self.assertEqual(response.items[0].item_id, 99)


class LobbyTest(unittest.TestCase):
    def test_room_list(self) -> None:
        name = "房间".encode("gbk") + b"\0"
        room = bytes([len(name)]) + name + struct.pack(">HBHBBI", 3, lobby.ROOM_FLAG_PREPARING | lobby.ROOM_FLAG_PASSWORD, 21, 0x24, 0, 0)
        payload = struct.pack(">HBH", 0, 2, 1) + room + struct.pack(">BH", 0, 0)
        self.assertEqual(lobby.parse_room_list(payload), [lobby.Room("房间", 3, 3, 21, 2, 4, 0, 0)])

    def test_player_list(self) -> None:
        game_info = struct.pack(">6IHBI4I", *([0] * 6), 1, 0, 0, 0, 0, 0, 0)
        player_info = struct.pack(">I", 1_000_002) + "糖二".encode("gbk").ljust(20, b"\0") + struct.pack(">HBBI", 2, 0, 0, 0)
        player_info += game_info + bytes([0]) + struct.pack(">IH", 0, 0) + b"\xff" * 8
        payload = bytes([0, 0, 1]) + player_info + struct.pack(">II", 7, 0) + bytes([0])
        players = lobby.parse_player_list(payload)
        self.assertEqual([(p.uin, p.nickname, p.player_id, p.honor) for p in players], [(1_000_002, "糖二", 2, 7)])


def room_vectors() -> dict[str, bytes]:
    lines = (pathlib.Path(__file__).parent / "data" / "room_vectors.txt").read_text().splitlines()
    return {name: bytes.fromhex(value) for name, value in (line.split() for line in lines if not line.startswith("#"))}


class RoomTest(unittest.TestCase):
    def test_create_room_request_layout(self) -> None:
        payload = room.build_create_room_request(1_000_001, "一起来玩吧", room.PROPERTY_FREE_RULE, "abc", 1, 9, now=5)
        self.assertEqual(len(payload), 50)  # createRoomRequestSize
        self.assertEqual(struct.unpack_from(">II", payload, 0), (1_000_001, 5))
        self.assertEqual(payload[8:28].rstrip(b"\0").decode("gbk"), "一起来玩吧")
        self.assertEqual(payload[28], room.PROPERTY_FREE_RULE | room.PROPERTY_PASSWORD)  # password flag follows the slot
        self.assertEqual(payload[29:45].rstrip(b"\0"), b"abc")
        self.assertEqual(struct.unpack_from(">BI", payload, 45), (1, 9))
        self.assertEqual(room.build_create_room_request(1, "", room.PROPERTY_PASSWORD, "", 0)[28], 0)

    def test_enter_and_join_request_layout(self) -> None:
        payload = room.build_enter_room_request(1_000_001, 4, 7, "pw", now=5)
        self.assertEqual(len(payload), 27)  # enterRoomRequestSize
        self.assertEqual(struct.unpack_from(">IIHB", payload, 0), (1_000_001, 5, 4, 7))
        self.assertEqual(payload[11:].rstrip(b"\0"), b"pw")
        self.assertEqual(room.build_join_room_request(1_000_001, 3, now=5), struct.pack(">IIB", 1_000_001, 5, 3))

    def test_room_name_slot_keeps_whole_characters(self) -> None:
        payload = room.build_create_room_request(1, "糖" * 15, 0, "", 0)
        self.assertEqual(payload[8:28], "糖".encode("gbk") * 9 + b"\0\0")

    def test_enter_room_response_from_server_encoder(self) -> None:
        snapshot = room.parse_enter_room_response(room_vectors()["enter"])
        self.assertEqual((snapshot.room_id, snapshot.local_team, snapshot.local_seat, snapshot.map_id), (4, 2, 3, 21))
        self.assertEqual((snapshot.room_flag, snapshot.owner_id, snapshot.game_type, snapshot.wedding_mode_id), (2, 1, 1, 6))
        self.assertEqual(snapshot.seat_status, [2, 1, 2, 0, 1, 1, 1, 1])
        owner, guest = snapshot.players
        self.assertEqual((owner.uin, owner.nickname, owner.team, owner.seat, owner.game_info.role_id), (1_000_001, "糖一", 1, 1, 7))
        self.assertEqual((owner.items[0].item_id, len(owner.pet), owner.honor), (99, 40, 11))
        self.assertEqual((guest.nickname, guest.team, guest.seat, guest.status, guest.kin_name), ("糖二", 2, 3, 1, "家族"))
        self.assertEqual((guest.honor, guest.spouse_uin, guest.kin_flag), (22, 1_000_001, struct.pack(">II", 5, 12)))

    def test_join_room_response_from_server_encoder(self) -> None:
        snapshot = room.parse_enter_room_response(room_vectors()["join"], join=True)
        self.assertEqual((snapshot.room_id, snapshot.room_name, len(snapshot.players)), (4, "一起来玩吧", 2))
        self.assertEqual(snapshot.players[1].spouse_uin, 1_000_001)

    def test_enter_room_failure(self) -> None:
        snapshot = room.parse_enter_room_response(struct.pack(">HH", 14, 4) + bytes(22))
        self.assertEqual((snapshot.result, snapshot.room_id, snapshot.players), (14, 4, []))
        joined = room.parse_enter_room_response(struct.pack(">HH", 13, 0) + bytes(40), join=True)
        self.assertEqual(joined.result, 13)

    def test_enter_room_notify_from_server_encoder(self) -> None:
        room_id, member = room.parse_enter_room_notify(room_vectors()["notify"])
        self.assertEqual((room_id, member.nickname, member.seat, member.honor, member.spouse_uin), (4, "糖二", 3, 22, 1_000_001))

    def test_in_room_requests(self) -> None:
        self.assertEqual(room.build_change_role_request(1, 5, now=2), struct.pack(">IIH", 1, 2, 5))
        self.assertEqual(room.build_change_team_request(1, 8, now=2), struct.pack(">IIH", 1, 2, 8))
        self.assertEqual(room.build_modify_room_info_request(1, 21, 1, 0, now=2), struct.pack(">IIHBI", 1, 2, 21, 1, 0))
        self.assertEqual(room.build_set_seat_status_request(1, 3, room.SET_SEAT_LOCKED, now=2), struct.pack(">IIBB", 1, 2, 3, 2))
        self.assertEqual(room.build_kick_request(1, 2, 1_000_002, now=2), struct.pack(">IIHI", 1, 2, 2, 1_000_002))
        self.assertEqual(len(room.build_modify_room_request(1, room.MODIFY_NAME, "房间", 2, "", 0, now=2)), 54)
        chat = room.build_room_chat_request(1, "你好", now=2)
        self.assertEqual(chat, struct.pack(">IIHH", 1, 2, 0, 4) + "你好".encode("gbk"))

    def test_in_room_notifications(self) -> None:
        self.assertEqual(room.parse_leave_notify(struct.pack(">HHHH", 2, 1, 0, 21)), (2, 1, 21))
        self.assertEqual(room.parse_member_notify(struct.pack(">IHB", 1_000_002, 2, 1)), (1_000_002, 2, 1))
        self.assertEqual(room.parse_modify_room_info_notify(struct.pack(">HBI", 21, 1, 0)), (21, 1, 0))
        modify = struct.pack(">I", 1) + "新房间".encode("gbk").ljust(20, b"\0") + bytes([3]) + b"pw".ljust(16, b"\0")
        self.assertEqual(room.parse_modify_room_notify(modify), ("新房间", 3, "pw"))
        message = "糖一说:你好".encode("gbk")
        self.assertEqual(room.parse_room_chat_notify(struct.pack(">HHH", 1, 0, len(message)) + message), (1, 0, "糖一说:你好"))
        self.assertEqual(room.parse_kick_notify(struct.pack(">HHIB", 0, 4, 1_000_002, 0)), (0, 4, 1_000_002))


class ChatTest(unittest.TestCase):
    def test_section_chat_request(self) -> None:
        payload = chat.build_section_chat(1000001, "hi", now=5)
        self.assertEqual(payload, bytes.fromhex("000f4241" "00000005" "ffff" "00000000" "0002") + b"hi")
        bugle = chat.build_section_chat(1000001, "hi", bugle=True, now=5)
        self.assertEqual(bugle[10:14], bytes.fromhex("00000001"))

    def test_section_message(self) -> None:
        text = "糖一说:你好".encode("gbk")
        payload = struct.pack(">HHH", 1, 0xFFFF, len(text)) + text + struct.pack(">IIII", 1000001, 0x4000, 0, 10) + bytes(8)
        message = chat.parse_section_message(payload)
        self.assertEqual((message.source_player_id, message.nickname, message.text, message.uin, message.bugle),
                         (1, "糖一", "你好", 1000001, True))

    def test_whisper(self) -> None:
        request = chat.build_whisper(1000001, 1000002, "嘘", now=5)
        self.assertEqual(request[8:20], bytes.fromhex("00000000" "0000" "000f4242" "0002"))
        text = "嘘".encode("gbk")
        notify = struct.pack(">IH", 1000001, len(text)) + text + "糖一".encode("gbk").ljust(32, b"\0")
        self.assertEqual(chat.parse_whisper(notify), chat.Whisper(1000001, "糖一", "嘘"))


class RoomTypeTest(unittest.TestCase):
    """internal/protocol/game/change_room_type.go."""

    def test_change_room_type(self) -> None:
        self.assertEqual(room.build_change_room_type_request(1000001, 1, now=5), bytes.fromhex("000f4241" "00000005" "01" "00000000"))
        self.assertEqual(room.parse_change_room_type_notify(bytes.fromhex("02" "00000007")), (2, 7))


class ShopTest(unittest.TestCase):
    """Layouts of internal/protocol/game/shop_list.go, shop_buy.go,
    item_status.go and item_update.go."""

    def test_requests(self) -> None:
        self.assertEqual(shop.build_shop_list(1000001, 7, 848, now=5),
                         bytes.fromhex("000f4241" "00000005" "00000007" "00000000" "00000350"))
        self.assertEqual(shop.build_buy(1000001, 13013, shop.PAY_TANG, 7, 848, now=5),
                         bytes.fromhex("000f4241" "00000005" "000032d5" "0002" "0003" "00000007" "00000350" "0000"))
        self.assertEqual(shop.build_item_status(1000001, [(13013, shop.STATUS_EQUIPPED, 2)], now=5),
                         bytes.fromhex("000f4241" "00000005" "0001" "000032d5" "01" "02"))

    def test_replies(self) -> None:
        self.assertEqual(shop.parse_shop_list(bytes.fromhex("0000" "0000" "00000350") + bytes(11)), (0, 848))
        message = "购买成功".encode("gbk").ljust(128, b"\0")
        reply = (struct.pack(">H", 0) + message + struct.pack(">HHIIH", 2, 3, 1000, 13013, 1)
                 + struct.pack(">I", 13013) + struct.pack(">I", 99998999))
        self.assertEqual(shop.parse_buy(reply), shop.BuyResult(0, "购买成功", 13013, [13013], 99998999))
        item = struct.pack(shop.ITEM_INFO_FORMAT, 13013, 1, 1, 2, 0, 0, 5, 0xFFFFFFFF)
        update = shop.parse_item_add(struct.pack(">IIIIH", 1000001, 5, 0, 13013, 1) + item)
        self.assertEqual((update.uin, update.commodity_id, update.items[0].item_id, update.items[0].status), (1000001, 13013, 13013, 1))


class FriendTest(unittest.TestCase):
    """Offsets of internal/protocol/game/friend.go and find_friend.go."""

    def test_requests(self) -> None:
        self.assertEqual(friend.build_friend_list(1000003, now=5), bytes.fromhex("000f4243" "00000005"))
        add = friend.build_add_friend(1000003, 1000004, "糖三", "你好", now=5)
        # DecodeLocalAddFriendRequest: UIN 0:4, time 4:8, target 8:12, length 12:14, word from 14.
        self.assertEqual(add[:14], bytes.fromhex("000f4243" "00000005" "000f4244" "0019"))
        self.assertEqual(add[14:], "糖三".encode("gbk").ljust(21, b"\0") + "你好".encode("gbk"))
        self.assertEqual(friend.build_remove_friend(1000003, 1000004, now=5), bytes.fromhex("000f4243" "00000005" "000f4244"))
        # DecodeLocalAnswerFriendRequest: result 0:2, requester 2:6, time 6:10, responder 10:14.
        self.assertEqual(friend.build_answer_friend(1, 1000003, 1000004, now=5),
                         bytes.fromhex("0001" "000f4243" "00000005" "000f4244"))
        self.assertEqual(friend.build_find_friend(1000003, 1000004, now=5), bytes.fromhex("000f4243" "00000005" "000f4244"))

    def test_friend_list(self) -> None:
        # marshalFriendListPayload: result, owner, maximum, count, then 200 UIN slots (810 bytes).
        payload = struct.pack(">HIHH", 0, 1000003, 200, 1) + struct.pack(">200I", 1000004, *([0] * 199))
        self.assertEqual(len(payload), 810)
        self.assertEqual(friend.parse_friend_list(payload), friend.FriendList(0, 1000003, 200, [1000004]))

    def test_friend_update(self) -> None:
        # FriendUpdate.MarshalNetworkBinary: 163 bytes.
        payload = struct.pack(">IIIB", 1000003, 1000004, 1621150000, 2) + struct.pack(">30I", 11, 12, *([0] * 28))
        payload += b"\x01" + "糖四".encode("gbk").ljust(20, b"\0") + struct.pack(">BII", 0, 8192, 7)
        update = friend.parse_friend_update(payload)
        self.assertEqual(update, friend.FriendUpdate(1000003, 1000004, 1621150000, [11, 12], True, "糖四", 0, 8192, 7))
        with self.assertRaises(ValueError):
            friend.parse_friend_update(payload[:-1])

    def test_application_and_results(self) -> None:
        # BuildLocalAddFriendRequestNotification relays the requester's word unchanged.
        word = "糖三".encode("gbk").ljust(21, b"\0") + "你好".encode("gbk")
        notify = struct.pack(">IIIH", 1000003, 5, 1000004, len(word)) + word
        self.assertEqual(friend.parse_application(notify), friend.FriendApplication(1000003, 1000004, "糖三", "你好"))
        self.assertEqual(friend.parse_application(struct.pack(">IIIH", 1000003, 5, 1000004, 4) + "糖三".encode("gbk")).text, "")
        self.assertEqual(friend.parse_remove_result(struct.pack(">HII", 0, 1000003, 1000004)), (0, 1000004))
        self.assertEqual(friend.parse_answer(struct.pack(">HIII", 1, 1000003, 5, 1000004)), friend.FriendAnswer(1, 1000003, 1000004))

    def test_find_friend(self) -> None:
        # FindFriendResponse.MarshalNetworkBinary.
        payload = struct.pack(">HI", 0, 1000004) + "糖四".encode("gbk").ljust(20, b"\0")
        payload += struct.pack(">HBBIIHHH", 4, 0, 0, 8192, 1, 1, 1, 7) + "一起来玩吧".encode("gbk").ljust(20, b"\0")
        payload += b"\x01" + struct.pack(">6IHBI4I", 5200, 520, 52, 0, 1621150000, 99, 179, 1, 0, 520, 52, 5, 42)
        payload += struct.pack(">H", 1) + struct.pack(">HIBBBBII", 99, 1, 1, 0, 0, 0, 5, 0xFFFFFFFF)
        kin = "糖家".encode("gbk")
        payload += struct.pack(">IH", 3, len(kin)) + kin + bytes.fromhex("00000003" "00000005") + struct.pack(">I", 9)
        payload += b"\x01" + struct.pack(">BIHI", 2, 10, 1, 100)
        found = friend.parse_find_friend(payload)
        self.assertEqual((found.uin, found.nickname, found.player_id, found.identity, found.section_id, found.room_id, found.room_name),
                         (1000004, "糖四", 4, 8192, 1, 7, "一起来玩吧"))
        self.assertEqual((found.status, found.game_info.points, found.game_info.extended_points, found.items[0].item_id),
                         (1, 1621150000, 42, 99))
        self.assertEqual((found.kin_index, found.kin_name, found.kin_flag[4:], found.honor), (3, "糖家", bytes.fromhex("00000005"), 9))
        with self.assertRaises(ValueError):
            friend.parse_find_friend(payload + b"\0")
        self.assertEqual(friend.parse_find_friend(struct.pack(">HI", 1, 1000004)).result, 1)


class MatchTest(unittest.TestCase):
    def test_game_event_notify(self) -> None:
        # internal/protocol/game/game_event_test.go: player 1 dies at (300, 191).
        payload = bytes.fromhex("0001000000010000000fa70f00000001" "0000f85b012c00bf00")
        event = match.parse_game_event_notify(payload)
        self.assertEqual((event.room_id, event.schema), (1, 0x0FA7))
        self.assertEqual(event.body, bytes.fromhex("00010000f85b012c00bf00"))

    def test_game_event_notify_too_short(self) -> None:
        with self.assertRaises(ValueError):
            match.parse_game_event_notify(bytes.fromhex("0001000000010000001fa70f0000"))

    def test_game_over(self) -> None:
        body = struct.pack(">IB", 52980, 2)
        body += struct.pack(">HBIIB", 1, match.RESULT_LOSS, 0, 10, 2) + struct.pack(">IIII", 1, 0, 5, 0)
        body += struct.pack(">HBIIB", 20004, match.RESULT_WIN, 0, 30, 0)
        body += bytes([0])
        over = match.parse_game_over(body)
        self.assertEqual(over.time_ms, 52980)
        self.assertEqual([(result.player_id, result.result, result.point) for result in over.results],
                         [(1, match.RESULT_LOSS, 10), (20004, match.RESULT_WIN, 30)])
        self.assertEqual((over.results[0].counts, over.results[0].scores), ([1, 0], [5, 0]))
        self.assertEqual(over.game_mode, 0)

    def test_hello(self) -> None:
        self.assertEqual(match.build_hello(1000001, now=0x1234), bytes.fromhex("000f4241000012340000"))


if __name__ == "__main__":
    unittest.main()
