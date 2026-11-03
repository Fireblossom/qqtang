import unittest

from qqtclient import roomwalk
from qqtclient.net import roompeer


class RoomPeerTest(unittest.TestCase):
    def test_presence_and_relay_vectors(self) -> None:
        # docs/chatroom-move-re.md §4.1 and §4.3, checksums verified there.
        presence = roompeer.build_presence(1, 2, 1000002, ("192.168.1.10", 50000))
        self.assertEqual(presence.hex(), "001800000000010002000f424200019c2506c0a8010ac350")
        relay = bytes.fromhex("0035" "00" "00000005" "0002" "000f4242" "0002" "4047" "06" "0001000f4241"
                              "15179aa50bb800110000002701ec0000001b01000000000000b81e853e")
        kind, number, player, uin, data = roompeer.parse_datagram(relay)
        self.assertEqual((kind, number, player, uin), (2, 5, 2, 1000002))
        data_id, (seat, x, y, direction, speed) = roompeer.parse_room_msg_data(data)
        self.assertEqual((data_id, seat, x, y, direction, round(speed, 2)), (roompeer.MOVE_ID, 1, 236, 283, 0, 0.26))

    def test_message_data_and_grid(self) -> None:
        message = roompeer.room_msg_data(roompeer.PUT_BOMB_ID, roompeer.bomb_body(1, 308, 315), 0x2A, 0x15179E4E)
        self.assertEqual(message.hex(), "15179e4e0bb900090000002a01340100003b010000")
        self.assertEqual(roomwalk.cell_of(308, 315), (5, 7))


if __name__ == "__main__":
    unittest.main()
