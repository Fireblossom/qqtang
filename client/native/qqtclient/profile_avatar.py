"""The character in the player information dialog (UI.playerInfoDlg.playerDemo).

When a FIND_FRIEND answer arrives, Client.exe 0x0048EB20 -> 0x0054E8B0 builds
a fresh avatar for the player's role (1 when the role is 0 or above 22) and
runs the script's playerInfo_useCommItem(id) for every item worn by that
role (item byte +7, ItemInfo.role_id). The script answers with
selRoom_playerWear (0x0054EB90 -> 0x00442210), and the widget's
SC_selRoom_drawPlayer callback (0x0054EB60) draws the standing avatar.
"""

from __future__ import annotations

import dataclasses
from typing import Any, Callable

import pygame

from .avatar import REMOVED_PART, AvatarCache
from .runtime import UIRuntime
from .script import call_script

DEMO_BOX = "UI.playerInfoDlg.playerDemo.demo"
MAX_ROLE = 22
REMOVED = REMOVED_PART


@dataclasses.dataclass
class ProfileAvatar:
    role: int = 0  # 0: nobody looked up yet
    wear: dict[str, int] = dataclasses.field(default_factory=dict)


def wear(avatar: ProfileAvatar, kind: str, part: int) -> None:
    """0x00442210: a cap hides the hair; a hairstyle sets the cap to 0 (no
    hair) or 100."""
    kind, part = str(kind), int(part)
    if kind.lower() == "cap":
        avatar.wear["hair"] = REMOVED
    if kind.lower() == "hair":
        avatar.wear["cap"] = REMOVED if part == 0 else 100
    avatar.wear[kind] = part if part > 0 else REMOVED


def build_profile_avatar(runtime: UIRuntime) -> tuple[ProfileAvatar, Callable[[Any], None], dict[str, Callable[..., Any]]]:
    """The avatar, a dresser for a FoundPlayer, and the native."""
    avatar = ProfileAvatar()

    def dress(found: Any) -> None:
        role = found.game_info.role_id if found.game_info is not None else 0
        avatar.role = role if 0 < role <= MAX_ROLE else 1
        avatar.wear.clear()
        use_item = runtime.namespace.get("playerInfo_useCommItem")
        for item in found.items:
            if item.role_id == avatar.role and callable(use_item):
                call_script(use_item, item.item_id)

    def selRoom_playerWear(kind, part, _effect=0, _color=0):
        wear(avatar, kind, part)

    return avatar, dress, {"selRoom_playerWear": selRoom_playerWear}


def profile_drawer(runtime: UIRuntime, avatar: ProfileAvatar, avatars: AvatarCache) -> Callable[[pygame.Surface], None]:
    def draw(screen: pygame.Surface) -> None:
        widget = runtime.find(DEMO_BOX)
        if widget is None or avatar.role == 0:
            return
        image = avatars.get(avatar.role, 0, "stand", 3, 0, tuple(sorted(avatar.wear.items())))
        if image is not None:
            x, y = widget.absolute()
            # The 100x100 avatar canvas, centred, feet near the box's bottom
            # (as the shop preview; the native placement is not traced).
            screen.blit(image, (x + widget.w // 2 - 50, y + widget.h - 100))

    return draw
