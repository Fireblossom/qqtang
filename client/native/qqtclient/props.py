"""The room's prop bar (uiRoom 装备道具栏) and the prop storage that fills it,
after QQTSection.dll's game-data object.

The bar holds up to seven props numbered 20001..22999. Equipping or taking
one off sends ITEM_STATUS_CHANGE with status 1 or 0 and role 0; which slot a
prop sits in is only kept in the client directory's profile/<uin>.ini.
"""

from __future__ import annotations

import pathlib
import types
from typing import Any, Callable

from .net import shop
from .runtime import UIRuntime
from .script import log
from .session import Session

SLOTS = 7
STORAGE_LIMIT = 500


def is_bar_prop(item_id: int) -> bool:
    return 20000 < item_id < 23000


def in_storage(item_id: int, status: int) -> bool:
    """QQTSection.dll 0x1002B683: room props, 30001..31999, and 24001..24499
    unless put away (status 4)."""
    return is_bar_prop(item_id) or 30000 < item_id < 32000 or (24000 < item_id < 24500 and status != 4)


def settle_storage(rows: list[Any]) -> list[Any]:
    """0x1002BD13: partition props, then materials, swapping from the tail.

    This is intentionally not a stable sort: the original 500-entry array
    pulls the last matching item forward at each gap. Books follow both.
    """
    slots = list(rows[:STORAGE_LIMIT])
    slots.extend([None] * (STORAGE_LIMIT - len(slots)))
    head = 0
    for matches in (is_bar_prop, lambda item: 30000 < item < 32000):
        while head < STORAGE_LIMIT:
            if slots[head] is not None and matches(slots[head].m_stItem.m_nItemID):
                head += 1
                continue
            tail = next((i for i in range(STORAGE_LIMIT - 1, head, -1)
                         if slots[i] is not None and matches(slots[i].m_stItem.m_nItemID)), None)
            if tail is None:
                break
            slots[head], slots[tail] = slots[tail], slots[head]
            head += 1
    return [row for row in slots if row is not None]


def item_view(item: Any) -> types.SimpleNamespace:
    return types.SimpleNamespace(
        m_nItemID=item.item_id, m_iNumOfItem=item.quantity, m_bItemStatus=item.status, m_bItemRoleID=item.role_id,
        m_bItemEffect=item.effect, m_bItemColor=item.color, m_dwBuyTime=item.buy_time, m_dwAvailPeriod=item.available_period)


EMPTY_ITEM = types.SimpleNamespace(item_id=0, quantity=0, status=0, role_id=0, effect=0, color=0, buy_time=0, available_period=0)


def place_saved(items: list[Any], saved: list[int] | None) -> list[int]:
    """The bar after login (QQTSection.dll 0x1002B8A3): equipped props
    (status 1) go back to their saved slot; others take the first slot that
    is empty and not saved for another equipped prop. Without a saved file
    they fill the bar in inventory order."""
    equipped = {item.item_id for item in items if is_bar_prop(item.item_id) and item.status == shop.STATUS_EQUIPPED}
    slots = [0] * SLOTS
    placed = 0
    saved = list(saved) if saved is not None else None
    for item in items:
        if placed >= SLOTS:
            break
        item_id = item.item_id
        if item_id not in equipped:
            continue
        if saved is None:
            slots[placed] = item_id
            placed += 1
        elif item_id in saved:
            index = saved.index(item_id)
            slots[index] = item_id
            placed += 1
            saved[index + 1:] = [0 if value == item_id else value for value in saved[index + 1:]]
        else:
            for index in range(SLOTS):
                if is_bar_prop(slots[index]) or saved[index] in equipped:
                    continue
                slots[index] = item_id
                placed += 1
                break
    return slots


def build_props_natives(runtime: UIRuntime, session: Session) -> dict[str, Callable[..., Any]]:
    slots = [0] * SLOTS
    storage_sorted = False

    def inventory() -> list[Any]:
        return session.profile.items if session.profile else []

    def find_item(item_id: int) -> Any:
        return next((item for item in inventory() if item.item_id == item_id), None)

    def profile_path() -> pathlib.Path:
        return pathlib.Path("profile") / f"{session.uin}.ini"

    def save() -> None:
        try:
            profile_path().parent.mkdir(exist_ok=True)
            profile_path().write_text("".join(f"{value}  " for value in slots))
        except OSError as error:
            log(f"[props] cannot save the prop bar: {error}")

    def load() -> None:
        try:
            values = [int(word) for word in profile_path().read_text().split()[:SLOTS]]
            saved = values + [0] * (SLOTS - len(values))
        except (OSError, ValueError):
            saved = None
        slots[:] = place_saved(inventory(), saved)
        save()

    def set_status(item_id: int, equipped: bool) -> None:
        """QQTSection.dll 0x1002B508."""
        if not is_bar_prop(item_id):
            return
        status = shop.STATUS_EQUIPPED if equipped else shop.STATUS_OWNED
        session.send(shop.ITEM_STATUS_COMMAND, shop.build_item_status(session.uin, [(item_id, status, 0)]))
        item = find_item(item_id)
        if item is not None:
            item.status = status

    def prop_entry(item: Any) -> types.SimpleNamespace:
        """The 0x94-byte storage and bar entry: the item, its name and description."""
        description = runtime.namespace.get("itemList", {}).get(item.item_id, ()) if item.item_id else ()
        return types.SimpleNamespace(m_stItem=item_view(item),
                                     m_szName=description[2] if len(description) > 2 else "",
                                     m_szDescrip=description[3] if len(description) > 3 else "")

    def GetStorage():
        storage = [prop_entry(item) for item in inventory() if item.quantity > 0 and item.status != shop.STATUS_RECYCLED
                   and in_storage(item.item_id, item.status)]
        return settle_storage(storage) if storage_sorted else storage[:STORAGE_LIMIT]

    def RequestSettleStorage():
        nonlocal storage_sorted
        storage_sorted = True

    def GetEquipedProp():
        return [prop_entry((find_item(item_id) if item_id else None) or EMPTY_ITEM) for item_id in slots]

    def EquipOneProp(slot, item_id):
        """QQTSection.dll 0x1002B37A. Slot -1 puts the prop in the first free
        slot, replacing slot 0 when the bar is full; item 0 empties a slot; a
        prop already on the bar moves."""
        slot, item_id = int(slot), int(item_id)
        if slot == -1:
            if not is_bar_prop(item_id) or item_id in slots:
                return
            free = next((index for index, value in enumerate(slots) if not is_bar_prop(value)), None)
            if free is None:
                set_status(slots[0], False)
                free = 0
            slots[free] = item_id
            set_status(item_id, True)
            save()
            return
        if not 0 <= slot < SLOTS:
            return
        if item_id == 0:
            if is_bar_prop(slots[slot]):
                set_status(slots[slot], False)
            slots[slot] = 0
            save()
            return
        if not is_bar_prop(item_id):
            return
        moved = item_id in slots
        if moved:
            index = slots.index(item_id)
            if index == slot:
                return
            slots[index] = 0
        if slots[slot]:
            set_status(slots[slot], False)
        slots[slot] = item_id
        if not moved:
            set_status(item_id, True)
        save()

    session.on_entered.append(load)
    return {"GetStorage": GetStorage, "GetEquipedProp": GetEquipedProp, "EquipOneProp": EquipOneProp,
            "RequestSettleStorage": RequestSettleStorage}
