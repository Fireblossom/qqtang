"""Shop service messages (internal/protocol/game/shop_list.go, shop_buy.go,
item_status.go, item_update.go).

The shop is a separate TCP endpoint with the game envelope (net.game); it
trusts the account's live game connection from the same host, so there is
no login. Each request here uses its own short connection: the server closes
the purchase socket after answering anyway. Item status changes (equip,
recycle) and the inventory updates that follow travel on the game connection.
"""

from __future__ import annotations

import dataclasses
import socket
import struct
import time

from . import game
from .player import ItemInfo

SHOP_LIST_COMMAND = 0x035E
BUY_COMMAND = 0x0259
PRESENT_COMMAND = 0x025A
RESTORE_COMMAND = 0x0263
MONEY_NOTIFY_COMMAND = 0x0094
ITEM_STATUS_COMMAND = 0x0085
ITEM_ADD_NOTIFY_COMMAND = 0x00D9

DEAL_PURCHASE = 2
# PayType: 1 Q币, 3 糖币 (the only one this server settles), 4 VNet,
# 6 酷比宝石, 7 Q点.
PAY_QB, PAY_TANG, PAY_VNET, PAY_KUBI, PAY_QPOINT = 1, 3, 4, 6, 7

# ITEM_INFO.ItemStatus.
STATUS_OWNED, STATUS_EQUIPPED, STATUS_RECYCLED, STATUS_LEARNED = 0, 1, 2, 4
ITEM_INFO_FORMAT = ">HIBBBBII"
ITEM_INFO_SIZE = struct.calcsize(ITEM_INFO_FORMAT)


@dataclasses.dataclass
class BuyResult:
    result: int  # 0 success
    message: str
    commodity_id: int
    item_ids: list[int]
    money_left: int  # 糖币 balance after the purchase


@dataclasses.dataclass
class ItemAdd:
    uin: int
    commodity_id: int
    items: list[ItemInfo]  # absolute values, not deltas


def _stamp(uin: int, now: int | None) -> bytes:
    return struct.pack(">II", uin, (int(time.time()) if now is None else now) & 0xFFFFFFFF)


def build_shop_list(uin: int, client_version: int, shop_version: int, now: int | None = None) -> bytes:
    """REQUEST_SHOPLIST: UIN, time, client version, CS version, shop list version."""
    return _stamp(uin, now) + struct.pack(">III", client_version, 0, shop_version)


def parse_shop_list(payload: bytes) -> tuple[int, int]:
    """(result, latest shop list version)."""
    result, _download, version = struct.unpack_from(">HHI", payload, 0)
    return result, version


def build_buy(uin: int, commodity_id: int, pay_type: int, client_version: int, commodity_version: int,
              mixed: bool = False, now: int | None = None) -> bytes:
    return _stamp(uin, now) + struct.pack(">IHHIIH", commodity_id, DEAL_PURCHASE, pay_type, client_version,
                                          commodity_version, int(mixed))


def build_present(uin: int, target: int, commodity: int, text: str, pay_type: int, version: int,
                  mixed: bool = False, now: int | None = None) -> bytes:
    encoded = str(text).encode("gbk", "replace")[:255].decode("gbk", "ignore").encode("gbk")
    return (_stamp(uin, now) + struct.pack(">IHHIB", commodity, DEAL_PURCHASE, pay_type, target, len(encoded))
            + encoded + struct.pack(">IH", version, int(mixed)))


def parse_present(payload: bytes) -> tuple[int, str, int]:
    if len(payload) != 138:
        raise ValueError("invalid present response length")
    result = struct.unpack_from(">H", payload)[0]
    text = payload[2:130].split(b"\0", 1)[0].decode("gbk", "replace")
    return result, text, struct.unpack_from(">I", payload, 134)[0]


def build_restore(uin: int, item: int, version: int, now: int | None = None) -> bytes:
    return _stamp(uin, now) + struct.pack(">II", item, version)


def parse_restore(payload: bytes) -> tuple[int, int, int, str]:
    if len(payload) != 136:
        raise ValueError("invalid restore response length")
    result, item, period = struct.unpack_from(">HIH", payload)
    return result, item, period, payload[8:136].split(b"\0", 1)[0].decode("gbk", "replace")


def parse_buy(payload: bytes) -> BuyResult:
    (result,) = struct.unpack_from(">H", payload, 0)
    message = payload[2:130].split(b"\0", 1)[0].decode("gbk", "replace")
    _deal, _pay, _paid, commodity_id, count = struct.unpack_from(">HHIIH", payload, 130)
    item_ids = list(struct.unpack_from(f">{count}I", payload, 144))
    (money_left,) = struct.unpack_from(">I", payload, 144 + 4 * count)
    return BuyResult(result, message, commodity_id, item_ids, money_left)


def parse_result(payload: bytes) -> int:
    """The u16 result of ITEM_STATUS_CHANGE's reply."""
    return struct.unpack_from(">H", payload, 0)[0] if len(payload) >= 2 else 0


def build_item_status(uin: int, changes: list[tuple[int, int, int]], now: int | None = None) -> bytes:
    """changes: (item ID, new status, new role ID)."""
    return _stamp(uin, now) + struct.pack(">H", len(changes)) + b"".join(
        struct.pack(">IBB", item_id, status, role_id) for item_id, status, role_id in changes)


def parse_item_add(payload: bytes) -> ItemAdd:
    uin, _time, _source, commodity_id, count = struct.unpack_from(">IIIIH", payload, 0)
    items = [ItemInfo(*struct.unpack_from(ITEM_INFO_FORMAT, payload, 18 + index * ITEM_INFO_SIZE)) for index in range(count)]
    return ItemAdd(uin, commodity_id, items)


def request(address: tuple[str, int], uin: int, section_id: int, command: int, payload: bytes,
            timeout: float = 10.0) -> game.Packet:
    """Send one shop request on a fresh connection and return the reply."""
    packet = game.Packet(1, 1, uin, command, 0, 1, game.ROUTE_PLAYER, game.MARKER, section_id, payload)
    with socket.create_connection(address, timeout=timeout) as sock:
        sock.settimeout(timeout)
        sock.sendall(game.encode(packet))
        while True:
            data = game.read_packet(sock)
            if data is None:
                raise ConnectionError("shop server closed the connection")
            reply = game.decode(data)
            if reply.command == command:
                return reply
