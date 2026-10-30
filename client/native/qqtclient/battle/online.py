"""A live match: from GAME_BEGIN (0x0089) until GAME_OVER, then back to the room.

The sidecar plays the match (simulation and UDP scene traffic, see
cmd/qqt-battle-sidecar/online.go). This class owns the TCP side: it forwards
the match's notifications to the sidecar, sends the REQUEST_PLAY copies the
sidecar returns, keeps the connection alive and shows the server's verdict.
"""

from __future__ import annotations

import dataclasses
from typing import Any, Callable

import pygame

from ..avatar import AvatarCache
from ..net import game, match, room
from ..render import Renderer
from ..resources import Resources
from ..runtime import UIRuntime
from ..script import log
from ..session import Session
from .scene import BattleScene, ResultRow
from .sidecar import Sidecar, SidecarError

# The result stays up this long unless Enter, Esc or a click returns to the
# room earlier.
RESULT_SHOWN_MS = 10_000
SKIP_KEYS = (pygame.K_RETURN, pygame.K_KP_ENTER, pygame.K_ESCAPE)
HELLO_INTERVAL_MS = 60_000


@dataclasses.dataclass
class MatchMember:
    uin: int
    nickname: str
    points: int  # competitive points before the match (level icon)
    wear: tuple[tuple[str, int], ...] = ()  # costume parts worn in the room
    seat: int = -1  # room seat 0..7 (Client.exe actor +0x396); -1 if unknown
    gender: int = 0  # as the room reports it; 1 picks the male title ranks


@dataclasses.dataclass
class MatchContext:
    """What the room knows about the match's players and how to go back."""
    free_rule: bool
    members: dict[int, MatchMember]  # by PlayerID
    back_to_room: Callable[[], None]


class OnlineBattle:
    def __init__(self, runtime: UIRuntime, renderer: Renderer, resources: Resources, session: Session,
                 begin: bytes, context: MatchContext):
        self.runtime, self.renderer, self.session, self.context = runtime, renderer, session, context
        if session.profile is None or session.section is None or session.hall is None:
            raise SidecarError("not in a section")
        server = session.hall.server(session.section.server_id)
        if server is None:
            raise SidecarError("section server is unknown")
        self.local_id = session.profile.player_id
        sidecar = Sidecar()
        try:
            state = sidecar.start_online(resources.root, begin, self.local_id, session.uin, server.ip, server.udp_port,
                                         {player_id: member.uin for player_id, member in context.members.items()}, context.free_rule,
                                         session.autoplay)
        except SidecarError:
            sidecar.close()
            raise
        begin_info = state["begin"]
        players = [dict(player, name=member.nickname if (member := context.members.get(player["id"])) else f"玩家{player['id']}",
                        seat=member.seat if member else -1, points=member.points if member else 0,
                        gender=member.gender if member else 0)
                   for player in begin_info["players"]]
        log(f"[match] game {begin_info['game_id']} map {begin_info['map_id']}, arbitrator {begin_info['arbitrator']}, "
            f"players " + ", ".join(f"{player['id']}:{player['name']}" for player in players))
        if state.get("prepared"):
            log("[match] room props: " + ", ".join(f"{entry['item']}x{entry['count']}" for entry in state["prepared"]))
        if begin_info["arbitrator"] != self.local_id:
            log(f"[match] following arbitrator {begin_info['arbitrator']}")
        self.scene = BattleScene(resources, renderer.sprites, AvatarCache(resources), sidecar, begin_info["map_id"], players,
                                 local_id=self.local_id, state=state, realtime=True,
                                 wear={player_id: member.wear for player_id, member in context.members.items()})
        self.scene.on_tcp_out = lambda command, payload: session.send(command, payload)
        self.elapsed_ms = 0.0
        self.last_hello_ms = 0.0
        self.finish_at: float | None = None
        self.finished = False
        self.net_logged: tuple[Any, ...] | None = None
        self.saved_handlers = {command: session.handlers.get(command) for command in
                               (match.REQUEST_PLAY_COMMAND, match.GAME_EVENT_NOTIFY_COMMAND, match.NOTIFY_UDP_OK_COMMAND,
                                match.HELLO_COMMAND)}
        session.handlers.update({
            match.REQUEST_PLAY_COMMAND: lambda _packet: None,  # echo of each accepted copy
            match.GAME_EVENT_NOTIFY_COMMAND: self.on_game_event,
            match.NOTIFY_UDP_OK_COMMAND: lambda packet: self.scene.pending_tcp.append((packet.command, packet.payload)),
            match.HELLO_COMMAND: lambda _packet: None,
        })
        session.on_disconnected.append(self.finish)
        runtime.namespace.update({
            "LeaveGame": self.leave,
            # Explore-mode panels; empty lists make uiGame hide them.
            "GetPVEBossInfo": lambda: [],
            "GetPVEPlayerInfo": lambda: [],
        })
        runtime.goto_screen("game")
        self.scene.attach(runtime, begin_info["map_id"], renderer.font, renderer.text_line)
        renderer.underlay, renderer.overlay = self.scene.draw, self.scene.draw_overlay

    def on_game_event(self, packet: game.Packet) -> None:
        try:
            event = match.parse_game_event_notify(packet.payload)
        except (ValueError, IndexError) as error:
            log(f"[match] bad game event: {error}")
            return
        if event.schema != match.SCHEMA_GAME_OVER:
            self.scene.pending_tcp.append((packet.command, packet.payload))
            return
        over = match.parse_game_over(event.body)
        mine = next((result for result in over.results if result.player_id == self.local_id), None)
        log("[match] game over: " + ", ".join(f"{result.player_id}={result.result}" for result in over.results))
        self.scene.result = mine.result if mine is not None else match.RESULT_LOSS
        self.scene.show_results([
            ResultRow(result.player_id, result.result, *(result.counts + [0, 0, 0])[:3], result.point,
                      self.context.members[result.player_id].points + result.point if result.player_id in self.context.members else result.point)
            for result in over.results
        ])
        if self.finish_at is None:
            self.finish_at = self.elapsed_ms + RESULT_SHOWN_MS

    def handle(self, event: pygame.event.Event) -> None:
        if self.finished:
            return
        if self.finish_at is not None and (event.type == pygame.KEYDOWN and event.key in SKIP_KEYS
                                           or event.type == pygame.MOUSEBUTTONDOWN):
            self.finish()
            return
        self.scene.handle(event)

    def tick(self, elapsed_ms: float) -> None:
        if self.finished:
            return
        self.elapsed_ms += elapsed_ms
        try:
            self.scene.tick(elapsed_ms)
        except SidecarError as error:
            log(f"[match] sidecar failed: {error}")
            self.finish()
            return
        net = self.scene.state.get("net") or {}
        summary = (net.get("presence"), net.get("peers_ready"), net.get("arbitrator"))
        if summary != self.net_logged:
            self.net_logged = summary
            log(f"[match] UDP presence {net.get('presence')}, peers ready {net.get('peers_ready')}/{net.get('peers')}")
        if self.elapsed_ms - self.last_hello_ms >= HELLO_INTERVAL_MS:
            self.last_hello_ms = self.elapsed_ms
            self.session.send(match.HELLO_COMMAND, match.build_hello(self.session.uin))
        if self.finish_at is not None and self.elapsed_ms >= self.finish_at:
            self.finish()

    def leave(self) -> None:
        """The leave button: give up the match and leave the room."""
        self.session.send(room.LEAVE_ROOM_COMMAND, room.build_simple_request(self.session.uin))
        self.finish()

    def finish(self) -> None:
        if self.finished:
            return
        self.close()
        self.context.back_to_room()

    def close(self) -> None:
        if self.finished:
            return
        self.finished = True
        self.scene.close()
        if self.finish in self.session.on_disconnected:
            self.session.on_disconnected.remove(self.finish)
        for command, handler in self.saved_handlers.items():
            if handler is None:
                self.session.handlers.pop(command, None)
            else:
                self.session.handlers[command] = handler
        if self.renderer.underlay == self.scene.draw:
            self.renderer.underlay = self.renderer.overlay = None
