"""The newbie tutorial's stages (maps 1501..1504, then 311) on screen.

The sidecar's tutorial mode (cmd/qqt-battle-sidecar/tutorial.go) runs
Task.txt's controller next to the engine; every step answers with the
engine state and a "tutorial" part. This class draws that part the way
Client.exe does (docs/tutorial-re.md):

- hints through uiGame's own NotifyAddHint(flag, 270, y, text) /
  NotifyRemoveHint() (0x005AFA3E, 0x005AFAE5), with the speaker's picture
  misc(360 + role) (misc359 for Kubi [inferred]) at (214, 239);
- the target arrow misc355 and Kubi misc356 on the field;
- the side panel (0x005EE383): misc600, the stage's two task lines, the
  last item's description (misc602) and on 1503 the kinds eaten (misc601);
- the story scroll (misc360 rising over misc358, clipped to (160, 100,
  400, 400)) and the task cards misc371..374, during which nothing else is
  drawn (gm+0x56EFD = 0).

The local game ends on 0xFBB: the scene shows 胜利/失败 and, after the
result time or a key, on_leave(True) goes on with the tutorial's end flow
(0x005A4181); the leave button calls on_leave(False).
"""

from __future__ import annotations

import os
import random
from typing import Any, Callable

import pygame

from ..avatar import AvatarCache
from ..keylayout import BattleKeys
from ..net import match
from ..render import Renderer
from ..resources import Resources
from ..runtime import UIRuntime
from ..script import call_script, log
from .scene import CELL, FIELD_ORIGIN, BattleScene
from .sidecar import Sidecar, SidecarError

LOCAL_ID = 1
ROLE = 8  # QQTSection 0x1001C5A7 puts the player in as role 8
RESULT_SHOWN_MS = 12_000  # as free practice (practice.py) [inferred]
SKIP_KEYS = (pygame.K_RETURN, pygame.K_KP_ENTER, pygame.K_ESCAPE)

# The hint table at Client.exe 0x007F49C4 (29 GBK strings, read from the
# binary; the four leading spaces are part of them).
HINT_TEXTS = (
    "    我是英俊善良举世无双的的海王子，我要出色地完成我的任务……",
    "    有谁能救救我么？我和我的主人遭遇了海难，我被压在一个海葵的下面。",
    "    你真是个善良的人，你通过键盘的上下左右到箭头所指的位置",
    "    现在只要按空格键就可以将我周围的石头和海葵炸毁，我就自由了。",
    "    感谢你，善良而英俊的王子，你能帮忙也救救我的主人么。",
    "    我的主人就在那儿，你可以用刚才解救我的方法清理周围的障碍，然后戳破包裹主人的糖浆",
    "    主人，主人，看到你得救我太高兴了，我还以为再也见不到你了。",
    "    你现在感觉如何，是否需要休息一下",
    "    非常感谢，善良的人，是你救了我和酷比，真是太感谢你了！",
    "    你真是太聪明了！！！恭喜您顺利完成第一个任务！！！",
    "    你杀了不该杀的人，现在任务失败啦！",
    "    作为报答，我愿意将我一些小小的技巧和你分享，无论多么美丽的世界总会有些邪恶的东西，这些技巧或许对于保护自己会有帮助。",
    "    非常荣幸能够接受你的指导。",
    "    在糖果王国漫长历史中，人们发现了三种可以增加攻击力的物品。他们分别是糖果,药水,跑鞋。除此之外还有一些辅助的物品。",
    "    我如何得到他们提升自己的能力",
    "    现在你去炸毁周围那些海葵，他们就在里面",
    "    很好，这就是糖果了。最基本而且最重要的物品之一了，它能够增加你放QQ糖的数量。每获得一个，就能多放一个QQ糖。",
    "    你的运气不错啊，药水可以增加角色QQ糖的威力。每获得一个，QQ糖的威力就会增加一格",
    "    这鞋挺配你的衣服的，它能够提升你的移动速度，得到的越多，跑的越快",
    "    这是叉子，关键时候可以解救你，如果不慎被果冻困住，可以使用它脱离危险",
    "    这是一种有趣的辅助攻击物品，将它放置在敌人出没的路上，踩到的人会被缓速",
    "    你真是个幸运儿，它会在一定时间里使你放的QQ糖隐形",
    "    这是个恶作剧的东西。在路上放下，踩上的人会滑到路的尽头.至于是否对自己有利，看你怎么使用了",
    "    现在你不用一块块的炸了，你直接可以看到地下隐藏的物品",
    "    钱，我想不用我介绍它的作用了吧",
    "    我们要保卫自己的家园！！！！",
    "    象我这样放泡杀他，杀死侵略者！！！让我们来共同保卫自己的家园！",
    "    你真是太聪明了，恭喜你进入第三关！",
    "    我该怎么办呢？",
)

MISC = "object/misc/misc{}_stand.img"
AVATAR_AT = (214 - 12, 239 + 1)  # SetPos (214, 239), 0x005C477E's (-12, +1)
ARROW, KUBI = 355, 356
ARROW_FRAME_MS, KUBI_FRAME_MS = 150, 150  # [inferred]
KUBI_ANCHOR = (0, 20)  # 0x005C47DC(0, 0x14)
SCROLL, SCROLL_BACK = 360, 358
SCROLL_CLIP = pygame.Rect(160, 100, 400, 400)  # 0x005EE83F
SCROLL_FROM_Y, SCROLL_SPEED = 310, 20  # mover (160, 310) -> (160, -20), 20 [px/s inferred]
CARD_AT = (200 - 12, 180 + 1)  # SetPos (200, 180); drawn without the frame offset [inferred]
PANEL, PANEL_DIGITS, PANEL_ITEM = 600, 601, 602
PANEL_AT = (638 - 12, 157 + 1)
PANEL_TASK_AT, PANEL_GOAL_AT = (635 - 12, 220 + 1), (640 - 12, 298 + 1)
PANEL_ITEM_AT, PANEL_DIGITS_AT = (662 - 12, 385 + 1), (765 - 12, 325 + 1)
# 0x005EE584: the stage's task and goal lines.
PANEL_LINES = {1501: (604, 605), 1502: (604, 605), 1503: (607, 608), 1504: (610, 611), 311: (613, 614)}


def hint_box(text: str) -> tuple[int, int, int]:
    """0x005AFA3E: (flag, x, y) for NotifyAddHint by the GBK length."""
    length = len(text.encode("gbk", "replace"))
    if length < 0x65:
        return 3, 270, 190
    if length < 0xC9:
        return 2, 270, 155
    return 1, 270, 120  # the original then forgets the text (fixed)


class TutorialSidecar:
    """BattleScene steps this like a Sidecar; it adds the any-key flag."""

    def __init__(self, sidecar: Sidecar):
        self.sidecar = sidecar
        self.any_key = False

    def step(self, actions: list[dict[str, Any]], ticks: int = 1, tcp: Any = None) -> dict[str, Any]:
        any_key, self.any_key = self.any_key, False
        return self.sidecar.step(actions, ticks, tcp, any_key=any_key)

    def close(self) -> None:
        self.sidecar.close()


class TutorialBattle:
    def __init__(self, runtime: UIRuntime, renderer: Renderer, resources: Resources,
                 on_leave: Callable[[bool], None], autoplay: bool | None = None, uin: int | None = None):
        self.runtime, self.renderer, self.resources = runtime, renderer, resources
        self.on_leave = on_leave
        self.autoplay = os.environ.get("QQT_TUTORIAL_AUTOPLAY") == "1" if autoplay is None else autoplay
        self.uin = uin
        self.avatars = AvatarCache(resources)
        self.sidecar: TutorialSidecar | None = None
        self.scene: BattleScene | None = None
        self.stage = -1
        self.hint: tuple[int, int] | None = None
        self.result: int | None = None
        self.shown_ms = 0.0
        self.finished = False

    # Starting -------------------------------------------------------------------

    def start(self) -> None:
        sidecar = Sidecar()
        try:
            state = sidecar.start_tutorial(self.resources.root, LOCAL_ID, ROLE, random.getrandbits(31) or 1, self.autoplay)
        except SidecarError:
            sidecar.close()
            raise
        self.sidecar = TutorialSidecar(sidecar)
        self.runtime.namespace.update({
            "LeaveGame": self.leave,
            "GetPVEBossInfo": lambda: [],
            "GetPVEPlayerInfo": lambda: [],
        })
        self._show(state)

    def _show(self, state: dict[str, Any]) -> None:
        """A (new) stage: the local GAME_BEGIN on its map."""
        begin = state["begin"]
        self.stage = (state.get("tutorial") or {}).get("stage", 0)
        players = [dict(player, name=self.runtime.namespace.get("nickname") or "我", seat=0, points=0) for player in begin["players"]]
        if self.runtime.current_screen != "game":
            self.runtime.goto_screen("game")
        assert self.sidecar is not None
        scene = BattleScene(self.resources, self.renderer.sprites, self.avatars, self.sidecar, begin["map_id"], players,
                            local_id=LOCAL_ID, state=state)
        scene.show_player_list = False
        scene.keys = BattleKeys.for_uin(self.uin or self.runtime.namespace.get("uin") or None)
        scene.attach(self.runtime, begin["map_id"], self.renderer.font, self.renderer.text_line, self.renderer.draw_widget)
        self.scene = scene
        self.renderer.underlay, self.renderer.overlay = self.draw, self.draw_overlay
        for name in ("showRecordRecorder", "showRecordPlayer"):  # 0x005A3C7F
            self._script(name, 0)
        self._set_hint(None)
        log(f"[tutorial] stage {self.stage}: map {begin['map_id']}")

    def _script(self, name: str, *args: Any) -> Any:
        function = self.runtime.namespace.get(name)
        return call_script(function, *args) if callable(function) else None

    # Input and time -------------------------------------------------------------

    @property
    def tutorial(self) -> dict[str, Any]:
        return (self.scene.state.get("tutorial") or {}) if self.scene is not None else {}

    def handle(self, event: pygame.event.Event) -> None:
        if self.scene is None or self.finished:
            return
        if self.result is not None:
            if event.type == pygame.KEYDOWN and event.key in SKIP_KEYS or event.type == pygame.MOUSEBUTTONDOWN:
                self._finish(True)
            return
        if event.type == pygame.KEYDOWN and self.sidecar is not None:
            self.sidecar.any_key = True  # 0x005A542E: any key ends the scroll and the cards
        self.scene.handle(event)

    def tick(self, elapsed_ms: float) -> None:
        if self.scene is None or self.finished:
            return
        try:
            self.scene.tick(elapsed_ms)
        except SidecarError as error:
            log(f"[tutorial] sidecar failed: {error}")
            self._finish(False)
            return
        tutorial = self.tutorial
        if tutorial.get("stage", 0) != self.stage and self.scene.state.get("begin"):
            self._show(self.scene.state)  # action 10: the next map
            return
        hint = tutorial.get("hint")
        self._set_hint((hint["text"], hint["avatar"]) if hint else None)
        if self.result is None and tutorial.get("result") is not None:
            self.result = int(tutorial["result"])
            log(f"[tutorial] game over on map {tutorial.get('map_id')}: result {self.result}")
            self.scene.result = match.RESULT_WIN if self.result == match.RESULT_WIN else match.RESULT_LOSS
            self.scene.over = True
        if self.result is not None:
            self.shown_ms += elapsed_ms
            if self.shown_ms >= RESULT_SHOWN_MS:
                self._finish(True)

    def _set_hint(self, hint: tuple[int, int] | None) -> None:
        if hint == self.hint:
            return
        self.hint = hint
        if hint is None or not 0 <= hint[0] < len(HINT_TEXTS):
            self._script("NotifyRemoveHint")
            return
        text = HINT_TEXTS[hint[0]]
        flag, x, y = hint_box(text)
        self._script("NotifyAddHint", flag, x, y, text)

    # Drawing --------------------------------------------------------------------

    def _frames(self, screen: pygame.Surface, image: int, frame: int, x: int, y: int, offset: bool = True) -> None:
        frames = self.renderer.sprites.get(MISC.format(image))
        if not frames:
            return
        surface, offset_x, offset_y = frames[frame % len(frames)]
        if surface is not None:
            screen.blit(surface, (x + offset_x, y + offset_y) if offset else (x, y))

    def draw(self, screen: pygame.Surface) -> None:
        """Under the UI: the field, Kubi, the arrow and the speaker."""
        if self.scene is None:
            return
        self.scene.draw(screen)
        tutorial, now = self.tutorial, int(self.scene.now_ms)
        ox, oy = FIELD_ORIGIN
        if kubi := tutorial.get("kubi"):
            # Kubi's SetPos is the cell centre (0x005C6EC1), anchor (0, 20).
            self._frames(screen, KUBI, now // KUBI_FRAME_MS, ox + kubi["col"] * CELL - KUBI_ANCHOR[0], oy + kubi["row"] * CELL - KUBI_ANCHOR[1])
        if arrow := tutorial.get("arrow"):
            # On the local player's +0x41C at the cell centre (0x005C6F82);
            # its anchor is not known: centred above the cell [inferred].
            self._frames(screen, ARROW, now // ARROW_FRAME_MS, ox + arrow["col"] * CELL - 5, oy + arrow["row"] * CELL - 48)
        if self.hint is not None and self.hint[1]:
            self._frames(screen, self.hint[1], 0, *AVATAR_AT)

    def _draw_panel(self, screen: pygame.Surface, tutorial: dict[str, Any]) -> None:
        lines = PANEL_LINES.get(int(tutorial.get("map_id") or 0))
        if lines is None:
            return
        self._frames(screen, PANEL, 0, *PANEL_AT)
        self._frames(screen, lines[0], 0, *PANEL_TASK_AT)
        self._frames(screen, lines[1], 0, *PANEL_GOAL_AT)
        self._frames(screen, PANEL_ITEM, int(tutorial.get("panel_item") or 0), *PANEL_ITEM_AT)
        if tutorial.get("map_id") == 1503:
            self._frames(screen, PANEL_DIGITS, int(tutorial.get("eaten") or 0), *PANEL_DIGITS_AT)

    def draw_overlay(self, screen: pygame.Surface) -> None:
        """Above the UI: the result banner, or the scroll and the task card,
        which hide everything else (gm+0x56EFD = 0)."""
        if self.scene is None:
            return
        tutorial = self.tutorial
        if tutorial.get("hide_ui"):
            screen.fill((0, 0, 0))
            self._frames(screen, SCROLL_BACK, 0, 2, 1)
            if (scroll := tutorial.get("scroll_ms")) is not None:
                y = SCROLL_FROM_Y - int(scroll) * SCROLL_SPEED // 1000
                clip = screen.get_clip()
                screen.set_clip(SCROLL_CLIP)
                self._frames(screen, SCROLL, 0, SCROLL_CLIP.x - 12, y + 1)  # 0x005C4747's (-12, +1)
                screen.set_clip(clip)
            elif card := tutorial.get("card"):
                self._frames(screen, int(card), 0, *CARD_AT, offset=False)
            return
        # The panel is drawn by the scene in Client.exe and covers exactly
        # uiGame's player list (177x355 at (626, 158)); the host draws it
        # above that list [inferred z-order].
        self._draw_panel(screen, tutorial)
        self.scene.draw_overlay(screen)

    # Ending ---------------------------------------------------------------------

    def leave(self) -> None:
        """The leave button: 0x005A26C1 -> 0x005A2C6D -> 0x005A4181."""
        self._finish(False)

    def _finish(self, ended: bool) -> None:
        if self.finished:
            return
        self.finished = True
        self._set_hint(None)
        self.close()
        self.renderer.underlay = self.renderer.overlay = None
        self.on_leave(ended)

    def close(self) -> None:
        if self.scene is not None:
            self.scene.close()  # closes the sidecar
            self.scene = None
        elif self.sidecar is not None:
            self.sidecar.close()
        self.sidecar = None
