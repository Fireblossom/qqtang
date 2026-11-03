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
from ..render import SpriteCache, cell_width
from ..resources import Resources
from ..runtime import UIRuntime
from ..script import call_script, log
from . import mapfile
from .sidecar import (ACTOR_ELIMINATED, ACTOR_TRAPPED, EVENT_ACTOR_ELIMINATED, EVENT_ACTOR_RESPAWNED, EVENT_BOMB_EXPLODED,
                      EVENT_CELL_DESTROYED, EVENT_MAP_ELEMENT_MOVED, MOVE_DOWN, MOVE_LEFT, MOVE_NONE, MOVE_RIGHT, MOVE_UP, TICK_MS, Sidecar)

CELL = mapfile.CELL
# Where the 600x520 field sits on the 800x600 screen, inside the host-drawn
# frame (object/ui/game/gametop.img 623x27 and gameleft.img 10x544).
FIELD_ORIGIN = (10, 27)
FRAME_TOP = "object/ui/game/gametop.img"
FRAME_LEFT = "object/ui/game/gameleft.img"
BOMB_IMAGE = "object/bomb/bomb1_stand.img"
# 0x005DF6FB gives ordinary and power bubbles the same (0, 15) anchor
# as scene items. This is not half of the DIMG canvas size.
BOMB_ANCHOR = (0, 15)
# Kicked and dropped bubbles, and dropped items, move at 400 px/s
# (Client.exe 0x005D3C1D, 0x005A467D).
FLIGHT_PX_PER_MS = 0.4
FLIGHT_STEPS = {0: (1, 0), 1: (0, -1), 2: (-1, 0), 3: (0, 1)}  # wire direction: right, up, left, down
# Avatar canvas (100x100) point that sits on the actor's position.
AVATAR_ANCHOR = (50, 64)
# A named Boss plays its cloth's "birth" for 1000 ms (0x005ACBFC).
BOSS_BIRTH_MS = 1000
WALK_FRAME_MS = 100
FLAME_FRAME_MS = 70
ITEM_FRAME_MS = 120
# Scene items, loose buns and placed banana/glue (0x006046C7): sprite point =
# cell centre (0x005CCE0A, 0x005F760A), anchor (0, 15) except items 201
# (5, 20) and 203, 204 (0, 0) (0x005CCE83; loose buns 0x005F750E). With
# 0x005C477E's (-12, 1) and the field's (-8, 21) the image (with its own
# offset) goes to the cell's top-left minus the anchor. Measured on a 2021
# recording (Bilibili BV1Fh411m7Cx) for items 6, 9 and 11.
ITEM_ANCHOR = (0, 15)
ITEM_ANCHORS = {201: (5, 20), 203: (0, 0), 204: (0, 0)}
DIE_MS = 1000
# Being trapped: the full-body trigger pose under a syrup blob, which first
# forms (misc111_stand, 5 frames) and then wobbles (misc111_trigger). The
# blob was identified from the assets, not traced in Client.exe.
SYRUP_FORM = "object/misc/misc111_stand.img"
SYRUP_LOOP = "object/misc/misc111_trigger.img"
SYRUP_FRAME_MS = 80
SYRUP_ALPHA = 190
# Harm protection (relive, end of a transformation): Client.exe 0x005B5202
# plays effect/flash2.eff, magic0139's 15 frames 100 ms apart, looping every
# 1500 ms; 0x005B5244 advances it only while protected and 0x005B528F draws
# it over the actor. Its effect point is the actor's sprite point, position
# + (10, 30) (0x005B514E), which is our field point + (2, 9). The track's
# blend byte is 1, which 0x005832E0 turns into GFX.dll flag 0x10: source
# alpha onto destination one, i.e. added light.
FLASH_IMAGE = "object/magic/magic0139.img"
FLASH_AT = (10 - 8, 30 - 21)
FLASH_FRAME_MS, FLASH_MS = 100, 1500
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
# The in-game player list, one 51-pixel row per room seat (Client.exe
# 0x005E1C24 sets it up, 0x005E1E46 draws it), over UI.game. The portrait
# (0x005DCF44, placed by 0x005DD1C3) is the player's avatar at sprite point
# (685, 162) + 51 per seat (role 41: 10 higher); the in-field actor's sprite
# point is its position + (10, 30) (0x005B514E), which puts our 100x100
# canvas at sprite point - (50, 67). The portrait draws a separate part list
# (+0x2CC); that it is only the head is inferred, so the host crops the
# canvas to HEAD_CROP.
PLAYER_LIST_STEP = 51
PLAYER_LIST_PORTRAIT_AT = (685 - 50, 162 - 67)  # canvas top-left for seat 0
PLAYER_LIST_ROLE_RAISE = {41: 10}
HEAD_CROP = pygame.Rect(26, 18, 48, 40)  # head area of the 100x100 avatar canvas
PLAYER_LIST_TEXT_RIGHT = 788  # names and titles end here (x = 788 - 6 per GBK byte)
PLAYER_LIST_NAME_Y = 115
PLAYER_LIST_TITLE_Y = 135
PLAYER_LIST_TEXT = (0xC7, 0xF2, 0xFC)  # over a black copy one pixel down and right
# Title by competitive points (Client.exe table 0x7F5940): the first whose
# bound exceeds the points. The three highest add a gendered rank
# (0x7F5D78 for gender 1, 0x7F5D82 otherwise); past the table it is "???".
PLAYER_TITLES = (
    (10000, "QQ堂平民"), (30000, "糖果爱好者"), (60000, "采购学徒"), (100000, "熬糖工人"), (150000, "拌糖熟练工"),
    (210000, "甜味技术员"), (280000, "捏形师傅"), (360000, "品糖高手"), (450000, "造型专家"), (550000, "QQ糖大师"),
    (700000, "糖果志愿者"), (900000, "奶酪预备兵"), (1150000, "泡泡糖战士"), (1450000, "刨冰骑兵"), (1800000, "棒棒糖游侠"),
    (2200000, "饼干队长"), (2650000, "果冻骑士"), (3150000, "巧克力将军"), (3700000, "冰琪淋勇者"), (4300000, "QQ堂英雄"),
    (5000000, "糖果镇长"), (5900000, "中华城主"), (7100000, "梦幻岛主"), (8700000, "星星公爵"), (10800000, "月亮"),
    (13500000, "太阳"), (25350000, "紫钻"), (63300000, "酷比大天使"), (144100000, "创世之神"),
)
PLAYER_TITLE_RANKS = {24: ("王子", "公主"), 25: ("国王", "女王"), 26: ("皇帝", "女皇")}
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
# Treasure (rule 5) and wrestle (rule 4) scoreboard, Client.exe 0x005DB0B5
# and 0x005E1F3A (rule 4 builds it in slot 10, 0x0060B789): per room seat
# (actor +0x396, which uiGame's ui_NotifyShowLevelIcon passes to
# GetSeatInfo), a badge (frame 1 while that player's team has the highest
# total; all blue before the first update) and the player's value in ally
# or opponent digits: the carried gem value under rule 5, the player's own
# kills under rule 4 (0x0060583E, 0x005ADDE8). The sprite points (636, 110),
# (640, 120) and (633/647, 120) draw (-12, +1) off them (0x005C477E); the
# 2021 wrestle recording BV1WK4y1Q7Nq matches to a pixel.
TREASURE_BADGE = "object/misc/misc350_stand.img"
TREASURE_DIGITS = ("object/misc/misc353_stand.img", "object/misc/misc351_stand.img")  # opponent, ally
TREASURE_BADGE_AT = (636 - 12, 110 + 1)
TREASURE_ONE_DIGIT_AT = (640 - 12, 120 + 1)
TREASURE_TWO_DIGITS_X = (633 - 12, 647 - 12)
# Rule 4's kill streak badge for the local player (0x005DD427, drawn by
# 0x005DD5AE from the actor's position): streak 2..9 as misc(250 + n), 10 and
# more as two digits misc(260 + d), then misc270 连斩, at x + 30 (+ 22 per
# digit), rising from 20 to 40 pixels above over 1000 ms and fading out in
# the last 500 ms. Our field point is the position - (20, 20).
STREAK_MS, STREAK_FADE_MS = 1000, 500
STREAK_X, STREAK_DIGIT_STEP, STREAK_TEXT_GAP = 30, 22, 23
STREAK_TEXT = "object/misc/misc270_stand.img"
TREASURE_STEP = 51
TREASURE_GEM_VALUES = (1, 2, 3)  # scene items 150, 151, 152
# Bun (rule 3). Client.exe builds both houses at the start (0x005B720C, map
# element 8009) and puts each in the grid at its anchor, replacing what the
# map file has there (0x005D839C; map 806 draws houses 8009/8010 itself).
# CMEBunShop (0x006151C6, 0x0061531F) plays default_MapElement.eff with
# misc151 (the first house) or misc152, 4 frames 100 ms apart (the .eff's
# 100 read as the frame time, inferred), at effect point (col*40+8,
# row*40+1): an ordinary element's effect point is its cell*40 - offset +
# (8, 21) (0x005EEE71), so the house sits 1 pixel right of where element
# 8009 would. Then it draws one small bun per bun it holds, 8 pixels apart,
# from sprite point (col*40+20, row*40+20): its own colour's buns with
# anchor (-43, 17), captured ones with (-43, 7) (0x006152BF), so the
# captured row is 10 pixels lower; misc161 is the first house's colour,
# misc162 the other. 0x005C477E draws a sprite at point - anchor + (-12, 1),
# which is our field point + (8, 21) like an effect point. Checked against
# a 2021 recording of map 819 (Bilibili BV1Fh411m7Cx): both houses, both
# rows and the 8-pixel step match to the pixel.
BUN_HOUSES = ("object/misc/misc151_stand.img", "object/misc/misc152_stand.img")  # first house, second
BUN_HOUSE_ELEMENTS = (8009, 8010)
BUN_HOUSE_OFFSET = (-1 + 1, -20)  # element 8009's offset from mapElem.py, 1 pixel right
BUN_HOUSE_FRAME_MS, BUN_HOUSE_MS = 100, 1000  # default_MapElement.eff: 100 ms frames, keys 0..1000
BUN_ICONS = ("object/misc/misc161_stand.img", "object/misc/misc162_stand.img")  # first house's colour, other
BUN_ICON_AT = ((43, -17), (43, -7))  # own row, captured row; from the house's top-left cell
BUN_ICON_STEP = 8
# A loose bun is object/item/item11, drawn like a scene item; a carrier
# wears it as cap 22 (first house's colour) or 23 (0x005AA4EC).
#
# Sculpture (rule 6). A statue (0x005F5D4B, element 12008, built over the
# map file's own statue elements) draws up to three layers from sprite point
# = its cell's centre (0x005F639E): the base misc221 (first statue) or
# misc222 with anchor (11, 47); a stage for progress 1..3 (misc223..225 or
# misc226..228) or misc229 for a complete one, anchor (11, 104); and when
# complete a hero, anchor (12, 150): misc239..242 for the first slot of
# material 13..16, otherwise misc(230 + p) (p < 18) or misc(225 + p), p
# being the completing player's role (inferred). A deposit or a lost piece
# plays the stage's _trigger frames for 2000 ms (0x005F634C; the 100 ms
# frame time is inferred). Loose fragments draw as item{material}
# (0x005F750E) and are hidden under a map element (0x005F75A5); a carrier
# wears cap 101..105 for material 12..16 (0x005AA4EC).
STATUE_ELEMENTS = (12008, 12009)
STATUE_BASES = ("object/misc/misc221", "object/misc/misc222")  # first statue, second
STATUE_STAGES = (("object/misc/misc223", "object/misc/misc224", "object/misc/misc225"),
                 ("object/misc/misc226", "object/misc/misc227", "object/misc/misc228"))
STATUE_DONE = "object/misc/misc229"
STATUE_BASE_ANCHOR, STATUE_STAGE_ANCHOR, STATUE_HERO_ANCHOR = (11, 47), (11, 104), (12, 150)
STATUE_TRIGGER_MS, STATUE_TRIGGER_FRAME_MS = 2000, 100
STATUE_PIECES = 4
FRAGMENT_CAP_BASE = 101 - 12  # cap = material + this
BUN_LOOSE = "object/item/item11_stand.img"
BUN_CAPS = (22, 23)


def effect_frame(elapsed: float, length_ms: int, frame_ms: int, frames: int) -> int:
    """A looping .eff track's frame (0x00582D20, 0x0057CD10): the effect's
    time wraps at its last key, the track's frame at frames * frame_ms."""
    return int(elapsed % length_ms % (frames * frame_ms) // frame_ms)


def flight_position(flight: dict[str, Any], clock: int, width: int, height: int) -> tuple[int, int]:
    """Where a flying bubble's cell corner is (field pixels). The mover
    (0x005A45CE) keeps going past the field edge and draws modulo the field
    (0x005A47E4); a sideways flight rises in an arc a quarter of its length
    high at the middle (0x005A48D7, 0x005A4925)."""
    step_x, step_y = FLIGHT_STEPS[flight["dir"] & 3]
    length = flight["cells"] * CELL
    progress = min(max((clock - flight["start"]) / max(flight["until"] - flight["start"], 1), 0.0), 1.0)
    # 0x005DF724 passes cell centres to the mover. Wrap those centres before
    # applying the arc (0x005A47E4 / 0x005A4983), then return a cell corner.
    # Wrapping the corner instead makes the bubble cross an edge 20 px late.
    dx, dy = step_x * length, step_y * length
    if "to" in flight:
        dx = (flight["to"]["col"] - flight["from"]["col"]) * CELL
        dy = (flight["to"]["row"] - flight["from"]["row"]) * CELL
        if step_x > 0 and dx < 0:
            dx += width
        elif step_x < 0 and dx > 0:
            dx -= width
        if step_y > 0 and dy < 0:
            dy += height
        elif step_y < 0 and dy > 0:
            dy -= height
        length = abs(dx)
    x = flight["from"]["col"] * CELL + CELL // 2 + dx * progress
    y = flight["from"]["row"] * CELL + CELL // 2 + dy * progress
    x = x + width if x < 0 else x - width if x > width else x
    y = y + height if y < 0 else y - height if y > height else y
    arc = -length / 4 * (1 - (2 * progress - 1) ** 2) if step_x else 0
    return int(x) - CELL // 2, int(y) - CELL // 2 + int(arc)


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


def player_title(points: int, gender: int) -> str:
    """The player list's second line for these competitive points."""
    for index, (bound, title) in enumerate(PLAYER_TITLES):
        if points < bound:
            ranks = PLAYER_TITLE_RANKS.get(index)
            return title + (ranks[0 if gender == 1 else 1] if ranks else "")
    return "???"


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
        self._place_bun_houses()
        self._place_statues()
        self._place_tank_bases()
        self._place_boxes()
        self.statue_changed: dict[tuple[int, int], tuple[int, float]] = {}  # by cell: (progress, now_ms of the change)
        self.realtime = realtime
        begin = state.get("begin") or {}
        self.rule = begin.get("rule") or "ordinary"
        self.round_end_ms = begin.get("round_ms") or ROUND_END_MS
        # The native scoreboard keeps its leader flags clear until the first
        # score update.
        self.treasure_scored = False
        # Online: TCP notifications for the sidecar, and where its TCP
        # copies go.
        self.pending_tcp: list[tuple[int, bytes]] = []
        self.on_tcp_out: Callable[[int, bytes], None] | None = None
        # Online: the server's verdict for the banner (net.match.RESULT_*).
        self.result: int | None = None
        self.result_rows: list[ResultRow] = []
        self.result_shown_at = 0.0
        # Online: GAME_OVER has arrived. Client.exe then stops the scene's
        # play update (0x005C874F sets state 2; 0x005C8703 then only runs
        # 0x005C8BE2, the clock), so nothing more is simulated or sent.
        self.over = False
        self.end_logged = False
        self.previous_positions: dict[int, tuple[int, int]] = {}
        self.walk_started: dict[int, int] = {}
        # Wall-clock ms since the scene started; keeps running after the match
        # ends, when the engine clock stops.
        self.now_ms = 0.0
        self.eliminated_at: dict[int, float] = {}  # by now_ms
        self.trapped_at: dict[int, float] = {}  # by now_ms
        self.flash_ms: dict[int, int] = {}  # protection effect time per actor, advanced only while protected
        self.premultiplied: dict[tuple[str, int], pygame.Surface] = {}  # additive frames, colour * alpha
        self.local_streak = 0
        self.streak_badge: tuple[int, float] | None = None  # (streak, now_ms shown)
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
        # Room seats 0..7 by PlayerID (online only).
        self.seats = {player["id"]: player["seat"] for player in players if player.get("seat", -1) >= 0}
        self.titles = {player["id"]: player_title(player.get("points", 0), player.get("gender", 0)) for player in players}
        self.runtime: UIRuntime | None = None
        self.shown_seconds = -1
        self.shown_tank_hp: dict[int, int] = {}
        self.font: Callable[[int], pygame.font.Font] | None = None
        self.text: Callable[[str, int, tuple[int, int, int]], pygame.Surface] | None = None
        log(f"[battle] map {map_id} {self.map_name} ({self.map.width}x{self.map.height}, rule {self.map.rule})")

    # UI.game ------------------------------------------------------------------

    def attach(self, runtime: UIRuntime, map_id: int, font: Callable[[int], pygame.font.Font],
               text: Callable[[str, int, tuple[int, int, int]], pygame.Surface] | None = None) -> None:
        """Prepare UI.game the way the native host does when a match starts."""
        self.runtime, self.font, self.text = runtime, font, text
        # Client.exe (0x005A3C7F) hides the replay recorder and player; it
        # shows the player again only when playing a replay back.
        self._script("showRecordRecorder", 0)
        self._script("showRecordPlayer", 0)
        self._script("ui_SetGameMode", GAME_MODE_NORMAL)
        self._script("ui_InitPVEBoss")  # hides the boss bars when there are none
        self._script("game_setMapID", map_id)
        self._update_timer()
        self._update_tank_bases()

    def _script(self, name: str, *args: Any) -> Any:
        function = self.runtime.namespace.get(name) if self.runtime else None
        return call_script(function, *args) if callable(function) else None

    def _update_timer(self) -> None:
        seconds = max(0, self.round_end_ms - self.clock) // 1000
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
        if self.outcome["ended"] and not self.realtime or self.over:
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
        self._note_statue_changes()
        streak = (self.state.get("streaks") or {}).get(str(self.local_id), 0)
        if self.rule == "wrestle" and streak > self.local_streak and streak >= 2:
            self.streak_badge = (min(streak, 99), self.now_ms)
        self.local_streak = streak
        for actor in list(self.state["actors"]) + list((self.state.get("boss") or {}).get("actors") or ()):
            if actor.get("protected_until", 0) > previous_clock:
                self.flash_ms[actor["id"]] = self.flash_ms.get(actor["id"], 0) + min(self.clock, actor["protected_until"]) - previous_clock
        for event in self.state["events"] or ():
            self._apply(event)
        self._update_timer()
        self._update_status_bar()
        self._update_tank_bases()
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
                if element > 0 and (self.elements.get(element, mapfile.ElementInfo()).life_time > 0 or
                                   self.map.rule == 7 and element in (13011, 13012, 13013, 13014)):
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
        elif kind == EVENT_ACTOR_RESPAWNED:
            for timers in (self.eliminated_at, self.trapped_at, self.walk_started):
                timers.pop(event["player"], None)

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
            image_id = pickup["scene"] + 800 if self.map.rule == 8 and pickup["scene"] in (1, 2, 7) else pickup["scene"]
            self._draw_frames(field, f"object/item/item{image_id}_stand.img", self.clock // ITEM_FRAME_MS,
                              *self._item_at(pickup["scene"], pickup["row"], pickup["col"]))
        for fragment in (self.state.get("sculpture") or {}).get("loose") or ():
            if not fragment["hidden"]:
                self._draw_frames(field, f"object/item/item{fragment['material']}_stand.img", self.clock // ITEM_FRAME_MS,
                                  *self._item_at(fragment["material"], fragment["row"], fragment["col"]))
        for bun in (self.state.get("bun") or {}).get("loose") or ():
            self._draw_frames(field, BUN_LOOSE, self.clock // ITEM_FRAME_MS, *self._item_at(0, bun["row"], bun["col"]))
        for field_object in self.state.get("fields") or ():
            # Banana and glue placed with items 41/42 (scene IDs 42/43).
            self._draw_frames(field, f"object/item/item{field_object['action']}_stand.img", self.clock // ITEM_FRAME_MS,
                              *self._item_at(field_object["action"], field_object["row"], field_object["col"]))
        for flight in (self.state.get("boss") or {}).get("flights") or ():
            # A Boss skill's objects fly from its cell to theirs at 400 px/s
            # (0x005FDB18 -> 0x005D9BBF) and lie there once landed.
            span = max(flight["lands"] - flight["start"], 1)
            done = min(max(self.clock - flight["start"], 0), span) / span
            x0, y0 = self._item_at(flight["scene"], flight["from_row"], flight["from_col"])
            x1, y1 = self._item_at(flight["scene"], flight["row"], flight["col"])
            self._draw_frames(field, f"object/item/item{flight['scene']}_stand.img", self.clock // ITEM_FRAME_MS,
                              int(x0 + (x1 - x0) * done), int(y0 + (y1 - y0) * done))
        kick = self.state.get("kick") or {}
        for fall in kick.get("falls") or ():
            # Rule 2's dropped items fall from row 0 of their column at
            # 400 px/s (0x005D9BBF) and are picked up once they land.
            x, y = self._item_at(fall["scene"], 0, fall["col"])
            drop = min(max(self.clock - fall["start"], 0), fall["until"] - fall["start"]) * FLIGHT_PX_PER_MS
            self._draw_frames(field, f"object/item/item{fall['scene']}_stand.img", self.clock // ITEM_FRAME_MS, x, y + int(drop))
        aim = (self.state.get("machine") or {}).get("aim")
        if aim is not None:
            self._draw_frames(field, "object/misc/misc450_stand.img", self.clock // 100,
                              ox + aim["col"] * CELL, oy + aim["row"] * CELL)
        flights = dict(kick.get("flights") or {})
        flights.update((self.state.get("machine") or {}).get("flights") or {})
        for bomb in self.state["bombs"] or ():
            if bomb.get("hidden"):
                continue
            # Rule 6's style-9 bubble uses that bubble type's image, placed like
            # the others (its size was not checked against a recording).
            image = f"object/bomb/bomb{bomb['style']}_stand.img" if bomb.get("style") else BOMB_IMAGE
            x, y = bomb["col"] * CELL, bomb["row"] * CELL
            flight = flights.get(str(bomb["id"]))
            if flight is not None and flight["start"] <= self.clock < flight["until"]:
                x, y = flight_position(flight, self.clock, self.map.width * CELL, self.map.height * CELL)
            self._draw_frames(field, image, self.clock // 150, ox + x - BOMB_ANCHOR[0], oy + y - BOMB_ANCHOR[1])
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
            self._draw_bun_houses(field, row)
            self._draw_statues(field, row)
            self._draw_tank_bases(field, row)
            self._draw_machine_gates(field, row)
            self._draw_boxes(field, row)
            self._draw_bosses(field, row)
            for actor in sorted(actors_by_row.get(row, ()), key=lambda item: item["y"]):
                self._draw_actor(field, actor)
                if self.map.rule == 8:
                    self._draw_box_health(field, actor)
        self._draw_streak(field)
        self._draw_box_effects(field)
        charge = (self.state.get("machine") or {}).get("charge")
        actor = self._local_actor() if charge is not None else None
        if actor is not None and actor["state"] != ACTOR_ELIMINATED:
            # 005D2BDE / 005D2D02: local-only five-shot charge bar.
            self._draw_frames(field, "object/misc/misc179_stand.img", 0, actor["x"] - 11, actor["y"] - 43)
            frames = self.sprites.get("object/misc/misc181_stand.img")
            if frames and frames[0][0] is not None:
                fill = frames[0][0]
                width = min(38, int((charge["count"] + charge["elapsed"] / max(charge["period"], 1)) * 38 / 5))
                field.blit(fill, (actor["x"] - 10, actor["y"] - 35), (0, 0, width, fill.get_height()))
        for path, position in ((FRAME_TOP, (0, 0)), (FRAME_LEFT, (0, oy))):
            self._draw_frames(screen, path, 0, *position)

    @staticmethod
    def _item_at(scene: int, row: int, col: int) -> tuple[int, int]:
        """Where a scene item's image (before its own offset) goes."""
        ax, ay = ITEM_ANCHORS.get(scene, ITEM_ANCHOR)
        ox, oy = FIELD_ORIGIN
        return ox + col * CELL - ax, oy + row * CELL - ay

    def _place_bun_houses(self) -> None:
        """Put the houses in the grid as 0x005D839C does, replacing the map
        file's own house elements (map 806)."""
        bun = self.state.get("bun")
        for house in (bun or {}).get("houses") or ():
            for row in range(house["row"], min(house["row"] + 3, self.map.height)):
                for col in range(house["col"], min(house["col"] + 3, self.map.width)):
                    if abs(self.map.element(mapfile.LAYER_TOP, row, col)) in BUN_HOUSE_ELEMENTS:
                        self._set(mapfile.LAYER_TOP, row, col, 0)

    def _place_tank_bases(self) -> None:
        for base in (self.state.get("tank") or {}).get("bases", ()):
            for row in range(base["row"], min(base["row"] + 3, self.map.height)):
                for col in range(base["col"], min(base["col"] + 3, self.map.width)):
                    if abs(self.map.element(mapfile.LAYER_TOP, row, col)) == 19031:
                        self._set(mapfile.LAYER_TOP, row, col, 0)

    def _update_tank_bases(self) -> None:
        for base in (self.state.get("tank") or {}).get("bases", ()):
            if self.shown_tank_hp.get(base["team"]) != base["hp"]:
                self._script("ui_UpdateTankBaseLife", base["team"], base["hp"])
                self.shown_tank_hp[base["team"]] = base["hp"]

    def _place_boxes(self) -> None:
        state = self.state.get("box") or {}
        for entry in list(state.get("objects") or ()) + list(state.get("launchers") or ()):
            for layer in (mapfile.LAYER_WALL, mapfile.LAYER_TOP):
                self._set(layer, entry["row"], entry["col"], 0)

    def _draw_boxes(self, field: pygame.Surface, row: int) -> None:
        state = self.state.get("box") or {}
        for box in state.get("objects") or ():
            if box.get("hidden") or box["y"] // CELL != row:
                continue
            action = "die" if box["destroy_at"] else "stand"
            started = box["destroy_at"] - 500 if box["destroy_at"] else 0
            path = mapfile.element_image(box["element"], action)
            info = self.elements.get(box["element"], mapfile.ElementInfo())
            if path:
                self._draw_frames(field, path, max(0, self.clock - started) // 100,
                                  FIELD_ORIGIN[0] + box["x"] - 20 - info.offset[0],
                                  FIELD_ORIGIN[1] + box["y"] - 20 - info.offset[1])
        for corner in state.get("launchers") or ():
            if corner["row"] != row:
                continue
            elapsed = self.clock - corner["fired"]
            if corner["fired"] and 0 <= elapsed < 1300:
                self._draw_frames(field, "object/misc/misc480_stand.img", elapsed // 100,
                                  FIELD_ORIGIN[0] + corner["col"] * CELL - 12,
                                  FIELD_ORIGIN[1] + row * CELL - 12)
            else:
                self._draw_element(field, corner["element"], row, corner["col"])
        for npc in (state.get("npc") or {}).get("actors") or ():
            if npc.get("dead") or npc["y"] // CELL != row:
                continue
            record = {"id": npc["id"], "role": npc["role"], "avatar_role": 0, "team": npc["team"],
                      "x": npc["x"], "y": npc["y"], "facing": npc["facing"],
                      "state": ACTOR_TRAPPED if npc.get("trapped") else 0, "protected_until": 0}
            if npc.get("moving"):
                self.walk_started.setdefault(npc["id"], self.clock)
            else:
                self.walk_started.pop(npc["id"], None)
            self._draw_actor(field, record)

    def _draw_box_effects(self, field: pygame.Surface) -> None:
        for effect in (self.state.get("box") or {}).get("effects") or ():
            if not effect["start"] <= self.clock < effect["until"]:
                continue
            for row in range(max(0, effect["row"] - 1), min(self.map.height, effect["row"] + 2)):
                for col in range(max(0, effect["col"] - 1), min(self.map.width, effect["col"] + 2)):
                    self._draw_frames(field, f"object/flame/flame{effect['style']}_stand.img",
                                      (self.clock - effect["start"]) // 50, FIELD_ORIGIN[0] + col * CELL, FIELD_ORIGIN[1] + row * CELL)

    def _draw_bosses(self, field: pygame.Surface, row: int) -> None:
        """A Boss overlay's NPCs (docs/boss-lifecycle-re.md): the role's cloth
        "birth" for the first 1000 ms (0x005ACBFC), then stand/walk, the HP bar
        over the head (0x005D2835, every client) and the 3 s flash after a
        hit. A dead Boss disappears at once (0x00606606)."""
        for boss in (self.state.get("boss") or {}).get("actors") or ():
            if min(max(boss["y"] // CELL, 0), self.map.height - 1) != row:
                continue
            record = {"id": boss["id"], "role": boss["role"], "avatar_role": 0, "team": 7, "x": boss["x"], "y": boss["y"],
                      "facing": boss["facing"], "state": 0, "protected_until": boss["protected_until"], "durability": boss["hp"]}
            born_for = self.clock - boss["born"]
            cloth = self.avatars.parts(boss["role"], "stand").get("cloth")
            birth = f"object/cloth/cloth{cloth}_birth.img"
            if 0 <= born_for < BOSS_BIRTH_MS and cloth and self.sprites.get(birth):
                frames = len(self.sprites.get(birth) or ()) or 1
                self._draw_frames(field, birth, min(frames - 1, born_for * frames // BOSS_BIRTH_MS),
                                  boss["x"] - AVATAR_ANCHOR[0], boss["y"] - AVATAR_ANCHOR[1])
            else:
                if boss.get("moving"):
                    self.walk_started.setdefault(boss["id"], self.clock)
                else:
                    self.walk_started.pop(boss["id"], None)
                self._draw_actor(field, record)
            # 0x005D2448: misc196's frame is drawn for every non-local actor.
            if boss["hp"] > 0:
                self._draw_frames(field, "object/misc/misc196_stand.img", 0, boss["x"] - 11, boss["y"] - 43)
                number = 190 + boss["hp"] if boss["hp"] <= 5 else 191 + min(boss["hp"], 10)
                self._draw_frames(field, f"object/misc/misc{number}_stand.img", 0, boss["x"] - 10, boss["y"] - 42)

    def _draw_box_health(self, field: pygame.Surface, actor: dict[str, Any]) -> None:
        hp = actor.get("durability", 0)
        if hp <= 0 or actor["state"] == ACTOR_ELIMINATED:
            return
        if actor["id"] != self.local_id:
            self._draw_frames(field, "object/misc/misc196_stand.img", 0, actor["x"] - 11, actor["y"] - 43)
        number = 190 + hp if hp <= 5 else 191 + min(hp, 10)
        self._draw_frames(field, f"object/misc/misc{number}_stand.img", 0, actor["x"] - 10, actor["y"] - 42)

    def _draw_machine_gates(self, field: pygame.Surface, row: int) -> None:
        # 0x005EF91F locates machine images one column left, two rows up.
        for gate in (self.state.get("machine") or {}).get("gates") or ():
            if gate["row"] != row or gate.get("destroyed"):
                continue
            first = gate["first"]
            elapsed = self.clock - gate["fired"]
            firing = gate["fired"] > 0 and 0 <= elapsed < 800
            number = (167 if first else 168) if firing else (401 if first else 402) if gate["ready"] else (163 if first else 164)
            frame = elapsed * 3 // 100 if firing else self.clock // 100
            x = FIELD_ORIGIN[0] + (gate["col"] - 1) * CELL
            y = FIELD_ORIGIN[1] + (gate["row"] - 2) * CELL
            self._draw_frames(field, f"object/misc/misc{number}_stand.img", frame, x, y)
            local = self._local_actor()
            if gate["ready"] and local is not None and local["team"] == gate["team"]:
                arrow_x = FIELD_ORIGIN[0] + gate["col"] * CELL + (-20 if first else 60)
                self._draw_frames(field, "object/misc/misc400_stand.img", self.clock // 100, arrow_x, y)

    def _draw_machine_durability(self, screen: pygame.Surface) -> None:
        # 0x005E2093 / 0x005E1E46: no badge, local-team misc460 / other misc461.
        local = self._local_actor()
        for index, actor in enumerate(self.state["actors"]):
            same = local is not None and actor["team"] == local["team"]
            self._draw_frames(screen, f"object/misc/misc{460 if same else 461}_stand.img",
                              actor.get("durability", 4), 628, 121 + 51 * self._seat(index, actor))

    def _draw_tank_bases(self, field: pygame.Surface, row: int) -> None:
        # 005F64E5 / 005F65F0: first base misc623..626, second 627..630;
        # sprite point = anchor cell centre, image anchor (31,47).
        for index, base in enumerate((self.state.get("tank") or {}).get("bases", ())):
            if base["row"] != row:
                continue
            hp = base["hp"]
            stage = 3 if hp == 0 else 2 if hp <= 40 else 1 if hp <= 70 else 0
            path = f"object/misc/misc{623 + index * 4 + stage}_stand.img"
            self._draw_frames(field, path, self.clock // 100,
                              FIELD_ORIGIN[0] + base["col"] * CELL - 31,
                              FIELD_ORIGIN[1] + base["row"] * CELL - 47)

    def _draw_streak(self, field: pygame.Surface) -> None:
        """The local player's N 连斩 badge (0x005DD4E6, 0x005DD5AE)."""
        if self.streak_badge is None:
            return
        streak, shown_at = self.streak_badge
        left = STREAK_MS - (self.now_ms - shown_at)
        actor = self._local_actor()
        if left < 0 or actor is None:
            self.streak_badge = None
            return
        if streak < 10:
            parts = [f"object/misc/misc{250 + streak}_stand.img"]
        else:
            parts = [f"object/misc/misc{260 + digit}_stand.img" for digit in divmod(streak, 10)]
        parts.append(STREAK_TEXT)
        alpha = 255 if left >= STREAK_FADE_MS else max(0, int(255 - (STREAK_FADE_MS - left) ** 2 / 1000))
        ox, oy = FIELD_ORIGIN
        y = oy + actor["y"] - (40 - left / 50) - 20
        x = ox + actor["x"] + STREAK_X - 20
        for index, path in enumerate(parts):
            frames = self.sprites.get(path)
            if not frames or frames[0][0] is None:
                continue
            image, offset_x, offset_y = frames[0]
            if alpha < 255:
                image = image.copy()
                image.set_alpha(alpha)
            dx = index * STREAK_DIGIT_STEP if path != STREAK_TEXT else (len(parts) - 1) * STREAK_DIGIT_STEP + 1
            field.blit(image, (x + dx + offset_x, int(y) + offset_y))

    def _place_statues(self) -> None:
        """Statues replace the map file's own statue elements (0x005D839C)."""
        for statue in (self.state.get("sculpture") or {}).get("statues") or ():
            for layer in range(mapfile.LAYERS):
                if abs(self.map.element(layer, statue["row"], statue["col"])) in STATUE_ELEMENTS:
                    self._set(layer, statue["row"], statue["col"], 0)

    def _note_statue_changes(self) -> None:
        for statue in (self.state.get("sculpture") or {}).get("statues") or ():
            cell = (statue["row"], statue["col"])
            before = self.statue_changed.get(cell)
            if before is None:
                self.statue_changed[cell] = (statue["progress"], -STATUE_TRIGGER_MS)
            elif before[0] != statue["progress"]:
                self.statue_changed[cell] = (statue["progress"], self.now_ms)

    def _statue_hero(self, statue: dict[str, Any]) -> str:
        for material in statue["slots"]:
            if 13 <= material <= 16:
                return f"object/misc/misc{239 + material - 13}"
        actor = next((actor for actor in self.state["actors"] if actor["id"] == statue["completed_by"]), None)
        role = (actor or {}).get("role", 0)
        return f"object/misc/misc{230 + role if role < 18 else 225 + role}"

    def _draw_statues(self, field: pygame.Surface, row: int) -> None:
        """The statues standing on this row (0x005F639E)."""
        ox, oy = FIELD_ORIGIN
        for statue in (self.state.get("sculpture") or {}).get("statues") or ():
            if statue["row"] != row:
                continue
            x, y = ox + statue["col"] * CELL + CELL // 2 - 12 - 8, oy + statue["row"] * CELL + CELL // 2 + 1 - 21  # sprite point -> field
            colour = 0 if statue["first"] else 1
            layers = [(STATUE_BASES[colour], STATUE_BASE_ANCHOR)]
            progress = statue["progress"]
            if progress >= STATUE_PIECES:
                layers += [(STATUE_DONE, STATUE_STAGE_ANCHOR), (self._statue_hero(statue), STATUE_HERO_ANCHOR)]
            elif progress > 0:
                layers.append((STATUE_STAGES[colour][progress - 1], STATUE_STAGE_ANCHOR))
            changed_at = self.statue_changed.get((statue["row"], statue["col"]), (0, -STATUE_TRIGGER_MS))[1]
            for index, (path, (ax, ay)) in enumerate(layers):
                if index == 1 and self.now_ms - changed_at < STATUE_TRIGGER_MS and self.sprites.get(path + "_trigger.img"):
                    self._draw_frames(field, path + "_trigger.img", int((self.now_ms - changed_at) // STATUE_TRIGGER_FRAME_MS), x - ax, y - ay)
                else:
                    self._draw_frames(field, path + "_stand.img", 0, x - ax, y - ay)

    def _draw_bun_houses(self, field: pygame.Surface, row: int) -> None:
        """The houses anchored on this row, each with its row of small buns."""
        bun = self.state.get("bun")
        if not bun:
            return
        ox, oy = FIELD_ORIGIN
        for house in bun.get("houses") or ():
            if house["row"] != row:
                continue
            first = house["team"] == bun["first"]
            x, y = ox + house["col"] * CELL, oy + house["row"] * CELL
            self._draw_frames(field, BUN_HOUSES[0 if first else 1], effect_frame(self.now_ms, BUN_HOUSE_MS, BUN_HOUSE_FRAME_MS, 4),
                              x + BUN_HOUSE_OFFSET[0], y + BUN_HOUSE_OFFSET[1])
            own, other = (BUN_ICONS[0], BUN_ICONS[1]) if first else (BUN_ICONS[1], BUN_ICONS[0])
            for path, count, (dx, dy) in ((own, house["own"], BUN_ICON_AT[0]), (other, house["captured"], BUN_ICON_AT[1])):
                for index in range(count):
                    self._draw_frames(field, path, 0, x + dx + index * BUN_ICON_STEP, y + dy)

    def _wear(self, actor: dict[str, Any]) -> tuple[tuple[str, int], ...]:
        """Worn costume parts; a bun or fragment carrier's cap is what it carries."""
        wear = self.wear.get(actor["id"], ())
        bun, sculpture = self.state.get("bun"), self.state.get("sculpture")
        if bun:
            carried = bun.get("carried", {}).get(str(actor["id"]), -1)
            cap = BUN_CAPS[0 if carried == bun["first"] else 1] if carried >= 0 else 0
        elif sculpture:
            carried = sculpture.get("carried", {}).get(str(actor["id"]), -1)
            cap = carried + FRAGMENT_CAP_BASE if carried >= 0 else 0
        else:
            cap = 0
        if not cap:
            return wear
        return tuple(part for part in wear if part[0] != "cap") + (("cap", cap),)

    def _seat(self, index: int, actor: dict[str, Any]) -> int:
        """The player's room seat, or its list position offline."""
        return self.seats.get(actor["id"], index)

    def _draw_player_list(self, screen: pygame.Surface) -> None:
        for index, actor in enumerate(self.state["actors"]):
            top = self._seat(index, actor) * PLAYER_LIST_STEP
            image = self.avatars.get(actor["role"], actor["team"] - 1, "stand", SPRITE_DIRECTION[MOVE_DOWN], wear=self.wear.get(actor["id"], ()))
            if image is not None:
                raise_by = PLAYER_LIST_ROLE_RAISE.get(actor["role"], 0)
                screen.blit(image.subsurface(HEAD_CROP), (PLAYER_LIST_PORTRAIT_AT[0] + HEAD_CROP.x,
                                                          PLAYER_LIST_PORTRAIT_AT[1] + HEAD_CROP.y + top - raise_by))
            if self.text is None:
                continue
            for line, y in ((self.names.get(actor["id"], ""), PLAYER_LIST_NAME_Y), (self.titles.get(actor["id"], ""), PLAYER_LIST_TITLE_Y)):
                if not line:
                    continue
                x = PLAYER_LIST_TEXT_RIGHT - cell_width(line, 12)
                screen.blit(self.text(line, 12, (0, 0, 0)), (x + 1, y + top + 1))
                screen.blit(self.text(line, 12, PLAYER_LIST_TEXT), (x, y + top))

    def _draw_frames(self, surface: pygame.Surface, path: str, frame: int, x: int, y: int, additive: bool = False) -> None:
        """additive: GFX.dll blend flag 0x10, colour * alpha added to the screen."""
        frames = self.sprites.get(path)
        if not frames:
            return
        image, offset_x, offset_y = frames[frame % len(frames)]
        if image is None:
            return
        if additive:
            key = (path, frame % len(frames))
            if key not in self.premultiplied:
                self.premultiplied[key] = image.convert_alpha().premul_alpha()
            surface.blit(self.premultiplied[key], (x + offset_x, y + offset_y), special_flags=pygame.BLEND_RGB_ADD)
        else:
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
        image = 10 if flame.get("style") in (13, 14) else flame_image(self.explosions.get(flame["bomb"]), row, col)
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
            image = self.avatars.get(role, actor["team"] - 1, "die", direction, int(dead_for // (DIE_MS // 4)), wear=self._wear(actor))
        elif actor["state"] == ACTOR_TRAPPED:
            image = self.avatars.get(role, actor["team"] - 1, "trigger", direction, int(trapped_for // TRAPPED_FRAME_MS), wear=self._wear(actor))
        elif actor["id"] in self.walk_started:
            frame = (self.clock - self.walk_started[actor["id"]]) // WALK_FRAME_MS
            image = self.avatars.get(role, actor["team"] - 1, "walk", direction, frame, wear=self._wear(actor))
        else:
            image = self.avatars.get(role, actor["team"] - 1, "stand", direction, wear=self._wear(actor))
        ox, oy = FIELD_ORIGIN
        x, y = ox + actor["x"], oy + actor["y"]
        if image is not None:
            if actor["state"] == ACTOR_ELIMINATED:
                image = image.copy()
                image.set_alpha(int(255 * (1 - dead_for / DIE_MS)))
            surface.blit(image, (x - AVATAR_ANCHOR[0], y - AVATAR_ANCHOR[1]))
        if actor["state"] == ACTOR_TRAPPED:
            self._draw_syrup(surface, trapped_for, x - AVATAR_ANCHOR[0], y - AVATAR_ANCHOR[1])
        self._draw_flash(surface, actor, x, y)

    def _draw_flash(self, surface: pygame.Surface, actor: dict[str, Any], x: int, y: int) -> None:
        """The harm-protection bubble over an actor at field point (x, y)."""
        if actor.get("protected_until", 0) <= self.clock:
            return
        frames = self.sprites.get(FLASH_IMAGE) or ()
        self._draw_frames(surface, FLASH_IMAGE, effect_frame(self.flash_ms.get(actor["id"], 0), FLASH_MS, FLASH_FRAME_MS, len(frames) or 1),
                          x + FLASH_AT[0], y + FLASH_AT[1], additive=True)

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

    def _draw_treasure(self, screen: pygame.Surface) -> None:
        """Each player's carried gem value, in the row of the player's room
        seat; a player whose seat is unknown uses its list position."""
        actors = self.state["actors"]
        if self.rule == "wrestle":
            kills = self.state.get("kills") or {}
            values = [kills.get(str(actor["id"]), 0) for actor in actors]
        else:
            values = [sum(count * value for count, value in zip(actor.get("carried") or (), TREASURE_GEM_VALUES)) for actor in actors]
        teams: dict[int, int] = {}
        for actor, value in zip(actors, values):
            teams[actor["team"]] = teams.get(actor["team"], 0) + value
        self.treasure_scored = self.treasure_scored or any(values) or (self.rule == "treasure" and any(actor["state"] == ACTOR_ELIMINATED for actor in actors))
        best = max(teams.values(), default=0)
        local = self._local_actor()
        for index, (actor, value) in enumerate(zip(actors, values)):
            top = self._seat(index, actor) * TREASURE_STEP
            leading = self.treasure_scored and teams[actor["team"]] == best
            self._draw_frames(screen, TREASURE_BADGE, int(leading), TREASURE_BADGE_AT[0], TREASURE_BADGE_AT[1] + top)
            digits = TREASURE_DIGITS[local is not None and actor["team"] == local["team"]]
            y = TREASURE_ONE_DIGIT_AT[1] + top
            if 0 <= value < 10:
                self._draw_frames(screen, digits, value, TREASURE_ONE_DIGIT_AT[0], y)
            elif value < 100:
                for x, digit in zip(TREASURE_TWO_DIGITS_X, divmod(value, 10)):
                    self._draw_frames(screen, digits, digit, x, y)

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
        """Host-drawn parts above UI.game: the selected item slot, the player
        list and the result banner."""
        if self.font is None:
            return
        if self._slots()[self.selected_slot][0]:
            slot = pygame.Rect(SLOT_ORIGIN[0] + self.selected_slot * SLOT_STEP, SLOT_ORIGIN[1], SLOT_SIZE, SLOT_SIZE)
            pygame.draw.rect(screen, (255, 230, 80), slot, 2)
        self._draw_player_list(screen)
        if self.rule in ("treasure", "wrestle"):
            self._draw_treasure(screen)
        if self.map.rule == 7:
            self._draw_machine_durability(screen)
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
