"""A battle: the field, its actors, bubbles, flames and items.

The Go sidecar owns the rules; this class turns its state into frames and
keyboard input into engine actions. Coordinates from the engine are pixels
from the top-left of the map with actors at their centre (40 px cells).
"""

from __future__ import annotations

import dataclasses
from typing import Any, Callable

import pygame

from ..avatar import AvatarCache
from ..render import SpriteCache
from ..resources import Resources
from ..runtime import UIRuntime
from ..script import call_script, log
from . import mapfile
from .sidecar import (ACTOR_ELIMINATED, ACTOR_TRAPPED, EVENT_ACTOR_ELIMINATED, EVENT_BOMB_EXPLODED, EVENT_CELL_DESTROYED,
                      EVENT_MAP_ELEMENT_MOVED, MOVE_DOWN, MOVE_LEFT, MOVE_NONE, MOVE_RIGHT, MOVE_UP, TICK_MS, Sidecar)

CELL = mapfile.CELL
# Where the 600x520 field sits on the 800x600 screen, inside the host-drawn
# frame (object/ui/game/gametop.img 623x27 and gameleft.img 10x544).
FIELD_ORIGIN = (10, 27)
FRAME_TOP = "object/ui/game/gametop.img"
FRAME_LEFT = "object/ui/game/gameleft.img"
BOMB_IMAGE = "object/bomb/bomb1_stand.img"
# Avatar canvas (100x100) point that sits on the actor's position.
AVATAR_ANCHOR = (50, 64)
WALK_FRAME_MS = 100
FLAME_FRAME_MS = 70
ITEM_FRAME_MS = 120
DIE_MS = 1000
# Being trapped: the full-body trigger pose under a syrup blob, which first
# forms (misc111_stand, 5 frames) and then wobbles (misc111_trigger). The
# blob was identified from the assets, not traced in Client.exe.
SYRUP_FORM = "object/misc/misc111_stand.img"
SYRUP_LOOP = "object/misc/misc111_trigger.img"
SYRUP_FRAME_MS = 80
SYRUP_ALPHA = 190
TRAPPED_FRAME_MS = 100
DESTROY_MS = 300
# Engine direction -> avatar sprite direction (0 right, 1 up, 2 left, 3 down).
SPRITE_DIRECTION = {MOVE_UP: 1, MOVE_RIGHT: 0, MOVE_DOWN: 3, MOVE_LEFT: 2, MOVE_NONE: 3}
KEY_MOVES = {pygame.K_UP: MOVE_UP, pygame.K_RIGHT: MOVE_RIGHT, pygame.K_DOWN: MOVE_DOWN, pygame.K_LEFT: MOVE_LEFT}
# Client.exe's default key layout (0x0048AE40, Windows VK codes): space puts
# a bubble, 1..7 use held-item slot 0..6, Ctrl uses the selected slot and Tab
# switches it.
BOMB_KEYS = (pygame.K_SPACE,)
SLOT_KEYS = {pygame.K_1 + index: index for index in range(7)}
USE_KEYS = (pygame.K_LCTRL, pygame.K_RCTRL)
SWITCH_KEYS = (pygame.K_TAB,)
NEEDLE_ACTION = 63  # self-rescue from a bubble
STATUS_BAR = "UI.game.statusBar"
# Held-item slot icons (baseItemIcon<i>) on the status bar at y 540.
SLOT_ORIGIN = (185, 540)
SLOT_STEP = 54
SLOT_SIZE = 50
ROUND_END_MS = 240000  # battleengine.StandardRoundTimeMS, including the 3 s Ready/Go
PLAYER_LIST = "UI.game.playerListDlg"
# Rows of the in-game player list (uiGame playerListDlg.playerBtn<i>); the
# native host draws each player's head, name and state into them.
PLAYER_ROW_ORIGIN = (609 + 46, 91)
PLAYER_ROW_STEP = 48
HEAD_CROP = pygame.Rect(26, 18, 48, 40)  # head area of the 100x100 avatar canvas
GAME_MODE_NORMAL = 0
READY_MS = 3000  # online Ready/Go countdown from GAME_BEGIN
RESULT_DRAW, RESULT_WIN = 1, 2  # net.match.RESULT_*
# Result screen (object/misc): a Win/Lose/Draw card, then the result table.
RESULT_CARDS = {RESULT_WIN: 297, RESULT_DRAW: 295}  # otherwise 296, Lose
RESULT_WORDS = {RESULT_WIN: 314, RESULT_DRAW: 316}  # otherwise 315, 失败
RESULT_TABLE = "object/misc/misc300_stand.img"  # 520x438: 胜负 昵称 杀敌数 救人数 等级 特殊荣誉
RESULT_CARD_MS = 2500
RESULT_TABLE_SIZE = (520, 438)
# Inside the Win card (misc297, 594x327): the blank panel for the winners.
WIN_PANEL = pygame.Rect(130, 135, 370, 175)
RESULT_ROW_Y = 190  # centre of row 1 in the table image
RESULT_ROW_STEP = 28
RESULT_COLUMNS = (75, 139, 208, 285, 355)  # column centres in the table image
RESULT_TEXT = (16, 62, 112)
# Top of the table: the local player's head, name and four stat boxes
# labelled with misc 303 杀敌, 302 救人, 307 得分 and 306 奖励.
RESULT_HEAD = (16, 15)
RESULT_NAME = (264, 31)
RESULT_STATS = ((129, 55, 303), (319, 55, 302), (129, 91, 307), (319, 91, 306))


@dataclasses.dataclass
class ResultRow:
    """One player's line of the match result (NOTIFY_GAME_OVER)."""
    player_id: int
    result: int  # RESULT_*
    kills: int
    rescues: int
    reward: int
    point: int  # points won in this match
    level_points: int  # total points, for the level icon


@dataclasses.dataclass
class Explosion:
    row: int
    col: int
    blast: tuple[int, int, int, int]  # inclusive row min, row max, col min, col max


def flame_image(explosion: Explosion | None, row: int, col: int) -> int:
    """object/flame/flame<k>: 1 centre, 2..5 arm and 6..9 arm end for the
    directions right, up, left, down."""
    if explosion is None or (row, col) == (explosion.row, explosion.col):
        return 1
    row_min, row_max, col_min, col_max = explosion.blast
    if row == explosion.row:
        direction, end = (0, col == col_max) if col > explosion.col else (2, col == col_min)
    else:
        direction, end = (3, row == row_max) if row > explosion.row else (1, row == row_min)
    return (6 if end else 2) + direction


class BattleScene:
    """state is the started sidecar's init reply. In realtime (online) mode
    the sidecar keeps its own clock, so it is stepped every frame."""

    def __init__(self, resources: Resources, sprites: SpriteCache, avatars: AvatarCache, sidecar: Sidecar,
                 map_id: int, players: list[dict[str, Any]], local_id: int, state: dict[str, Any], realtime: bool = False,
                 wear: dict[int, tuple[tuple[str, int], ...]] | None = None):
        """wear: worn costume parts by PlayerID, as the room drew them."""
        self.resources, self.sprites, self.avatars, self.sidecar = resources, sprites, avatars, sidecar
        self.local_id = local_id
        self.wear = wear or {}
        description = mapfile.load_descriptions(resources)[map_id]
        self.map = mapfile.parse_map(resources.read("map/" + description.file_name))
        self.elements = mapfile.load_elements(resources)
        self.map_name = description.name
        self.state = state
        self.realtime = realtime
        # Online: TCP notifications for the sidecar, and where its TCP
        # copies go.
        self.pending_tcp: list[tuple[int, bytes]] = []
        self.on_tcp_out: Callable[[int, bytes], None] | None = None
        # Online: the server's verdict for the banner (net.match.RESULT_*).
        self.result: int | None = None
        self.result_rows: list[ResultRow] = []
        self.result_shown_at = 0.0
        self.end_logged = False
        self.previous_positions: dict[int, tuple[int, int]] = {}
        self.walk_started: dict[int, int] = {}
        # Wall-clock ms since the scene started; keeps running after the match
        # ends, when the engine clock stops.
        self.now_ms = 0.0
        self.eliminated_at: dict[int, float] = {}  # by now_ms
        self.trapped_at: dict[int, float] = {}  # by now_ms
        self.explosions: dict[int, Explosion] = {}
        self.dying: dict[tuple[int, int, int], tuple[int, int]] = {}  # (layer, row, col) -> (element, clock)
        self.held: list[int] = []
        self.bomb_pressed = False
        self.use_pressed = 0  # held-item action to use on the next step
        self.prop_pressed = 0  # room prop (item ID) to use on the next step
        self.selected_slot = 0
        self.shown_slots: list[tuple[int, int]] | None = None
        self.accumulated_ms = 0.0
        self.names = {player["id"]: player.get("name") or f"玩家{player['id']}" for player in players}
        self.runtime: UIRuntime | None = None
        self.shown_seconds = -1
        self.font: Callable[[int], pygame.font.Font] | None = None
        log(f"[battle] map {map_id} {self.map_name} ({self.map.width}x{self.map.height}, rule {self.map.rule})")

    # UI.game ------------------------------------------------------------------

    def attach(self, runtime: UIRuntime, map_id: int, font: Callable[[int], pygame.font.Font]) -> None:
        """Prepare UI.game the way the native host does when a match starts."""
        self.runtime, self.font = runtime, font
        self._script("ui_SetGameMode", GAME_MODE_NORMAL)
        self._script("ui_InitPVEBoss")  # hides the boss bars when there are none
        self._script("game_setMapID", map_id)
        self._update_timer()

    def _script(self, name: str, *args: Any) -> Any:
        function = self.runtime.namespace.get(name) if self.runtime else None
        return call_script(function, *args) if callable(function) else None

    def _update_timer(self) -> None:
        seconds = max(0, ROUND_END_MS - self.clock) // 1000
        if seconds != self.shown_seconds and self.runtime is not None:
            self.shown_seconds = seconds
            self.runtime.namespace["Win_SetText"](PLAYER_LIST + ".time", f"{seconds // 60:02d}:{seconds % 60:02d}")

    # Input and stepping -----------------------------------------------------

    def handle(self, event: pygame.event.Event) -> None:
        if event.type == pygame.KEYDOWN:
            if event.key in KEY_MOVES:
                move = KEY_MOVES[event.key]
                if move in self.held:
                    self.held.remove(move)
                self.held.append(move)  # the latest key wins
            elif event.key in BOMB_KEYS:
                self.bomb_pressed = True
            elif event.key in SLOT_KEYS:
                self._press_use(self._slots()[SLOT_KEYS[event.key]][0])
            elif event.key in USE_KEYS:
                self._press_use(self._smart_use())
            elif event.key in SWITCH_KEYS:
                self._switch_slot()
        elif event.type == pygame.KEYUP and event.key in KEY_MOVES:
            move = KEY_MOVES[event.key]
            if move in self.held:
                self.held.remove(move)

    def _local_actor(self) -> dict[str, Any] | None:
        return next((actor for actor in self.state["actors"] if actor["id"] == self.local_id), None)

    def _press_use(self, value: int) -> None:
        """Slot values above 255 are props brought from the room (item IDs)."""
        if value > 0xFF:
            self.prop_pressed = value
        else:
            self.use_pressed = value

    def _slots(self) -> list[tuple[int, int]]:
        """The local player's seven item slots as (action or item, count).
        Room props keep the first slots in GAME_BEGIN order (an empty slot
        once used up) and the held-item slots follow them; the native
        arrangement is inferred, its slot writer (0x005DAC82) is only reached
        from protected code."""
        prepared = [(entry["item"], entry["count"]) if entry["count"] > 0 else (0, 0)
                    for entry in self.state.get("prepared") or ()][:7]
        actor = self._local_actor()
        held = [(action, count) if count else (0, 0) for action, count in (actor.get("held") or ())] if actor else []
        return (prepared + held + [(0, 0)] * 7)[:7]

    def _smart_use(self) -> int:
        """Ctrl: the needle when trapped, otherwise the selected slot. Inferred
        from the key names; the native chooser was not traced."""
        slots = self._slots()
        actor = self._local_actor()
        if actor is not None and actor["state"] == ACTOR_TRAPPED:
            return NEEDLE_ACTION if any(action == NEEDLE_ACTION for action, _ in slots) else 0
        return slots[self.selected_slot][0]

    def _switch_slot(self) -> None:
        slots = self._slots()
        for step in range(1, 8):
            index = (self.selected_slot + step) % 7
            if slots[index][0]:
                self.selected_slot = index
                return

    def _update_status_bar(self) -> None:
        slots = self._slots()
        if slots == self.shown_slots or self.runtime is None:
            return
        self.shown_slots = slots
        if not slots[self.selected_slot][0]:
            self._switch_slot()
        for index, (action, count) in enumerate(slots):
            icon = {41: 21, 42: 23}.get(action, action)  # 0x005DAC82
            self.runtime.namespace["Win_SetImg"](f"{STATUS_BAR}.baseItemIcon{index}", f"res/uiRes/icon/item/item{icon}.img" if action else "")
            self.runtime.namespace["Win_SetText"](f"{STATUS_BAR}.itemNum{index}", str(count))

    @property
    def clock(self) -> int:
        return int(self.state["clock"])

    @property
    def outcome(self) -> dict[str, Any]:
        return self.state["outcome"]

    def tick(self, elapsed_ms: float) -> None:
        self.now_ms += elapsed_ms
        # Online the sidecar still exchanges network traffic after its own
        # engine has ended; the server decides when the match is over.
        if self.outcome["ended"] and not self.realtime:
            return
        self.accumulated_ms += elapsed_ms
        ticks = int(self.accumulated_ms // TICK_MS)
        if ticks <= 0 and not self.realtime:
            return
        self.accumulated_ms -= ticks * TICK_MS
        action = {"id": self.local_id, "move": self.held[-1] if self.held else MOVE_NONE, "bomb": self.bomb_pressed,
                  "use": self.use_pressed, "prop": self.prop_pressed}
        self.bomb_pressed, self.use_pressed, self.prop_pressed = False, 0, 0
        previous = {actor["id"]: (actor["x"], actor["y"]) for actor in self.state["actors"]}
        previous_clock = self.clock
        tcp, self.pending_tcp = self.pending_tcp, []
        self.state = self.sidecar.step([action], min(ticks, 10), tcp)
        for packet in self.state.get("tcp_out") or ():
            if self.on_tcp_out is not None:
                self.on_tcp_out(packet["cmd"], bytes.fromhex(packet["payload"]))
        for actor in self.state["actors"] if self.clock != previous_clock else ():
            moved = previous.get(actor["id"]) != (actor["x"], actor["y"])
            if moved and actor["id"] not in self.walk_started:
                self.walk_started[actor["id"]] = self.clock
            elif not moved:
                self.walk_started.pop(actor["id"], None)
        for event in self.state["events"] or ():
            self._apply(event)
        self._update_timer()
        self._update_status_bar()
        if self.outcome["ended"] and not self.end_logged:
            self.end_logged = True
            log(f"[battle] ended: {self.outcome}")

    def _apply(self, event: dict[str, Any]) -> None:
        kind, cell = event["kind"], event["cell"]
        if kind == EVENT_BOMB_EXPLODED:
            self.explosions[event["bomb"]] = Explosion(cell["row"], cell["col"], tuple(event["blast"]))
        elif kind == EVENT_CELL_DESTROYED:
            for layer in (mapfile.LAYER_WALL, mapfile.LAYER_TOP):
                element = self.map.element(layer, cell["row"], cell["col"])
                if element > 0 and self.elements.get(element, mapfile.ElementInfo()).life_time > 0:
                    self._set(layer, cell["row"], cell["col"], 0)
                    self.dying[(layer, cell["row"], cell["col"])] = (element, event["time"])
                    break
        elif kind == EVENT_MAP_ELEMENT_MOVED:
            source = event["from"]
            element = self.map.element(mapfile.LAYER_WALL, source["row"], source["col"])
            self._set(mapfile.LAYER_WALL, source["row"], source["col"], 0)
            self._set(mapfile.LAYER_WALL, cell["row"], cell["col"], element)
        elif kind == EVENT_ACTOR_ELIMINATED:
            self.eliminated_at[event["target"] or event["player"]] = self.now_ms

    def _set(self, layer: int, row: int, col: int, element: int) -> None:
        self.map.layers[layer][row * self.map.width + col] = element

    # Drawing ----------------------------------------------------------------

    def draw(self, screen: pygame.Surface) -> None:
        ox, oy = FIELD_ORIGIN
        field = screen.subsurface(pygame.Rect(0, 0, ox + self.map.width * CELL, oy + self.map.height * CELL))
        for row in range(self.map.height):
            for col in range(self.map.width):
                self._draw_element(field, self.map.element(mapfile.LAYER_FLOOR, row, col), row, col)
        for pickup in self.state["pickups"] or ():
            self._draw_frames(field, f"object/item/item{pickup['scene']}_stand.img", self.clock // ITEM_FRAME_MS,
                              ox + pickup["col"] * CELL, oy + pickup["row"] * CELL)
        for field_object in self.state.get("fields") or ():
            # Banana and glue placed with items 41/42 (scene IDs 42/43).
            self._draw_frames(field, f"object/item/item{field_object['action']}_stand.img", self.clock // ITEM_FRAME_MS,
                              ox + field_object["col"] * CELL, oy + field_object["row"] * CELL)
        for bomb in self.state["bombs"] or ():
            self._draw_frames(field, BOMB_IMAGE, self.clock // 150, ox + bomb["col"] * CELL - 2, oy + bomb["row"] * CELL - 1)
        for flame in self.state["flames"] or ():
            self._draw_flame(field, flame)
        actors_by_row: dict[int, list[dict[str, Any]]] = {}
        for actor in self.state["actors"]:
            actors_by_row.setdefault(min(max(actor["y"] // CELL, 0), self.map.height - 1), []).append(actor)
        for row in range(self.map.height):
            for layer in (mapfile.LAYER_WALL, mapfile.LAYER_TOP):
                for col in range(self.map.width):
                    self._draw_element(field, self.map.element(layer, row, col), row, col)
                    dying = self.dying.get((layer, row, col))
                    if dying is not None:
                        self._draw_dying(field, dying, row, col)
            for actor in sorted(actors_by_row.get(row, ()), key=lambda item: item["y"]):
                self._draw_actor(field, actor)
        for path, position in ((FRAME_TOP, (0, 0)), (FRAME_LEFT, (0, oy))):
            self._draw_frames(screen, path, 0, *position)

    def _draw_frames(self, surface: pygame.Surface, path: str, frame: int, x: int, y: int) -> None:
        frames = self.sprites.get(path)
        if not frames:
            return
        image, offset_x, offset_y = frames[frame % len(frames)]
        if image is not None:
            surface.blit(image, (x + offset_x, y + offset_y))

    def _draw_element(self, surface: pygame.Surface, element: int, row: int, col: int, action: str = "stand", frame: int = 0) -> None:
        if element <= 0:
            return
        path = mapfile.element_image(element, action)
        if path is None:
            return
        info = self.elements.get(element, mapfile.ElementInfo())
        ox, oy = FIELD_ORIGIN
        self._draw_frames(surface, path, frame, ox + col * CELL - info.offset[0], oy + row * CELL - info.offset[1])

    def _draw_dying(self, surface: pygame.Surface, dying: tuple[int, int], row: int, col: int) -> None:
        element, started = dying
        frames = self.sprites.get(mapfile.element_image(element, "die") or "")
        elapsed = self.clock - started
        if not frames or elapsed >= DESTROY_MS:
            return
        self._draw_element(surface, element, row, col, "die", min(len(frames) - 1, elapsed * len(frames) // DESTROY_MS))

    def _draw_flame(self, surface: pygame.Surface, flame: dict[str, Any]) -> None:
        row, col = flame["row"], flame["col"]
        image = flame_image(self.explosions.get(flame["bomb"]), row, col)
        frames = self.sprites.get(f"object/flame/flame{image}_stand.img")
        if not frames:
            return
        frame = max(0, self.clock - flame["impact"]) // FLAME_FRAME_MS
        ox, oy = FIELD_ORIGIN
        self._draw_frames(surface, f"object/flame/flame{image}_stand.img", min(frame, len(frames) - 1),
                          ox + col * CELL, oy + row * CELL)

    def _draw_actor(self, surface: pygame.Surface, actor: dict[str, Any]) -> None:
        dead_for = self.now_ms - self.eliminated_at.get(actor["id"], self.now_ms - DIE_MS)
        if actor["state"] == ACTOR_ELIMINATED and dead_for >= DIE_MS:
            return
        role = actor["avatar_role"] or actor["role"]
        direction = SPRITE_DIRECTION.get(actor["facing"], 3)
        if actor["state"] == ACTOR_TRAPPED:
            trapped_for = self.now_ms - self.trapped_at.setdefault(actor["id"], self.now_ms)
        else:
            self.trapped_at.pop(actor["id"], None)
        if actor["state"] == ACTOR_ELIMINATED:
            image = self.avatars.get(role, actor["team"] - 1, "die", direction, int(dead_for // (DIE_MS // 4)), wear=self.wear.get(actor["id"], ()))
        elif actor["state"] == ACTOR_TRAPPED:
            image = self.avatars.get(role, actor["team"] - 1, "trigger", direction, int(trapped_for // TRAPPED_FRAME_MS), wear=self.wear.get(actor["id"], ()))
        elif actor["id"] in self.walk_started:
            frame = (self.clock - self.walk_started[actor["id"]]) // WALK_FRAME_MS
            image = self.avatars.get(role, actor["team"] - 1, "walk", direction, frame, wear=self.wear.get(actor["id"], ()))
        else:
            image = self.avatars.get(role, actor["team"] - 1, "stand", direction, wear=self.wear.get(actor["id"], ()))
        ox, oy = FIELD_ORIGIN
        x, y = ox + actor["x"], oy + actor["y"]
        if image is not None:
            if actor["state"] == ACTOR_ELIMINATED:
                image = image.copy()
                image.set_alpha(int(255 * (1 - dead_for / DIE_MS)))
            surface.blit(image, (x - AVATAR_ANCHOR[0], y - AVATAR_ANCHOR[1]))
        if actor["state"] == ACTOR_TRAPPED:
            self._draw_syrup(surface, trapped_for, x - AVATAR_ANCHOR[0], y - AVATAR_ANCHOR[1])

    def _draw_syrup(self, surface: pygame.Surface, trapped_for: float, x: int, y: int) -> None:
        """The syrup blob on the actor's 100x100 avatar canvas at (x, y)."""
        forming = self.sprites.get(SYRUP_FORM) or []
        frame = int(trapped_for // SYRUP_FRAME_MS)
        if frame < len(forming):
            image, offset_x, offset_y = forming[frame]
        else:
            loop = self.sprites.get(SYRUP_LOOP)
            if not loop:
                return
            image, offset_x, offset_y = loop[(frame - len(forming)) % len(loop)]
        if image is not None:
            image = image.copy()
            image.set_alpha(SYRUP_ALPHA)
            surface.blit(image, (x + offset_x, y + offset_y))

    def show_results(self, rows: list[ResultRow]) -> None:
        self.result_rows = sorted(rows, key=lambda row: (-row.result, row.player_id != self.local_id))
        self.result_shown_at = self.now_ms

    def _misc(self, number: int) -> pygame.Surface | None:
        frames = self.sprites.get(f"object/misc/misc{number}_stand.img")
        return frames[0][0] if frames else None

    def _blit_centered(self, screen: pygame.Surface, image: pygame.Surface | None, center: tuple[int, int]) -> None:
        if image is not None:
            screen.blit(image, image.get_rect(center=center))

    def _draw_winners(self, screen: pygame.Surface, card: pygame.Rect) -> None:
        winners = [row.player_id for row in self.result_rows if row.result == RESULT_WIN]
        actors = {actor["id"]: actor for actor in self.state["actors"]}
        panel = WIN_PANEL.move(card.topleft)
        for index, player_id in enumerate(winners[:4]):
            actor = actors.get(player_id)
            image = self.avatars.get(actor["role"], actor["team"] - 1, "stand", wear=self.wear.get(player_id, ())) if actor else None
            if image is not None:
                x = panel.left + panel.width * (2 * index + 1) // (2 * min(len(winners), 4))
                screen.blit(image, image.get_rect(midbottom=(x, panel.bottom)))

    def _draw_results(self, screen: pygame.Surface) -> None:
        assert self.font is not None
        ox, oy = FIELD_ORIGIN
        center = (ox + self.map.width * CELL // 2, oy + self.map.height * CELL // 2)
        if self.now_ms - self.result_shown_at < RESULT_CARD_MS:
            card = self._misc(RESULT_CARDS.get(self.result if self.result is not None else -1, 296))
            self._blit_centered(screen, card, center)
            if card is not None and self.result == RESULT_WIN:
                self._draw_winners(screen, card.get_rect(center=center))
            return
        frames = self.sprites.get(RESULT_TABLE)
        if not frames or frames[0][0] is None:
            return
        tx, ty = center[0] - RESULT_TABLE_SIZE[0] // 2, center[1] - RESULT_TABLE_SIZE[1] // 2
        screen.blit(frames[0][0], (tx, ty))
        font = self.font(12)
        mine = next((row for row in self.result_rows if row.player_id == self.local_id), None)
        local = self._local_actor()
        if local is not None:
            head = self.avatars.get(local["role"], local["team"] - 1, "stand", wear=self.wear.get(local["id"], ()))
            if head is not None:
                screen.blit(head, (tx + RESULT_HEAD[0], ty + RESULT_HEAD[1]))
        name = self.font(14).render(self.names.get(self.local_id, ""), True, (255, 255, 255))
        screen.blit(name, name.get_rect(center=(tx + RESULT_NAME[0], ty + RESULT_NAME[1])))
        if mine is not None:
            for (x, y, label), value in zip(RESULT_STATS, (mine.kills, mine.rescues, mine.point, mine.reward)):
                label_image = self._misc(label)
                if label_image is not None:
                    screen.blit(label_image, (tx + x + 8, ty + y + 6))
                text = font.render(str(value), True, (255, 255, 255))
                screen.blit(text, (tx + x + 60, ty + y + 7))
        level = self.runtime.namespace.get("Level") if self.runtime else None
        for index, row in enumerate(self.result_rows[:8]):
            y = ty + RESULT_ROW_Y + index * RESULT_ROW_STEP
            columns = [tx + x for x in RESULT_COLUMNS]
            self._blit_centered(screen, self._misc(RESULT_WORDS.get(row.result, 315)), (columns[0], y))
            for column, text in zip(columns[1:4], (self.names.get(row.player_id, ""), str(row.kills), str(row.rescues))):
                rendered = font.render(text, True, RESULT_TEXT)
                screen.blit(rendered, rendered.get_rect(center=(column, y)))
            if callable(level):
                # uiGame's Level (levelCFG.py) names the medal and tie images.
                _title, main_level, min_level = level().getInfo(row.level_points)
                for path in (level().getMainLevel(main_level), level().getMinLevel(main_level, min_level)):
                    icon = self.sprites.get(path)
                    if icon and icon[0][0] is not None:
                        screen.blit(icon[0][0], (columns[4] - 20, y - 8))

    def draw_overlay(self, screen: pygame.Surface) -> None:
        """Host-drawn parts above UI.game: player rows and the result banner."""
        if self.font is None:
            return
        if self._slots()[self.selected_slot][0]:
            slot = pygame.Rect(SLOT_ORIGIN[0] + self.selected_slot * SLOT_STEP, SLOT_ORIGIN[1], SLOT_SIZE, SLOT_SIZE)
            pygame.draw.rect(screen, (255, 230, 80), slot, 2)
        x, y = PLAYER_ROW_ORIGIN
        for index, actor in enumerate(self.state["actors"]):
            row_y = y + index * PLAYER_ROW_STEP
            head = self.avatars.get(actor["role"], actor["team"] - 1, "stand", wear=self.wear.get(actor["id"], ()))
            if head is not None:
                portrait = head.subsurface(HEAD_CROP).copy()
                if actor["state"] == ACTOR_ELIMINATED:
                    portrait.fill((90, 90, 90, 255), special_flags=pygame.BLEND_RGBA_MULT)
                screen.blit(portrait, (x, row_y))
            status = {ACTOR_TRAPPED: "（被困）", ACTOR_ELIMINATED: "（阵亡）"}.get(actor["state"], "")
            label = self.font(12).render(self.names.get(actor["id"], "") + status, True, (255, 255, 255))
            screen.blit(label, (x + 52, row_y + 14))
        if self.result_rows:
            self._draw_results(screen)
            return
        text = None
        if self.realtime and self.now_ms < READY_MS:
            text = "准备" if self.now_ms < READY_MS - 1000 else "开始！"
        elif self.result is not None:
            text = {RESULT_WIN: "胜利！", RESULT_DRAW: "平局"}.get(self.result, "失败")
        elif self.outcome["ended"] and not self.realtime:
            local = next((actor for actor in self.state["actors"] if actor["id"] == self.local_id), None)
            if self.outcome["draw"] or self.outcome["timed_out"] and not self.outcome["winner"]:
                text = "平局"
            elif local is not None and local["team"] == self.outcome["winner"]:
                text = "胜利！"
            else:
                text = "失败"
        if text is not None:
            banner = self.font(48).render(text, True, (255, 230, 80))
            ox, oy = FIELD_ORIGIN
            center = (ox + self.map.width * CELL // 2, oy + self.map.height * CELL // 2)
            shade = pygame.Surface((banner.get_width() + 60, banner.get_height() + 30), pygame.SRCALPHA)
            shade.fill((0, 0, 0, 150))
            screen.blit(shade, shade.get_rect(center=center))
            screen.blit(banner, banner.get_rect(center=center))

    def close(self) -> None:
        self.sidecar.close()
