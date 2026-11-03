"""Shop natives (uiShop): entering and leaving the shop, the commodity
catalog, buying with 糖币, the backpack and recycler, equipping, and the
character preview.

The catalog is the client's own config/Commodity.ini (the server checks it
is identical to its catalog and never sends one): COMMODITYTYPE packs the
shop tab and sub-tab as major << 8 | minor, and [comment.N] lists the
recommended tab's sub-tabs. Item names and pictures come from the scripts'
commodityList/itemList. 糖币 purchases and gifts are settled by the server;
repairs, the fruit machine and tasks are not supported by it and
are stubbed here.
"""

from __future__ import annotations

import configparser
import dataclasses
import struct
import threading
import types
from typing import Any, Callable

import pygame

from .avatar import AvatarCache
from .net import pet, shop
from .resources import Resources
from .runtime import UIRuntime
from .script import call_script, log
from .session import Session

RECOMMENDED_TAB = 1
FUNCTION_TAB = 2
# Playable roles in the order the preview's arrow buttons cycle them.
ROLES = (1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 13, 14, 15, 16, 18, 19, 20, 21, 22)
# The preview character stands in the 96x96 preview box (preview.bg). The
# callback's own widget (preview.demo) sits up-left of the box; how the
# native callback offsets from it is not traced.
PREVIEW_BOX = "UI.shop.preview.bg"
PREVIEW_FRAME_MS = 100
PREVIEW_TEAMS = 8


@dataclasses.dataclass
class Commodity:
    id: int
    name: str
    major: int
    minor: int
    price_qq: int  # 角 (0.1 Q币); negative when not sold for this currency
    price_qpoint: int
    price_tang: int
    member_rebate: int  # percent; 100 or more means no discount
    rebate: int
    attribute: int
    item_id: int
    item_count: int
    period: int  # days; 0 permanent


class Catalog:
    def __init__(self, data: bytes | None):
        self.version = 0
        self.commodities: dict[int, Commodity] = {}
        self.categories: dict[tuple[int, int], list[int]] = {}
        if not data:
            return
        parser = configparser.ConfigParser(strict=False, interpolation=None)
        parser.optionxform = str.upper
        parser.read_string(data.decode("gbk", "replace").replace("\r", "\n"))
        public = next((parser[name] for name in parser.sections() if name.lower() == "public"), {})
        self.version = int(public.get("VERSION", 0) or 0)
        for section in parser.sections():
            if not section.upper().startswith("COMMODITY."):
                continue
            row = parser[section]
            number = lambda key, default=0: int(row.get(key, default) or default)  # noqa: E731
            kind = number("COMMODITYTYPE")
            commodity = Commodity(
                number("COMMODITYID"), row.get("COMMODITYNAME", ""), kind >> 8, kind & 0xFF,
                number("QPRICE", -1), number("QQPOINTPRICE", -1), number("QQTANGPRICE", -1),
                number("MEMBERREBATE", 100), number("REBATE", 100), number("ATTRIBUTE"),
                number("ITEM.1.ID"), number("ITEM.1.NUM", 1), number("ITEM.1.AVAILPERIOD"),
            )
            self.commodities[commodity.id] = commodity
            self.categories.setdefault((commodity.major, commodity.minor), []).append(commodity.id)
        for section in parser.sections():
            if section.lower().startswith("comment."):
                minor = int(section.split(".", 1)[1])
                ids = (int(value) for value in parser[section].get("ITEM.1", "").split(",") if value.strip().isdigit())
                self.categories[(RECOMMENDED_TAB, minor)] = [value for value in ids if value in self.commodities]

    def at(self, major: int, minor: int, index: int) -> Commodity | None:
        ids = self.categories.get((major, minor), [])
        return self.commodities.get(ids[index]) if 0 <= index < len(ids) else None


@dataclasses.dataclass
class Preview:
    role: int = 1
    team: int = 0
    wear: dict[str, int] = dataclasses.field(default_factory=dict)
    action: str = "stand"
    direction: int = 3
    started: int = 0


def item_view(item: Any) -> types.SimpleNamespace:
    """The ITEM_INFO object scripts read."""
    return types.SimpleNamespace(
        m_nItemID=item.item_id, m_iNumOfItem=item.quantity, m_bItemStatus=item.status, m_bItemRoleID=item.role_id,
        m_bItemEffect=item.effect, m_bItemColor=item.color, m_dwBuyTime=item.buy_time, m_dwAvailPeriod=item.available_period)


def build_shop_natives(runtime: UIRuntime, session: Session, resources: Resources,
                       avatars: AvatarCache) -> tuple[dict[str, Callable[..., Any]], Callable[[pygame.Surface], None]]:
    """Return the natives and the SC_shopDemo_draw preview callback."""
    catalog: list[Catalog] = []  # loaded on first use
    item_tabs: dict[str, int] = {}
    preview = Preview()

    def script(name: str, *args: Any) -> Any:
        function = runtime.namespace.get(name)
        return call_script(function, *args) if callable(function) else None

    def notify(message: str) -> None:
        runtime.show_msgbox(message, "", 3, "UI.SysMsgbox", 0)  # handle 0: no ui_OnMsgBoxResult action
        script("ui_msgBox", 3)

    def get_catalog() -> Catalog:
        if not catalog:
            catalog.append(Catalog(resources.read("config/Commodity.ini")))
            log(f"[shop] catalog version {catalog[0].version}, {len(catalog[0].commodities)} commodities")
        return catalog[0]

    def tab_of(item_id: int) -> int:
        """Backpack tab (2 function .. 6 purple diamond) of an item, from
        itemCFG.py's itemType table; the shop script overwrites that name."""
        if not item_tabs:
            source = runtime.loader.scripts.get("object/itemcfg")
            values: dict[str, Any] = {}
            if source is not None:
                exec(compile(source.read_text(encoding="utf-8"), "object/itemCFG.py", "exec"), values)
            item_tabs.update({name.strip(): tab for name, (tab, _sub) in values.get("itemType", {}).items()})
        entry = runtime.namespace.get("itemList", {}).get(item_id)
        return item_tabs.get(entry[0], FUNCTION_TAB) if entry else FUNCTION_TAB

    def inventory() -> list:
        return session.profile.items if session.profile else []

    def money() -> int:
        return session.profile.game_info.money if session.profile else 0

    def enabled_items(tab: int) -> list:
        known = runtime.namespace.get("itemList", {})
        return [item for item in inventory() if item.quantity > 0 and item.status != shop.STATUS_RECYCLED
                and item.item_id in known and tab_of(item.item_id) == tab]

    def recycled_items() -> list:
        return [item for item in inventory() if item.quantity > 0 and item.status == shop.STATUS_RECYCLED]

    def find_item(item_id: int) -> Any:
        return next((item for item in inventory() if item.item_id == int(item_id)), None)

    def shop_server() -> Any:
        """The directory lists the shop (and the deal server after it) as
        servers that no section uses."""
        hall = session.hall
        if hall is None:
            return None
        used = {section.server_id for channel in hall.channels for section in channel.sections}
        return next((server for server in hall.servers if server.id not in used), None)

    def shop_address() -> tuple[str, int] | None:
        server = shop_server()
        return (server.ip, server.port) if server else None

    def section_id() -> int:
        return session.section.id if session.section else 0

    def in_shop() -> bool:
        return runtime.current_screen == "shop"

    # Entering and leaving ---------------------------------------------------

    def GetRandShopServerID():
        server = shop_server()
        return server.id if server else 0

    def SetLocalShopVertion(_uin, version):
        session.save_state(shop_version=int(version))

    def InitializeShop():
        get_catalog()

    def EnterShop(uin, _server_id):
        address = shop_address()
        if address is None or session.profile is None:
            notify("商城暂时无法进入")
            return
        version = session.hall.version if session.hall else 0
        payload = shop.build_shop_list(int(uin), version, get_catalog().version)

        def work() -> None:
            try:
                reply = shop.request(address, int(uin), section_id(), shop.SHOP_LIST_COMMAND, payload)
                result, latest = shop.parse_shop_list(reply.payload)
            except (OSError, ValueError, ConnectionError) as error:
                log(f"[shop] cannot reach the shop at {address}: {error}")
                runtime.post(lambda: notify("无法连接商城服务器"))
                return
            runtime.post(lambda: entered(result, latest))

        threading.Thread(target=work, name="shop-enter", daemon=True).start()

    def entered(result: int, latest: int) -> None:
        if result != 0:
            notify("商城暂时无法进入")
            return
        if latest != get_catalog().version:
            log(f"[shop] server catalog version {latest} differs from config/Commodity.ini {get_catalog().version}")
        preview.role = role_from_profile()
        preview.wear.clear()
        runtime.goto_screen("shop")
        script("ui_NotifyMoney", 0, 0, money())
        # Only 糖币 purchases are settled by this server; show those prices.
        script("doUI", "UI.shop.useTangBRad", "OnClick")
        runtime.namespace["Win_SelectSelf"]("UI.shop.useTangBRad")

    def role_from_profile() -> int:
        role = session.profile.game_info.role_id if session.profile else 0
        role = role or int(session.state.get("last_role", 1))
        return role if role in ROLES else ROLES[0]

    def LeaveShop():
        runtime.goto_screen("selRoom")

    def UnInitializeShop():
        pass

    # Commodities --------------------------------------------------------------

    def fetchCommodityCnt(major, minor):
        return len(get_catalog().categories.get((int(major), int(minor)), []))

    def fetchCommodity(major, minor, index):
        commodity = get_catalog().at(int(major), int(minor), int(index))
        if commodity is None:
            return types.SimpleNamespace(m_szCommodityName="", m_iCommodityID=0, m_iPriceQQ=-1, m_iPriceQQGame=-1,
                                         m_iPriceQQTang=-1, m_dwAttribute=0, m_bMemberRebate=100, m_bRebate=100)
        return types.SimpleNamespace(
            m_szCommodityName=commodity.name, m_iCommodityID=commodity.id, m_iPriceQQ=commodity.price_qq,
            m_iPriceQQGame=commodity.price_qpoint, m_iPriceQQTang=commodity.price_tang, m_dwAttribute=commodity.attribute,
            m_bMemberRebate=commodity.member_rebate, m_bRebate=commodity.rebate)

    def fetchCommItem(major, minor, index, _item=0):
        commodity = get_catalog().at(int(major), int(minor), int(index))
        return types.SimpleNamespace(m_nAvailPeriod=commodity.period if commodity else 0,
                                     m_iItemNum=commodity.item_count if commodity else 0,
                                     m_nItemID=commodity.item_id if commodity else 0)

    def BuyCommodity(commodity_id, _deal_type, pay_type, mixed=0):
        address = shop_address()
        if address is None or session.profile is None:
            script("buyFail", "无法连接商城服务器")
            return
        uin = session.uin
        version = session.hall.version if session.hall else 0
        payload = shop.build_buy(uin, int(commodity_id), int(pay_type), version, get_catalog().version, bool(mixed))

        def work() -> None:
            try:
                reply = shop.request(address, uin, section_id(), shop.BUY_COMMAND, payload)
                result = shop.parse_buy(reply.payload)
            except (OSError, ValueError, ConnectionError, IndexError) as error:
                log(f"[shop] purchase failed: {error}")
                runtime.post(lambda: script("buyFail", "购买失败，请稍后重试"))
                return
            runtime.post(lambda: bought(result))

        threading.Thread(target=work, name="shop-buy", daemon=True).start()

    def bought(result: shop.BuyResult) -> None:
        log(f"[shop] buy {result.commodity_id}: {result.result} {result.message} items {result.item_ids}")
        if result.result != 0:
            if in_shop():
                script("buyFail", result.message or "购买失败")
            return
        if session.profile is not None:
            session.profile.game_info.money = result.money_left
        if in_shop():
            script("ui_NotifyMoney", 0, 0, money())
            notify(result.message or "购买成功")
        # The new item itself arrives as NOTIFY_PLAYER_ITEMADD (on_item_add).

    def PresentCommodity(target, commodity, _length, text, _deal, pay_type, mixed=0):
        address = shop_address()
        if address is None or session.profile is None or int(target) <= 0:
            notify("请输入有效的收件号码")
            return
        version = session.hall.version if session.hall else 0
        payload = shop.build_present(session.uin, int(target), int(commodity), str(text), int(pay_type), version, bool(mixed))

        def work():
            try:
                reply = shop.request(address, session.uin, section_id(), shop.PRESENT_COMMAND, payload)
                result, reason, paid = shop.parse_present(reply.payload)
            except (OSError, ValueError, ConnectionError) as error:
                log(f"[shop] gift failed: {error}")
                runtime.post(lambda: notify("无法完成赠送，请检查连接"))
                return
            log(f"[shop] present {commodity} to {target}: {result}, paid {paid}: {reason}")
            runtime.post(lambda: notify(reason or ("赠送成功" if result == 0 else "赠送失败")))

        threading.Thread(target=work, name="shop-present", daemon=True).start()

    def SC_ClickBuyBtn():
        pass

    def SC_ClickShopBtn():
        pass

    def GetMyTicket():
        return types.SimpleNamespace(m_iNumOfItem=-1)  # no shop tickets; -1 leaves the counter blank

    def IsFruitMechine():
        return 0

    def HandleFruitMachine(operation, _args=None):
        return [0] if int(operation) == 2 else []

    # Backpack and recycler ----------------------------------------------------

    def UpdateMyItem():
        pass  # the inventory is kept current from NOTIFY_PLAYER_ITEMADD

    def GetEnableItemCnt(tab):
        return len(enabled_items(int(tab)))

    def GetEnableItemInfo(tab, index):
        items = enabled_items(int(tab))
        return item_view(items[int(index)]) if 0 <= int(index) < len(items) else item_view(empty_item())

    def GetDisableItemCnt():
        return len(recycled_items())

    def GetDisableItemInfo(index):
        items = recycled_items()
        return item_view(items[int(index)]) if 0 <= int(index) < len(items) else item_view(empty_item())

    def empty_item() -> Any:
        return types.SimpleNamespace(item_id=0, quantity=0, status=0, role_id=0, effect=0, color=0, buy_time=0, available_period=0)

    def GetItemLeaveTime(item_id):
        """Days left; the client reads the period as signed, so permanent
        items (0xFFFFFFFF) are -1 and show a count instead."""
        item = find_item(item_id)
        if item is None:
            return 0
        period = item.available_period
        return period - (1 << 32) if period >= 1 << 31 else period // 86400

    def GetItemLeaveCount(item_id):
        item = find_item(item_id)
        return item.quantity if item else 0

    def GetItemInfoFromSec(item_id):
        item = find_item(item_id)
        return item_view(item if item else empty_item())

    def ChangeItemStatus(uin, item_id, status):
        item = find_item(item_id)
        session.send(shop.ITEM_STATUS_COMMAND, shop.build_item_status(int(uin), [(int(item_id), int(status), item.role_id if item else 0)]))

    def ActiveItemStatus(uin, role, _count, item_ids):
        """Save the preview's outfit as the role's equipment: wear the listed
        items and take off whatever else the role wore."""
        role, wanted = int(role), {int(item_id) for item_id in item_ids or ()}
        changes = [(item.item_id, shop.STATUS_OWNED, role) for item in inventory()
                   if item.status == shop.STATUS_EQUIPPED and item.role_id == role and item.item_id not in wanted]
        changes += [(item_id, shop.STATUS_EQUIPPED, role) for item_id in sorted(wanted)]
        if changes:
            session.send(shop.ITEM_STATUS_COMMAND, shop.build_item_status(int(uin), changes))

    def GetRoleItem(role, kind):
        known = runtime.namespace.get("itemList", {})
        for item in inventory():
            if (item.status == shop.STATUS_EQUIPPED and item.role_id == int(role) and item.quantity > 0
                    and item.item_id in known and known[item.item_id][0] == kind):
                return item.item_id
        return -1

    def GetAllowWear(_role, _item_id):
        return 0

    def NotifyNoWear():
        notify("当前角色不能穿戴这件服装")

    def GetAffectionItem(_role, _item_id):
        return 0

    # Not supported by this server ---------------------------------------------

    def GetRepairPrice(_item_id):
        return 0

    def RepairCommodity(*_args):
        notify("暂不支持修理")

    def IsBook(_item_id):
        return 0

    def InitTaskSys():
        pass

    def UnInitTaskSys():
        pass

    def RequestTasksInfo():
        pass

    def LoadShopKeyboardLayout():
        pass

    # Character preview ----------------------------------------------------------

    def shopPlayer_getRoleIdx():
        return preview.role

    def shopPlayer_changeRole(delta, _flag=1):
        index = ROLES.index(preview.role) if preview.role in ROLES else 0
        preview.role = ROLES[(index + int(delta)) % len(ROLES)]
        preview.wear.clear()

    def shopPlayer_wear(kind, part, _effect=0, _color=0):
        if int(part) > 0:
            preview.wear[str(kind)] = int(part)
        else:
            preview.wear.pop(str(kind), None)

    def act(action: str, direction: int | None = None) -> None:
        preview.action = action
        if direction is not None:
            preview.direction = direction
        preview.started = pygame.time.get_ticks()

    def shopPlayer_stand():
        act("stand")

    def shopPlayer_stop():
        act("stand")

    def shopPlayer_walk(direction):
        act("walk", int(direction))

    def shopPlayer_die():
        act("die")

    def shopPlayer_freeze():
        act("trigger")

    def shopPlayer_jump():
        act("stand")

    def shopPlayer_win():
        act("stand")

    def shopPlayer_putBomb():
        act("stand")

    def sc_shopPlayer_act(_action):
        act("stand")

    def shopPlayer_changeColor():
        preview.team = (preview.team + 1) % PREVIEW_TEAMS

    def draw_preview(screen: pygame.Surface) -> None:
        widget = runtime.find(PREVIEW_BOX)
        if widget is None:
            return
        frame = (pygame.time.get_ticks() - preview.started) // PREVIEW_FRAME_MS
        if preview.action == "die":
            frame = min(frame, 3)
        image = avatars.get(preview.role, preview.team, preview.action, preview.direction, frame, tuple(sorted(preview.wear.items())))
        if image is not None:
            x, y = widget.absolute()
            # The 100x100 avatar canvas, centred, feet near the box's bottom.
            screen.blit(image, (x + widget.w // 2 - 50, y + widget.h - 100))

    # Server messages --------------------------------------------------------------

    def on_item_add(packet) -> None:
        update = shop.parse_item_add(packet.payload)
        if session.profile is None or update.uin != session.uin:
            return
        items = session.profile.items
        for new in update.items:
            index = next((i for i, item in enumerate(items) if item.item_id == new.item_id), None)
            if index is None:
                items.append(new)
            else:
                items[index] = new
        log(f"[shop] inventory update: " + ", ".join(f"{item.item_id}x{item.quantity}/{item.status}" for item in update.items))
        if in_shop():
            script("ui_updateMyItems")
            if runtime.namespace["Win_IsVisible"]("UI.shop.recycler"):
                script("ui_updateRecycler")
        # QQTSection 0x1002A543: any pet item in the update refreshes the pet panel's items.
        if any(item.item_id in pet.PET_ITEMS for item in update.items):
            script("UpdatePetItemUi")

    def on_item_status(packet) -> None:
        result = shop.parse_result(packet.payload)
        if result != 0:
            log(f"[shop] item status change rejected: {result}")
        elif in_shop():
            # Client.exe ChangeItemStatusResponse refreshes both lists.
            script("ui_updateMyItems")
            script("ui_updateRecycler")

    def on_money(packet) -> None:
        if len(packet.payload) != 11:
            return
        result, _kind, balance, uin = struct.unpack(">HBII", packet.payload)
        if result == 0 and uin == session.uin and session.profile is not None:
            session.profile.game_info.money = balance
            log(f"[shop] absolute balance {balance}")
            if in_shop():
                script("ui_NotifyMoney", 0, 0, balance)

    session.handlers.update({
        shop.ITEM_ADD_NOTIFY_COMMAND: on_item_add,
        shop.ITEM_STATUS_COMMAND: on_item_status,
        shop.MONEY_NOTIFY_COMMAND: on_money,
    })

    excluded = ("script", "notify", "act")
    natives = {name: value for name, value in locals().items()
               if callable(value) and (name[0].isupper() or name.startswith(("fetch", "shopPlayer_", "sc_shopPlayer")))
               and name not in excluded}
    return natives, draw_preview
