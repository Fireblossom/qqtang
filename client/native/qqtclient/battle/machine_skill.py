"""Rule 7's charged skills (招式): input and poses. See docs/wrestle-skills-re.md.

In Client.exe only rule 7 (机械世界, maps 1301..1311) has them: the battle
input poll 0x005A4C52 feeds the combo recognizer only when the scene's rule
is 7, the sender 0x006184BA is registered by rule 7's setup 0x0060A28C, and
the poses are updated and drawn only in rule 7 (0x005AC065, 0x005AC6A2).

Hold the bubble key. After 900 ms the player shows the charging pose
(type 8, sent once). At 2000 ms the recognizer arms; a direction sequence
from COMBOS, each key within 300 ms of the previous one, picks a skill,
which is sent at once if it comes before 3000 ms. Direction keys pressed
while the bubble key is held do not move the player. The player must face
the other half: the first team right, the other team left.

The Go sidecar (cmd/qqt-battle-sidecar/machine_skill.go) builds the 0x115C,
arbitrates it and applies 0x115B; its notices drive the poses here.
"""

from __future__ import annotations

import dataclasses

from .animation import FrameLoop
from .sidecar import MOVE_DOWN, MOVE_LEFT, MOVE_NONE, MOVE_RIGHT, MOVE_UP

# The recognizer's key numbers are the keyboard-layout slots 1..5 (0x005A536D).
KEY_LEFT, KEY_RIGHT, KEY_UP, KEY_DOWN, KEY_BUBBLE = 1, 2, 3, 4, 5
KEY_OF_MOVE = {MOVE_LEFT: KEY_LEFT, MOVE_RIGHT: KEY_RIGHT, MOVE_UP: KEY_UP, MOVE_DOWN: KEY_DOWN}
# 0x007A23D4 (six shorts per entry) and the lengths at 0x007A244C. The
# entries come in mirrored pairs with the same effect (0x005FF2B8).
COMBOS = (
    (KEY_DOWN, KEY_LEFT, KEY_DOWN, KEY_LEFT),    # 0 凤舞
    (KEY_DOWN, KEY_RIGHT, KEY_DOWN, KEY_RIGHT),  # 1 凤舞
    (KEY_RIGHT, KEY_LEFT),                       # 2 长龙
    (KEY_LEFT, KEY_RIGHT),                       # 3 长龙
    (KEY_LEFT, KEY_DOWN, KEY_RIGHT),             # 4 天牢
    (KEY_RIGHT, KEY_DOWN, KEY_LEFT),             # 5 天牢
    (KEY_RIGHT, KEY_RIGHT),                      # 6 暴踢/闪避
    (KEY_LEFT, KEY_LEFT),                        # 7 暴踢/闪避
)
KEY_GAP_MS = 300  # 0x005CAE1D
MAX_KEYS = 10  # the sequence buffer; an 11th key would overwrite the candidate flags
ARM_AFTER_MS, ARM_BEFORE_MS = 2000, 2050  # 0x005A4C52: the recognizer arms in (2000, 2050)
CHARGE_QUIET_MS, CHARGE_SHOW_MS, CHARGE_READY_MS, CHARGE_LIMIT_MS = 500, 900, 1700, 3000  # 0x005A53DE
SKILL_NONE, SKILL_CHARGE, SKILL_REFUSED = -1, 8, 9
RESEND_MS = 1000  # 0x006184BA keeps an unchanged value quiet this long


class ComboRecognizer:
    """The object at input +0x184 (0x005CAE12..0x005CAFEC)."""

    def __init__(self) -> None:
        self.up = [True] * 6  # +0x6C..+0x71: key released, by key number
        self.skill = SKILL_NONE  # +0x74
        self.keys: list[tuple[int, int]] = []  # (key, time)
        self.candidates = [True] * len(COMBOS)
        self.reset()

    def clear(self) -> None:
        """0x005CAED0: forget the sequence; direction keys count as released."""
        self.keys = []
        for key in (KEY_LEFT, KEY_RIGHT, KEY_UP, KEY_DOWN):
            self.up[key] = True
        self.candidates = [True] * len(COMBOS)

    def reset(self) -> None:
        """0x005CAFEC."""
        self.skill = SKILL_NONE
        self.up[KEY_BUBBLE] = True
        self.clear()

    def release(self, key: int) -> None:
        """0x005CAFC2."""
        self.up[key] = True

    def press(self, key: int, now: int) -> None:
        """0x005CAF0B: a key that is down; only its first poll counts."""
        if self.skill != SKILL_NONE:
            return
        if key == KEY_BUBBLE:
            if self.up[KEY_BUBBLE]:
                self.reset()
                self.up[KEY_BUBBLE] = False  # armed: directions are recorded
            return
        if not self.up[key]:
            return
        self.up[key] = False
        if self.up[KEY_BUBBLE] or len(self.keys) >= MAX_KEYS:
            return
        self.keys.append((key, now))
        if not self._match() or self.skill != SKILL_NONE:
            self.clear()

    def _match(self) -> bool:
        """0x005CAE1D."""
        index = len(self.keys) - 1
        if index > 0 and self.keys[index][1] - self.keys[index - 1][1] > KEY_GAP_MS:
            self.clear()
            return False
        key = self.keys[index][0]
        for number, combo in enumerate(COMBOS):
            if not self.candidates[number]:
                continue
            if index < len(combo) and combo[index] == key:
                if len(combo) == index + 1:
                    self.clear()
                    self.release(KEY_BUBBLE)
                    self.skill = number
                    break
            else:
                self.candidates[number] = False
        # Two never-cleared flags past the eight entries keep this true.
        return True


class SkillInput:
    """The skill parts of the battle input poll 0x005A4C52, the charge
    classification 0x005A53DE and the sender 0x006184BA, for the local
    player."""

    def __init__(self) -> None:
        self.recognizer = ComboRecognizer()
        self.charge_start = 0  # input +0x180
        self.last_value = SKILL_NONE  # sender +0x10
        self.last_at = 0  # sender +0x0C
        self.charge_sent = False  # sender +0x08

    def poll(self, now: int, bubble_held: bool, keys_held: set[int]) -> None:
        if not bubble_held:
            self.charge_start = now
            self.recognizer.release(KEY_BUBBLE)
        elif ARM_AFTER_MS < now - self.charge_start < ARM_BEFORE_MS:
            self.recognizer.press(KEY_BUBBLE, now)
        for key in (KEY_LEFT, KEY_RIGHT, KEY_UP, KEY_DOWN):
            if key not in keys_held:
                self.recognizer.release(key)
            elif bubble_held:
                self.recognizer.press(key, now)

    def value(self, now: int) -> int:
        held = now - self.charge_start
        if held < CHARGE_QUIET_MS:
            return SKILL_NONE
        if held < CHARGE_READY_MS:
            return SKILL_CHARGE if held > CHARGE_SHOW_MS else SKILL_NONE
        if held < CHARGE_LIMIT_MS:
            return self.recognizer.skill
        self.recognizer.reset()
        return SKILL_REFUSED

    def send(self, now: int, facing: int, first_team: bool) -> int | None:
        """The skill type to request now (0..8), if any."""
        value = self.value(now)
        if (now - self.last_at < RESEND_MS and value == self.last_value) or (self.last_value == SKILL_NONE and value == SKILL_NONE):
            self.last_at, self.last_value = now, value
            return None
        self.last_value, self.last_at = value, now
        if facing != (MOVE_RIGHT if first_team else MOVE_LEFT):
            self.recognizer.reset()
            return None
        if value >= 0:
            if value == SKILL_CHARGE:
                if self.charge_sent:
                    return None
                self.charge_sent = True
            if value != SKILL_REFUSED:
                return value
        self.charge_sent = False
        return None

    def notice(self) -> None:
        """The local player's 0x115B arrived (0x005FF2B8 resets the input)."""
        self.recognizer.reset()


def held_move_keys(moves: set[int]) -> set[int]:
    return {KEY_OF_MOVE[move] for move in moves if move in KEY_OF_MOVE}


# Poses ---------------------------------------------------------------------
#
# 0x005AF5EB loads six scene pictures per player: 0x0701D601..0x0701DB01,
# i.e. misc470..475 (stand). 0x005AF5A7 picks one by pose: 0/1 凤舞 471,
# 2/3 長龍 472, 4/5 天牢 473, 6 暴踢 474, 7 閃避 475, 8 charging 470.
# 0x005AE26F restarts it (frame 0, then 100 ms of picture time,
# 0x005C4817), 0x005AC065 advances it by 1.5 x the scene time and clears it
# after 1200 ms (charging 1750 ms). Its SetPos is the player position
# - (0, 65); the charging fire sits at + (5, -50) facing right and
# - (45, 50) facing left, and disappears when the player moves facing up or
# down (0x005AD322). With 0x005C477E's (-12, 1) and the field's (-8, -21)
# the picture goes to the SetPos point - (20, 20).
POSE_IMAGES = {0: 471, 1: 471, 2: 472, 3: 472, 4: 473, 5: 473, 6: 474, 7: 475, SKILL_CHARGE: 470}
POSE_MS, CHARGE_POSE_MS = 1200, 1750
POSE_START_MS = 100
POSE_SPEED = 1.5
POSE_AT = (-20, -65 - 20)
CHARGE_AT = {MOVE_RIGHT: (5 - 20, -50 - 20), MOVE_LEFT: (-45 - 20, -50 - 20)}


# 0x005FF2B8's effects play these through 0x005A0DBA -> 0x00530850
# ("sound\\" + name, once), the PlaySound path, and only when the player is
# ours (0x00534990): the charge (0x005FF3B2) x26_01.wav; throws
# (0x005FF40C/0x005FF58E/0x005FF712) x27_01.wav before the arbitrator
# check; kick or dodge (0x005FF896) x27_01.wav only when pose 6 or 7 is
# set, so a dodge that cannot move is silent. The pose decides it all.
CHARGE_SOUND, SKILL_SOUND = "x26_01.wav", "x27_01.wav"


def pose_sound(pose: int) -> str | None:
    if pose == SKILL_CHARGE:
        return CHARGE_SOUND
    if 0 <= pose < SKILL_CHARGE:
        return SKILL_SOUND
    return None


def pose_image(pose: int) -> str:
    return f"object/misc/misc{POSE_IMAGES[pose]}_stand.img"


@dataclasses.dataclass
class SkillPose:
    pose: int
    started: int
    picture: FrameLoop

    @classmethod
    def start(cls, pose: int, clock: int, frames: int) -> "SkillPose":
        picture = FrameLoop(max(1, frames))
        picture.advance(POSE_START_MS)
        return cls(pose, clock, picture)

    def advance(self, clock: int, milliseconds: int) -> bool:
        """False once the pose is over."""
        self.picture.advance(int(milliseconds * POSE_SPEED))
        return clock - self.started <= (CHARGE_POSE_MS if self.pose == SKILL_CHARGE else POSE_MS)

    def offset(self, facing: int) -> tuple[int, int] | None:
        if self.pose != SKILL_CHARGE:
            return POSE_AT
        return CHARGE_AT.get(facing)


def vertical(facing: int) -> bool:
    return facing in (MOVE_UP, MOVE_DOWN, MOVE_NONE)
