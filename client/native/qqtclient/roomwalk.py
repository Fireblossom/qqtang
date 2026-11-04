"""Walking avatars and decorative bubbles in the room scene
(docs/chatroom-move-re.md). Client.exe's room scene (0x00457080, per frame
0x00457740) does this in every room: arrows walk the local avatar at 0.26
px/ms inside x 41..481, y 106..466, Space puts a harmless bubble that
bursts after 3000 ms, F8 flips the name tags (shown by default only in chat
rooms). Moves and bubbles go out in the TCP 0x0065 room-fast packet and
come back from the server as QQTPPP Type-2 datagrams, which run the
scripts' remotePlayerMove / remotePlayerPutBomb as the original does.
"""

from __future__ import annotations

import random
import socket
import time
from typing import Any, Callable

import pygame

from .battle.effect import EffectPlayer
from .net import roompeer
from .runtime import TEXT_INPUT_TYPES, UIRuntime
from .script import call_script, log
from .session import Session

SPEED = 0.26  # px/ms, 0x3E851EB8
BOUNDS = (41, 106, 481, 466)  # minX, minY, maxX, maxY (0x0045710F)
SEND_EVERY_MS = 300  # 0x00457CF0
BOMB_FUSE_MS = 3000  # 0x00429030
FLAME_MS = 400  # 0x004292D0
GRID_COLUMNS, GRID_ROWS = 12, 10
# Direction 0 right, 1 up, 2 left, 3 down; the flames of 0x0045B8D0.
STEPS = {0: (1, 0), 1: (0, -1), 2: (-1, 0), 3: (0, 1)}
KEY_DIRECTIONS = {pygame.K_LEFT: 2, pygame.K_RIGHT: 0, pygame.K_UP: 1, pygame.K_DOWN: 3}
FLAME_PARTS = ((0, 0, 1), (-1, 0, 7), (1, 0, 9), (0, -1, 8), (0, 1, 6))  # (drow, dcol, flame image)
WALK_FRAMES, WALK_FRAME_MS = 6, 100
FLAME_FRAME_MS = 50
CHAT_ROOM_TYPE = 4
PRESENCE_RETRY_MS = 1000
NAME_COLOR_SELF, NAME_COLOR_OTHER = (0, 255, 0), (255, 255, 0)


# Pets (docs/pet-re.md §8.1, CPet::Update 0x451120; read from the pseudo C):
# speed by squared distance to the owner, a jump back past 300 px, idle
# wandering every 1000..5000 ms. Image anchor (0x32, 0x54) (0x44C120).
PET_ANCHOR = (0x32, 0x54)
PET_FAR = 90000
PET_SPEEDS = ((3600, 100), (10000, 260), (22500, 300))  # (d below, px/s); else 340
PET_FAST = 340
PET_FRAME_MS = 100


class RoomPet:
    """A seat's pet in the room scene (CPet, created by AddPets 0x452DF0)."""

    def __init__(self, res_id: int, owner: Any, rng: random.Random | None = None) -> None:
        self.res_id = res_id
        self.rng = rng or random.Random()
        self.direction, self.walking = 3, False
        self.wandering = False
        self.target: tuple[float, float] | None = None
        self.idle_ms = 0
        self.x, self.y = float(owner.x), float(owner.y)
        self.jump_beside(owner)

    def jump_beside(self, owner: Any) -> None:
        """0x451520: beside the owner, on the side its facing picks."""
        side = -1 if owner.direction == 0 else 1
        self.x = owner.x + side * (20 + self.rng.randrange(50))
        self.y = owner.y + self.rng.randrange(-20, 21)
        self.clamp()
        self.target, self.wandering, self.walking = None, False, False

    def clamp(self) -> None:
        self.x = min(max(self.x, BOUNDS[0]), BOUNDS[2])
        self.y = min(max(self.y, BOUNDS[1]), BOUNDS[3])

    def update(self, owner: Any, dt: int) -> None:
        d = (owner.x - self.x) ** 2 + (owner.y - self.y) ** 2
        if d > PET_FAR:
            self.jump_beside(owner)
            return
        speed = next((value for limit, value in PET_SPEEDS if d < limit), PET_FAST)
        self.idle_ms -= dt
        if owner.walking:
            if d > 3600:
                self.target, self.wandering = (owner.x, owner.y), False
        elif not self.wandering:
            if d < 1600:
                self.target, self.walking = None, False
            if d < 401 or self.idle_ms <= 0:
                # Wander to a random point near the owner (0x451AE0).
                self.target = (owner.x + self.rng.randrange(-60, 61), owner.y + self.rng.randrange(-40, 41))
                self.wandering = True
                self.idle_ms = 1000 + self.rng.randrange(4001)
        if self.target is None:
            self.walking = False
            return
        tx, ty = self.target
        dx, dy = tx - self.x, ty - self.y
        distance = (dx * dx + dy * dy) ** 0.5
        step = speed * dt / 1000.0
        if distance <= step or distance < 1:
            self.x, self.y = tx, ty
            self.target, self.wandering, self.walking = None, False, False
        else:
            self.x += dx * step / distance
            self.y += dy * step / distance
            self.walking = True
            self.direction = (0 if dx > 0 else 2) if abs(dx) >= abs(dy) else (3 if dy > 0 else 1)
        self.clamp()

    def draw(self, screen: pygame.Surface, sprites: Any, clock: int) -> None:
        frames = sprites.get(f"object/pet/pet{self.res_id}_{'walk' if self.walking else 'stand'}.img") if sprites else None
        if not frames:
            return
        per_direction = max(1, len(frames) // 4)
        image, ox, oy = frames[min(self.direction, 3) * per_direction % len(frames) + (clock // PET_FRAME_MS) % per_direction]
        if image is not None:
            screen.blit(image, (int(self.x) - PET_ANCHOR[0] + ox, int(self.y) - PET_ANCHOR[1] + oy))


# Wedding scene effects (docs/wedding-re.md §6): SetWeddingEffect's effect at
# a fixed point over everything; love.eff over a couple standing within 50 px
# on both axes, placed 70 px above their midpoint and re-placed every 2000 ms.
WEDDING_EFFECT_AT = (250, 330)
LOVE_EFFECT = "effect/love.eff"
COUPLE_NEAR = 50
COUPLE_RAISE = 70
COUPLE_RECHECK_MS = 2000


class Couple:
    """0x00440CD0: seats a (the newcomer) and b."""

    def __init__(self, a: int, b: int) -> None:
        self.a, self.b = a, b
        self.active = False
        self.timer = 0
        self.elapsed = 0
        self.at = (0, 0)

    def update(self, first: Any, second: Any, dt: int) -> None:
        """0x00440D70."""
        if self.timer == 0 or self.timer >= COUPLE_RECHECK_MS:
            if first is not None and second is not None and abs(first.x - second.x) < COUPLE_NEAR and abs(first.y - second.y) < COUPLE_NEAR:
                self.active, self.timer, self.elapsed = True, 0, 0
                self.at = (int((first.x + second.x) / 2), int((first.y + second.y) / 2) - COUPLE_RAISE)
            else:
                self.active = False
        if self.active:
            self.timer += dt
            self.elapsed += dt


def cell_of(x: int, y: int) -> tuple[int, int]:
    """0x0045B130/0x0045B160: (row, col)."""
    return (y + 20 - 106) // 40, (x + 20 - 41) // 40


class RoomWalk:
    def __init__(self, runtime: UIRuntime, session: Session, state: Any, avatars: Any, sprites: Any,
                 font: Callable[[int], pygame.font.Font] | None = None):
        self.runtime, self.session, self.state, self.avatars, self.sprites, self.font = runtime, session, state, avatars, sprites, font
        self.keys: list[int] = []  # the held arrows, latest last (0x005A4BEF)
        self.name_key_down = False
        self.bombs: dict[tuple[int, int], tuple[int, int]] = {}  # (row, col) -> (placed, style)
        self.flames: list[tuple[int, int, int, int]] = []  # row, col, image, started
        self.clock = 0
        self.since_sent, self.last_sent = 0, None
        self.sequence = 0
        self.sock: socket.socket | None = None
        self.registered = False
        self.presence_at = -PRESENCE_RETRY_MS
        self.packet_number = 0
        self.last_numbers: dict[int, int] = {}
        self.couples: list[Couple] = []
        self.wedding_effect: str | None = None
        self.wedding_elapsed = 0
        self.effects = EffectPlayer(sprites.resources.read, sprites.get) if sprites is not None else None
        runtime.event_hooks.append(self.handle)
        runtime.tick_hooks.append(self.tick)
        session.on_disconnected.append(self.close)

    # Helpers ----------------------------------------------------------------

    @property
    def names_flag(self) -> bool:
        return getattr(self.runtime, "names_flag", False)

    @names_flag.setter
    def names_flag(self, value: bool) -> None:
        self.runtime.names_flag = value

    def in_room(self) -> bool:
        return self.runtime.current_screen == "room" and self.state.snapshot is not None and self.runtime.activity is None

    def local_seat(self) -> int:
        snapshot = self.state.snapshot
        return snapshot.local_seat - 1 if snapshot is not None else -1

    def avatar(self, seat: int) -> Any | None:
        if 0 <= seat < len(self.state.avatars):
            avatar = self.state.avatars[seat]
            if avatar.role > 0 and avatar.team >= 0:
                return avatar
        return None

    def typing(self) -> bool:
        focus = self.runtime.focus
        return focus is not None and focus.type in TEXT_INPUT_TYPES and focus.editable

    # Wedding effects --------------------------------------------------------

    def reset_scene(self) -> None:
        self.couples.clear()
        self.wedding_effect, self.wedding_elapsed = None, 0

    def add_couple(self, a: int, b: int) -> None:
        self.couples.append(Couple(a, b))

    def remove_couple(self, seat: int) -> None:
        """The first pair holding the seat goes (0x00440F90)."""
        for couple in self.couples:
            if seat in (couple.a, couple.b):
                self.couples.remove(couple)
                return

    def set_wedding_effect(self, name: str) -> None:
        """0x00459110: a new effect replaces the old one."""
        self.wedding_effect, self.wedding_elapsed = name, 0

    # Input ------------------------------------------------------------------

    def handle(self, event: pygame.event.Event) -> None:
        if not self.in_room():
            self.keys.clear()
            self.name_key_down = False
            return
        if event.type == pygame.KEYUP and event.key == pygame.K_F8:
            self.name_key_down = False
        if event.type == pygame.KEYUP and event.key in KEY_DIRECTIONS:
            if event.key in self.keys:
                self.keys.remove(event.key)
        if event.type != pygame.KEYDOWN or self.typing():
            return
        if event.key in KEY_DIRECTIONS:
            if event.key in self.keys:
                self.keys.remove(event.key)
            self.keys.append(event.key)
        elif event.key == pygame.K_SPACE:
            self.put_local_bomb()
        elif event.key == pygame.K_F8:
            if not self.name_key_down:
                self.names_flag = not self.names_flag
                self.name_key_down = True

    def put_local_bomb(self) -> None:
        seat = self.local_seat()
        avatar = self.avatar(seat)
        if avatar is None:
            return
        self.place(avatar.x, avatar.y, avatar)
        # 0x00457E90 sends it whether or not a bubble went down.
        self.send(roompeer.PUT_BOMB_ID, roompeer.bomb_body(seat, avatar.x, avatar.y))

    # Bubbles ----------------------------------------------------------------

    def place(self, x: int, y: int, avatar: Any) -> None:
        row, col = cell_of(x, y)
        if 0 <= row < GRID_ROWS and 0 <= col < GRID_COLUMNS and (row, col) not in self.bombs:
            self.bombs[(row, col)] = (self.clock, max(1, avatar.wear.get("bomb", 1)))

    def burst(self, row: int, col: int, chain: list[tuple[int, int]]) -> None:
        """0x0045B700: every bubble four-connected to it goes too."""
        if (row, col) not in self.bombs:
            return
        del self.bombs[(row, col)]
        chain.append((row, col))
        for d_row, d_col in ((-1, 0), (1, 0), (0, -1), (0, 1)):
            self.burst(row + d_row, col + d_col, chain)

    def tick_bombs(self) -> None:
        for key, (placed, _style) in list(self.bombs.items()):
            if key in self.bombs and self.clock - placed > BOMB_FUSE_MS:
                chain: list[tuple[int, int]] = []
                self.burst(*key, chain)
                for row, col in chain:
                    for d_row, d_col, image in FLAME_PARTS:
                        r, c = row + d_row, col + d_col
                        if 0 <= r < GRID_ROWS and 0 <= c < GRID_COLUMNS:
                            self.flames.append((r, c, image, self.clock))
                sound = self.runtime.namespace.get("PlaySound")
                if callable(sound):
                    sound("x10_01.wav", 1)
        self.flames = [flame for flame in self.flames if self.clock - flame[3] <= FLAME_MS]

    # Movement ---------------------------------------------------------------

    def step(self, avatar: Any, dt: int) -> None:
        """0x00441CF0 for one avatar."""
        bounds = BOUNDS
        x, y = avatar.x, avatar.y
        if avatar.queue:
            direction, tx, ty = avatar.queue[0]
            if (x, y) == (tx, ty):
                if len(avatar.queue) == 1:
                    avatar.walking = False
                avatar.direction = direction
                avatar.queue.pop(0)
                return
            avatar.direction = (0 if x < tx else 2) if x != tx else (3 if y < ty else 1)
            avatar.walking = True
            dx, dy = STEPS[avatar.direction]
            n = int(dt * SPEED)
            x, y = x + dx * n, y + dy * n
            if avatar.direction == 0 and x > tx or avatar.direction == 2 and x < tx:
                x = tx
            if avatar.direction == 3 and y > ty or avatar.direction == 1 and y < ty:
                y = ty
        elif avatar.walking:
            dx, dy = STEPS[avatar.direction]
            n = int(dt * SPEED)
            x, y = x + dx * n, y + dy * n
        if bounds[0] <= x <= bounds[2] and bounds[1] <= y <= bounds[3]:
            avatar.x, avatar.y = x, y

    def tick(self, elapsed: int) -> None:
        if not self.in_room():
            if self.runtime.current_screen == "game":
                self.registered = False  # the match registers its own UDP endpoint
            return
        dt = int(elapsed)
        self.clock += dt
        self.since_sent += dt
        self.network(dt)
        seat = self.local_seat()
        mine = self.avatar(seat)
        if mine is not None:
            if self.keys:
                mine.direction, mine.walking = KEY_DIRECTIONS[self.keys[-1]], True
            elif mine.walking and not mine.queue:
                mine.walking = False
        self.tick_bombs()
        for index in range(len(self.state.avatars)):
            avatar = self.avatar(index)
            if avatar is not None:
                self.step(avatar, dt)
                if getattr(avatar, "pet", None) is not None:
                    avatar.pet.update(avatar, dt)
        for couple in self.couples:
            couple.update(self.avatar(couple.a), self.avatar(couple.b), dt)
        if self.wedding_effect is not None:
            self.wedding_elapsed += dt
        if mine is not None:
            # 0x00457CF0: a moved avatar is sent at most every 300 ms, or at
            # once on a turn.
            last = self.last_sent or (None, None, None)
            if (mine.x, mine.y) != last[:2] and (self.since_sent > SEND_EVERY_MS or mine.direction != last[2]):
                self.last_sent, self.since_sent = (mine.x, mine.y, mine.direction), 0
                self.send(roompeer.MOVE_ID, roompeer.move_body(seat, mine.x, mine.y, mine.direction, SPEED if mine.walking else 0.0))

    # Remote -----------------------------------------------------------------

    def remote_move(self, seat: int, direction: int, x: int, y: int) -> None:
        """RemotePlayerMove (0x004421A0): queue the point unless it repeats."""
        avatar = self.avatar(seat)
        if avatar is None or seat == self.local_seat():
            return
        point = (int(direction), int(x), int(y))
        if not avatar.queue or avatar.queue[-1] != point:
            avatar.queue.append(point)

    def remote_bomb(self, seat: int) -> None:
        """RemotePlayerPutBomb (0x00458AA0) ignores the packet's x, y."""
        avatar = self.avatar(seat)
        if avatar is not None:
            self.place(avatar.x, avatar.y, avatar)

    # Network ----------------------------------------------------------------

    def recipients(self) -> list[int]:
        mine = self.session.profile.player_id if self.session.profile else 0
        return sorted({member.player_id for member in self.state.members.values() if member.player_id not in (0, mine)})

    def send(self, data_id: int, body: bytes) -> None:
        connection = self.session.connection
        recipients = self.recipients()
        if connection is None or not recipients:
            return
        self.sequence += 1
        message = roompeer.room_msg_data(data_id, body, self.sequence, int(time.monotonic() * 1000))
        try:
            connection.send_raw(lambda route: roompeer.build_room_fast(self.session.uin, route, recipients, [message]))
        except OSError as error:
            log(f"[roomwalk] send failed: {error}")

    def server_address(self) -> tuple[str, int] | None:
        session = self.session
        if session.hall is None or session.section is None:
            return None
        server = session.hall.server(session.section.server_id)
        return (server.ip, server.udp_port) if server is not None else None

    def network(self, _dt: int) -> None:
        address = self.server_address()
        if address is None or self.session.profile is None:
            return
        if self.sock is None:
            self.sock = socket.socket(socket.AF_INET, socket.SOCK_DGRAM)
            self.sock.setblocking(False)
            self.sock.connect(address)
        if not self.registered and self.clock - self.presence_at >= PRESENCE_RETRY_MS:
            self.presence_at = self.clock
            self.packet_number += 1
            local = self.sock.getsockname()
            try:
                self.sock.send(roompeer.build_presence(self.packet_number, self.session.profile.player_id, self.session.uin, local))
            except OSError as error:
                log(f"[roomwalk] presence failed: {error}")
        while True:
            try:
                data = self.sock.recv(2048)
            except (BlockingIOError, InterruptedError):
                break
            except OSError:
                break
            self.receive(data)

    def receive(self, data: bytes) -> None:
        parsed = roompeer.parse_datagram(data)
        if parsed is None:
            return
        kind, number, player, _uin, payload = parsed
        if kind == roompeer.TYPE_PRESENCE:
            if not self.registered:
                log("[roomwalk] UDP presence registered")
            self.registered = True
            return
        if kind != roompeer.TYPE_MULTICAST:
            return
        last = self.last_numbers.get(player)
        if last is not None and ((number - last) & 0xFFFFFFFF) >= 0x80000000 or last == number:
            return
        self.last_numbers[player] = number
        message = roompeer.parse_room_msg_data(payload)
        if message is None:
            return
        data_id, fields = message
        function = self.runtime.namespace.get("remotePlayerMove" if data_id == roompeer.MOVE_ID else "remotePlayerPutBomb")
        if not callable(function):
            return
        if data_id == roompeer.MOVE_ID:
            seat, x, y, direction, speed = fields
            call_script(function, seat, direction, x, y, speed)  # dir before x (QQTSection 0x1000C860)
        else:
            call_script(function, *fields)

    def close(self) -> None:
        if self.sock is not None:
            self.sock.close()
        self.sock, self.registered = None, False

    # Drawing ----------------------------------------------------------------

    def draw(self, screen: pygame.Surface, avatar_anchor: tuple[int, int]) -> None:
        """0x004575D0: bubbles, flames, the avatars (each with its pet) by seat,
        then the couple effects and the wedding effect on top."""
        for (row, col), (_placed, style) in self.bombs.items():
            frames = self.sprites.get(f"object/bomb/bomb{style}_stand.img") if self.sprites else None
            if frames:
                image, ox, oy = frames[(self.clock // 150) % len(frames)]
                if image is not None:
                    screen.blit(image, (41 + 40 * col - 20 + ox, 106 + 40 * row - 50 + oy))
        for row, col, image_id, started in self.flames:
            frames = self.sprites.get(f"object/flame/flame{image_id}_stand.img") if self.sprites else None
            if frames:
                index = min(len(frames) - 1, int((self.clock - started) * 2.5) // max(1, FLAME_FRAME_MS * 2))
                image, ox, oy = frames[index]
                if image is not None:
                    screen.blit(image, (41 + 40 * col - 15 + ox, 106 + 40 * row - 35 + oy))
        chat_room = self.state.snapshot is not None and self.state.snapshot.game_type == CHAT_ROOM_TYPE
        names = (not self.names_flag) if chat_room else self.names_flag
        for seat, avatar in enumerate(self.state.avatars):
            if avatar.role <= 0 or avatar.team < 0:
                continue
            wear = tuple(sorted(avatar.wear.items()))
            if avatar.walking:
                frame = (self.clock // WALK_FRAME_MS) % WALK_FRAMES
                image = self.avatars.get(avatar.role, avatar.team, "walk", avatar.direction, frame, wear=wear)
            else:
                image = self.avatars.get(avatar.role, avatar.team, "stand", avatar.direction, wear=wear)
            if image is not None:
                screen.blit(image, (avatar.x - avatar_anchor[0], avatar.y - avatar_anchor[1]))
            if getattr(avatar, "pet", None) is not None:
                avatar.pet.draw(screen, self.sprites, self.clock)
            if names and self.font is not None:
                self.draw_name(screen, seat, avatar)
        if self.effects is not None:
            for couple in self.couples:
                if couple.active:
                    self.effects.draw(screen, LOVE_EFFECT, couple.elapsed, *couple.at)
            if self.wedding_effect is not None:
                self.effects.draw(screen, self.wedding_effect, self.wedding_elapsed, *WEDDING_EFFECT_AT)

    def draw_name(self, screen: pygame.Surface, seat: int, avatar: Any) -> None:
        """0x0044C4A0: misc171 plate at (x-42, y-89), the nickname twice."""
        member = self.state.members.get(seat + 1)
        if member is None:
            return
        lx, ly = avatar.x - 30, avatar.y - 90
        plate = self.sprites.get("object/misc/misc171_stand.img") if self.sprites else None
        if plate and plate[0][0] is not None:
            screen.blit(plate[0][0], (lx - 12 + plate[0][1], ly + 1 + plate[0][2]))
        length = len(member.nickname.encode("gbk", "replace"))
        font = self.font(12)
        color = NAME_COLOR_SELF if seat == self.local_seat() else NAME_COLOR_OTHER
        screen.blit(font.render(member.nickname, True, (0, 0, 0)), (lx + 0x1C - 3 * length, ly + 6))
        screen.blit(font.render(member.nickname, True, color), (lx + 0x1B - 3 * length, ly + 7))
