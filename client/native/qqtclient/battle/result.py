"""v848 result controller: 00612F3B / 00613560 / 006137BB.

Image positions are the native host's screen positions, before DIMG offsets.
The scene paints the plan and the original UI paints its level-icon widgets.
"""

import bisect
import dataclasses
import struct


def f32(value):
    return struct.unpack("<f", struct.pack("<f", value))[0]


@dataclasses.dataclass(frozen=True)
class Image:
    number: int
    frame: int
    x: int
    y: int
    alpha: int = 255
    clip_width: float | None = None  # raw frame pixels, ignoring DIMG offsets


@dataclasses.dataclass(frozen=True)
class Text:
    text: str
    x: int
    y: int
    alpha: int


def digits(number, sheet, right, y, spacing, alpha):
    # 00612399 takes a signed integer; negative values produce no digits.
    if number < 0 or number >= 0x80000000:
        return []
    value = str(number)
    left = right - (len(value) - 1) * spacing
    return [Image(sheet, int(char), left + index * spacing, y, alpha) for index, char in enumerate(value)]


def result_points(rule, before, scores):
    # 00602BAB: treasure's fourth score is displayed but excluded from this
    # total. This is distinct from the GAME_OVER Point used by the XP bar.
    return (before + sum(score for index, score in enumerate(scores)
                         if rule != 5 or index != 3)) & 0xFFFFFFFF


def experience_level(points, thresholds):
    # Native 007F7780 uses an inclusive upper threshold; the UI Level script
    # uses a strict comparison, so passing the original points differs at a
    # boundary. The native row sends thresholds[level]-1 to that script.
    return min(max(1, bisect.bisect_left(thresholds, points)), len(thresholds) - 1)


def experience_percent(points, thresholds):
    level = experience_level(points, thresholds)
    low, high = thresholds[level - 1:level + 1]
    return f32(min(100, max(0, (points - low) * 100 / (high - low))))


def pve_experience_percent(points, thresholds):
    # 00602F9B chooses the interval with low <= points < high. At the
    # final level it finds no next interval and returns zero.
    level = bisect.bisect_right(thresholds, points)
    if level == 0 or level >= len(thresholds):
        return 0.0
    low, high = thresholds[level - 1:level + 1]
    return f32((points - low) * 100 / (high - low))


class ResultDisplay:
    def __init__(self, rule, rows, local_id, names, thresholds=(), progress=None, pve_thresholds=()):
        self.rule, self.rows, self.local_id, self.names = rule, rows, local_id, names
        self.base = 130 if rule == 12 else 30
        self.elapsed = 0
        self.phase = 0
        self.wait = 3000
        self.y = f32(0)
        self.table_at = (self.base - 12, -514)
        self.alpha = 0
        self.portrait = None
        self.thresholds = thresholds
        mine = next((row for row in rows if row.player_id == local_id), None)
        if progress is None and rule != 12 and thresholds and mine is not None and mine.points_before is not None:
            progress = (experience_percent(mine.points_before, thresholds),
                        experience_percent((mine.points_before + mine.point) & 0xFFFFFFFF, thresholds))
        if progress is None and rule == 12 and pve_thresholds and mine is not None and mine.pve_points_before is not None:
            progress = (pve_experience_percent(mine.pve_points_before, pve_thresholds),
                        pve_experience_percent(result_points(rule, mine.pve_points_before, mine.scores), pve_thresholds))
        self.progress = list(map(f32, progress)) if progress is not None else None

    def advance(self, milliseconds):
        self.elapsed += milliseconds
        if self.portrait is not None:
            self.portrait.advance(milliseconds)
        if self.phase == 0:
            if self.wait > 0:
                self.wait -= milliseconds
            else:
                self.phase = 1
        elif self.phase == 1:
            step = f32(700 * milliseconds * 0.001)
            self.y = f32(self.y + step)
            # Native 005A467D snaps when the remaining distance is shorter
            # than one more step, rather than waiting to cross the target.
            if abs(505 - self.y) < step:
                self.y = f32(505)
                self.phase = 3
            self.table_at = (self.base + 13, int(self.y) - 431)
        elif self.phase == 3:
            if self.progress is not None and self.progress[0] < self.progress[1]:
                self.progress[0] = min(self.progress[1], f32(self.progress[0] + milliseconds * self.progress[1] * 0.001))
            if self.alpha < 255:
                self.alpha = min(255, self.alpha + max(1, milliseconds >> 1))

    def plan(self):
        b, a = self.base, self.alpha
        images = [Image(290 if self.rule in (7, 8) else 300, 0, *self.table_at)]
        texts = []
        if self.phase != 3:
            return images, texts
        mine = next((row for row in self.rows if row.player_id == self.local_id), None)
        if mine is not None:
            texts.append(Text(self.names.get(self.local_id, ""), b + 215, 95, a))
            counts = (mine.kills, mine.rescues, mine.reward, mine.mode_count)
            scores = (mine.scores + (0, 0, 0, 0))[:4]
            labels = [(319 if self.rule in (7, 8) else 303, 307),
                      (320 if self.rule in (7, 8) else 302, 307), (306, 307)]
            mode_label = {3: (305, 307), 5: (304, 318), 8: (354, 307)}.get(self.rule)
            if mode_label is not None:
                labels.append(mode_label)
            for index, (label, score_label) in enumerate(labels):
                x = b + (145 if index % 2 == 0 else 335)
                y = 136 if index < 2 else 174
                images += [Image(label, 0, x, y, a), Image(score_label, 0, x + 105, y, a)]
                images += digits(counts[index], 309, x + 84, y - 4, 15, a)
                images += digits(scores[index], 310, x + 165, y + 4, 6, a)
        if self.progress is not None:
            images += [Image(462, 0, b + 138, 205, clip_width=f32(self.progress[1] * 3.83)),
                       Image(317, 0, b + 138, 205, clip_width=f32(self.progress[0] * 3.83))]
        for index, row in enumerate(self.rows[:8]):
            y = 255 + index * 27
            special = self.rule == 8
            images.append(Image({2: 314, 0: 315}.get(row.result, 316), 0, b + (91 if special else 73), y - 2, a))
            texts.append(Text(self.names.get(row.player_id, ""), b + (163 if special else 136), y + 2, a))
            images += digits(row.kills, 301, b + (256 if special else 221), y - 1, 10, a)
            images += digits(row.rescues, 301, b + (322 if special else 282), y - 1, 10, a)
            if row.identity & 0x2000:
                images.append(Image(2000, 0, b + 446, y - 9, a))
            elif 0 < row.remark < 0x80000000:
                images.append(Image((1000 + row.remark) & 0xFFFF, 0, b + 446, y - 4, a))
        if self.portrait is not None:
            images.append(Image(131, 0, *self.portrait.shadow_position))
        if mine is not None:
            if mine.identity & 0x2000:
                images.append(Image(2000, 0, b + 448, 91, a))
            elif 0 < mine.remark < 0x80000000:
                images.append(Image((1000 + mine.remark) & 0xFFFF, 0, b + 443, 91, a))
            images.append(Image({2: 311, 0: 312}.get(mine.result, 313), 0, b + 198, 6))
        images.append(Image(322 if self.rule == 12 else 321, 0, b + 41, 196))
        return images, texts

    def level_icons(self):
        if self.phase != 3:
            return []
        icons = [(self.base + (395 if self.rule == 8 else 364), 250 + index * 27,
                  self.thresholds[experience_level(row.level_points, self.thresholds)] - 1
                  if self.thresholds else row.level_points, index, self.alpha / 255)
                 for index, row in enumerate(self.rows[:8])]
        mine = next((row for row in self.rows if row.player_id == self.local_id), None)
        if mine is not None and self.alpha > 0:
            icons.append((200, 89, mine.level_points, 8, 1.0))
        return icons
