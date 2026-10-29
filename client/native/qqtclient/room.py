"""Room natives (uiRoom): create, enter and leave rooms, seats, ready/start,
role, team and map selection, room properties, kicking and room chat.

The server is authoritative: requests are sent and the local state changes
only when the matching notification arrives (the requester receives its own
ready/role/team/seat notifications too). Seat indexes passed to and from the
scripts are 0..7; the protocol numbers seats 1..8.
"""

from __future__ import annotations

import dataclasses
import types
from typing import Any, Callable

import pygame

from .avatar import AvatarCache
from .lobby import native_game_info
from .net import room
from .net.player import GameInfo
from .runtime import UIRuntime
from .script import call_script, log
from .session import Session

DEFAULT_ROOM_NAME = "一起来玩吧"
EQUIP_SLOTS = 7  # uiMain.defRoomEquipCnt
CHAT_LIST = "UI.room.chatArea.chatPanel.chatList"
SYSTEM_CHAT_COLOR = (255, 255, 0)
CHAT_COLOR = (255, 255, 255)
# playerChat bubble position relative to the room_setPlayerPos point.
BUBBLE_OFFSET = (20, 105)
# GetSeatInfo().seatStatus as uiRoom reads it: 1 draws the "stop" (closed)
# marker and 2 is an occupied seat. This differs from both protocol encodings.
NATIVE_SEAT_OPEN, NATIVE_SEAT_CLOSED, NATIVE_SEAT_OCCUPIED = 0, 1, 2
# Where room_setPlayerPos points relative to the 100x100 avatar canvas.
AVATAR_ANCHOR = (60, 98)


@dataclasses.dataclass
class SeatAvatar:
    team: int = -1  # colour index 0..7; -1 hides the seat's character
    role: int = -1
    x: int = 0
    y: int = 0


class RoomState:
    def __init__(self) -> None:
        self.snapshot: room.RoomSnapshot | None = None
        self.name = ""
        self.password = ""
        self.members: dict[int, room.RoomPlayer] = {}  # by seat 1..8
        self.avatars = [SeatAvatar() for _ in range(room.MAX_SEATS)]
        self.pending_password = ""

    @property
    def room_id(self) -> int:
        return self.snapshot.room_id if self.snapshot else 0

    def member_by_player_id(self, player_id: int) -> room.RoomPlayer | None:
        return next((member for member in self.members.values() if member.player_id == player_id), None)


def empty_game_info() -> GameInfo:
    return GameInfo(0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0)


def native_room_player(member: room.RoomPlayer | None) -> types.SimpleNamespace:
    if member is None:
        return types.SimpleNamespace(
            m_dwUin=0, m_szQQNickname="", m_wPlayerID=0, m_bTermAndSeat=0, m_bStatus=0, m_bGender=0, m_dwIdentity=0,
            m_stGameInfo=native_game_info(empty_game_info()), m_dwKinIndex=0, m_szKinName="",
            m_stKinFlagID=types.SimpleNamespace(m_iFlagID=-1),
        )
    flag_index = int.from_bytes(member.kin_flag[4:8], "big", signed=True) if len(member.kin_flag) == 8 else -1
    return types.SimpleNamespace(
        m_dwUin=member.uin, m_szQQNickname=member.nickname, m_wPlayerID=member.player_id,
        m_bTermAndSeat=member.team << 4 | member.seat, m_bStatus=member.status, m_bGender=member.gender,
        m_dwIdentity=member.identity, m_stGameInfo=native_game_info(member.game_info),
        m_dwKinIndex=member.kin_index, m_szKinName=member.kin_name, m_stKinFlagID=types.SimpleNamespace(m_iFlagID=flag_index),
    )


def build_room_natives(runtime: UIRuntime, session: Session, avatars: AvatarCache,
                       start_battle: Callable[[bytes, Any], None] | None = None) -> tuple[dict[str, Callable[..., Any]], Callable]:
    """Return the natives and the SC_room_drawPlayer draw callback.

    start_battle(GameBegin payload, battle.online.MatchContext) switches to
    the match when the server starts it."""
    state = RoomState()

    def script(name: str, *args: Any) -> Any:
        function = runtime.namespace.get(name)
        if not callable(function):
            log(f"[room] script callback {name} is missing")
            return None
        return call_script(function, *args)

    def notify(message: str) -> None:
        runtime.show_msgbox(message, "", 0, "UI.SysMsgbox", 0)  # 0: no ui_OnMsgBoxResult action

    def in_room_screen() -> bool:
        return runtime.current_screen == "room" and state.snapshot is not None

    def me() -> room.RoomPlayer | None:
        return next((member for member in state.members.values() if member.uin == session.uin), None)

    def am_owner() -> bool:
        mine = me()
        return mine is not None and state.snapshot is not None and mine.player_id == state.snapshot.owner_id

    def role_id() -> int:
        return session.profile.game_info.role_id if session.profile and session.profile.game_info.role_id else 1

    def refresh_seat(seat: int) -> None:
        if in_room_screen():
            script("ui_NotifyRoomSeatChange", seat - 1)

    def refresh_all_seats() -> None:
        for seat in range(1, room.MAX_SEATS + 1):
            refresh_seat(seat)

    def add_chat(text: str, color: tuple[int, int, int]) -> None:
        if in_room_screen():
            runtime.namespace["Win_SetColorText"](CHAT_LIST, text, 207, *color)

    # Entering ------------------------------------------------------------

    def enter(snapshot: room.RoomSnapshot) -> None:
        state.snapshot = snapshot
        state.name = snapshot.room_name or state.name
        state.password = state.pending_password
        state.members = {member.seat: member for member in snapshot.players}
        state.avatars = [SeatAvatar() for _ in range(room.MAX_SEATS)]
        log(f"[room] entered room {snapshot.room_id} ({len(snapshot.players)} players, map {snapshot.map_id}, type {snapshot.game_type})")
        log(f"[room] owner {snapshot.owner_id} local seat {snapshot.local_seat} seats {snapshot.seat_status} members "
            + ", ".join(f"{member.seat}:{member.player_id}:{member.nickname}" for member in snapshot.players))
        show_room()

    def show_room() -> None:
        snapshot = state.snapshot
        if snapshot is None:
            return
        script("ui_SetGameType", snapshot.game_type)
        runtime.goto_screen("room")
        script("ui_createRoomOK")
        script("ui_NotifyRoomID", snapshot.room_id)
        script("NotifyRoomPropChange")
        refresh_all_seats()
        script("ui_NotifyMapChange", snapshot.map_id)

    def on_create(packet) -> None:
        result, room_id = room.parse_create_room_response(packet.payload)
        if result != 0:
            notify("创建房间失败")
            return
        session.send(room.ENTER_ROOM_COMMAND, room.build_enter_room_request(session.uin, room_id, role_id(), state.pending_password))

    def on_enter(packet, join: bool = False) -> None:
        snapshot = room.parse_enter_room_response(packet.payload, join=join)
        if snapshot.result != 0:
            notify(room.ENTER_ROOM_RESULTS.get(snapshot.result, f"进入房间失败（{snapshot.result}）"))
            return
        enter(snapshot)

    def leave_to_lobby() -> None:
        state.snapshot, state.members = None, {}
        if runtime.current_screen == "room":
            runtime.goto_screen("selRoom")
            runtime.namespace["NotifyRoom"](session.game_type)  # refresh the room list now

    # Notifications -------------------------------------------------------

    def on_member_entered(packet) -> None:
        room_id, member = room.parse_enter_room_notify(packet.payload)
        if room_id != state.room_id:
            return
        state.members[member.seat] = member
        if state.snapshot:
            state.snapshot.seat_status[member.seat - 1] = room.SEAT_OCCUPIED
        add_chat(f"{member.nickname} 进入了房间", SYSTEM_CHAT_COLOR)
        refresh_seat(member.seat)

    def on_member_left(packet) -> None:
        player_id, owner_id, map_id = room.parse_leave_notify(packet.payload)
        member = state.member_by_player_id(player_id)
        if member is None or state.snapshot is None:
            return
        del state.members[member.seat]
        state.snapshot.seat_status[member.seat - 1] = room.SEAT_OPEN
        state.snapshot.owner_id = owner_id or state.snapshot.owner_id
        add_chat(f"{member.nickname} 离开了房间", SYSTEM_CHAT_COLOR)
        refresh_all_seats()  # the owner may have changed
        if map_id and map_id != state.snapshot.map_id:
            state.snapshot.map_id = map_id
            script("ui_NotifyMapChange", map_id)

    def member_update(field: str) -> Callable:
        def handler(packet) -> None:
            _uin, player_id, value = room.parse_member_notify(packet.payload)
            member = state.member_by_player_id(player_id)
            if member is None:
                return
            if field == "role":
                member.game_info.role_id = value
            else:
                setattr(member, field, value)
            refresh_seat(member.seat)
        return handler

    def on_map(packet) -> None:
        map_id, game_type, _continue_id = room.parse_modify_room_info_notify(packet.payload)
        if state.snapshot is None:
            return
        state.snapshot.map_id = map_id
        if game_type != state.snapshot.game_type:
            state.snapshot.game_type = game_type
            script("ui_UpdateGameType", game_type)
        script("ui_NotifyMapChange", map_id)

    def on_modify_room(packet) -> None:
        name, flag, password = room.parse_modify_room_notify(packet.payload)
        if state.snapshot is None:
            return
        state.name, state.password, state.snapshot.room_flag = name, password, flag
        if in_room_screen():
            script("NotifyRoomPropChange")

    def on_seat_status(packet) -> None:
        seat, status = room.parse_seat_status_notify(packet.payload)
        if state.snapshot is None or not 1 <= seat <= room.MAX_SEATS:
            return
        state.snapshot.seat_status[seat - 1] = room.SEAT_OPEN if status == room.SET_SEAT_OPEN else room.SEAT_LOCKED
        refresh_seat(seat)

    def on_chat(packet) -> None:
        source, _destination, message = room.parse_room_chat_notify(packet.payload)
        add_chat(message, SYSTEM_CHAT_COLOR if source == room.CHAT_SYSTEM_SOURCE else CHAT_COLOR)
        member = state.member_by_player_id(source)
        if member is not None and in_room_screen():
            # Speech bubble over the seat's character; the text drops the
            # "<nickname>说:" prefix.
            bubble = f"UI.room.playerChat{member.seat - 1}"
            avatar = state.avatars[member.seat - 1]
            runtime.namespace["Win_SetText"](bubble, message.split(":", 1)[-1])
            runtime.namespace["Win_Move2Pos"](bubble, avatar.x - BUBBLE_OFFSET[0], avatar.y - BUBBLE_OFFSET[1])
            script("ui_playerChat", member.seat - 1)

    def on_kicked(packet) -> None:
        _reason, room_id, kicked_uin = room.parse_kick_notify(packet.payload)
        if kicked_uin == session.uin and room_id == state.room_id:
            leave_to_lobby()
            notify("你被房主请出了房间")

    def on_leave(packet) -> None:
        if room.parse_result(packet.payload) == 0:
            leave_to_lobby()

    def on_kick_result(packet) -> None:
        if room.parse_result(packet.payload) != 0:
            add_chat("踢人失败", SYSTEM_CHAT_COLOR)

    def on_start_result(packet) -> None:
        result = room.parse_result(packet.payload)
        if result != 0:
            log(f"[room] start game rejected: 0x{result:04X}")  # the reason follows as a system chat line

    def on_game_begin(packet) -> None:
        if start_battle is None or state.snapshot is None:
            notify("对战画面尚未实现")
            return
        from .battle.online import MatchContext, MatchMember

        def back_to_room() -> None:
            if state.snapshot is not None:
                show_room()
            else:
                runtime.goto_screen("selRoom")

        members = {member.player_id: MatchMember(member.uin, member.nickname, member.game_info.points) for member in state.members.values()}
        try:
            start_battle(packet.payload, MatchContext(bool(state.snapshot.room_flag & room.PROPERTY_FREE_RULE), members, back_to_room))
        except Exception as error:  # noqa: BLE001 - keep the room usable
            log(f"[room] cannot start the match: {error}")
            notify(f"无法进入对战：{error}")

    def ignore(_packet) -> None:
        pass

    handlers = {
        room.CREATE_ROOM_COMMAND: on_create,
        room.ENTER_ROOM_COMMAND: on_enter,
        room.JOIN_ROOM_COMMAND: lambda packet: on_enter(packet, join=True),
        room.ENTER_ROOM_NOTIFY_COMMAND: on_member_entered,
        room.LEAVE_ROOM_NOTIFY_COMMAND: on_member_left,
        room.CHANGE_ROLE_NOTIFY_COMMAND: member_update("role"),
        room.CHANGE_TEAM_NOTIFY_COMMAND: member_update("team"),
        room.READY_NOTIFY_COMMAND: member_update("status"),
        room.MODIFY_ROOM_INFO_NOTIFY_COMMAND: on_map,
        room.MODIFY_ROOM_NOTIFY_COMMAND: on_modify_room,
        room.SET_SEAT_STATUS_NOTIFY_COMMAND: on_seat_status,
        room.ROOM_CHAT_NOTIFY_COMMAND: on_chat,
        room.KICK_OFF_NOTIFY_COMMAND: on_kicked,
        room.LEAVE_ROOM_COMMAND: on_leave,
        room.KICK_OFF_PLAYER_COMMAND: on_kick_result,
        room.START_GAME_COMMAND: on_start_result,
        room.GAME_BEGIN_NOTIFY_COMMAND: on_game_begin,
    }
    # Plain acknowledgements; the state change arrives as a notification.
    for command in (room.PLAYER_PETS_COMMAND, room.READY_COMMAND, room.CANCEL_READY_COMMAND, room.CHANGE_ROLE_COMMAND,
                    room.CHANGE_TEAM_COMMAND, room.MODIFY_ROOM_INFO_COMMAND, room.MODIFY_ROOM_COMMAND,
                    room.SET_SEAT_STATUS_COMMAND, room.ROOM_CHAT_COMMAND):
        handlers[command] = ignore
    session.handlers.update(handlers)
    session.on_disconnected.append(lambda: setattr(state, "snapshot", None))

    # Natives: lobby side -------------------------------------------------

    def CreateRoom(name, flag, _role_id, password):
        state.name = str(name) or DEFAULT_ROOM_NAME
        state.pending_password = str(password)
        session.send(room.CREATE_ROOM_COMMAND, room.build_create_room_request(
            session.uin, state.name, int(flag), state.pending_password, session.game_type))

    def EnterRoom(room_id, password=""):
        state.name, state.pending_password = session.room_names.get(int(room_id), ""), str(password)
        session.send(room.ENTER_ROOM_COMMAND, room.build_enter_room_request(session.uin, int(room_id), role_id(), state.pending_password))

    def AutoJoinRoom():
        state.name, state.pending_password = "", ""
        session.send(room.JOIN_ROOM_COMMAND, room.build_join_room_request(session.uin, session.game_type))

    # Natives: room screen ------------------------------------------------

    def LeaveRoom(_uin=0):
        if state.snapshot is None:
            leave_to_lobby()
            return
        if not session.send(room.LEAVE_ROOM_COMMAND, room.build_simple_request(session.uin)):
            leave_to_lobby()

    def GetSeatInfo(index):
        index = int(index)
        snapshot = state.snapshot
        status = snapshot.seat_status[index] if snapshot and 0 <= index < room.MAX_SEATS else room.SEAT_LOCKED
        member = state.members.get(index + 1)
        if member is None and status == room.SEAT_OCCUPIED:
            status = room.SEAT_OPEN
        is_master = member is not None and snapshot is not None and member.player_id == snapshot.owner_id
        if member is not None:
            native_status = NATIVE_SEAT_OCCUPIED
        else:
            native_status = NATIVE_SEAT_CLOSED if status == room.SEAT_LOCKED else NATIVE_SEAT_OPEN
        return types.SimpleNamespace(seatStatus=chr(native_status),
                                     bIsMaster=chr(int(is_master)), player=native_room_player(member))

    def GetRoomNameFromID(_room_id):
        return state.name

    def sc_getRoomProp():
        flag = state.snapshot.room_flag if state.snapshot else 0
        return types.SimpleNamespace(m_szRoomName=state.name, m_cRoomRule=int(bool(flag & room.PROPERTY_FREE_RULE)),
                                     m_szPassword=state.password)

    def sc_modifyRoom(name, flag, password):
        name, flag, password = str(name), int(flag), str(password)
        changes = room.MODIFY_NAME if name != state.name else 0
        if password:
            changes |= room.MODIFY_PASSWORD_PRESENT | room.MODIFY_PASSWORD_ENABLED
        if password != state.password:
            changes |= room.MODIFY_PASSWORD_CHANGED
        game_type = state.snapshot.game_type if state.snapshot else session.game_type
        if session.send(room.MODIFY_ROOM_COMMAND, room.build_modify_room_request(
                session.uin, changes, name, flag | (room.PROPERTY_PASSWORD if password else 0), password, game_type)):
            # Peers are notified; the requester only gets an acknowledgement.
            state.name, state.password = name, password
            if state.snapshot:
                state.snapshot.room_flag = flag & room.PROPERTY_FREE_RULE | (room.PROPERTY_PASSWORD if password else 0)

    def UserReady(_flag=0):
        mine = me()
        if mine is None:
            return
        if am_owner():
            session.send(room.START_GAME_COMMAND, room.build_simple_request(session.uin))
        else:
            command = room.CANCEL_READY_COMMAND if mine.status else room.READY_COMMAND
            session.send(command, room.build_simple_request(session.uin))

    def UserSelRoleInRoom(_selected, role):
        session.send(room.CHANGE_ROLE_COMMAND, room.build_change_role_request(session.uin, int(role)))
        session.save_state(last_role=int(role))

    def UserSelTeamInRoom(_selected, team):
        session.send(room.CHANGE_TEAM_COMMAND, room.build_change_team_request(session.uin, int(team)))

    def UserSelMapInRoom(map_id, continue_id=0):
        if state.snapshot is None:
            return
        game_type = state.snapshot.game_type
        if session.send(room.MODIFY_ROOM_INFO_COMMAND, room.build_modify_room_info_request(session.uin, int(map_id), game_type, int(continue_id))):
            # Only peers receive NOTIFY_CHANGE_MAP.
            state.snapshot.map_id = int(map_id)
            script("ui_NotifyMapChange", int(map_id))

    def ChangeRoomType(game_type):
        if state.snapshot is None:
            return
        state.snapshot.game_type = int(game_type)
        session.game_type = int(game_type)

    def UserClickSeatInRoom(index, button):
        """Button 0: the owner kicks the seat's player or opens/locks an empty
        seat. Button 1 (view profile) needs no server request."""
        seat = int(index) + 1
        if int(button) != 0 or state.snapshot is None or not am_owner() or not 1 <= seat <= room.MAX_SEATS:
            return
        member = state.members.get(seat)
        if member is not None:
            if member.uin != session.uin:
                session.send(room.KICK_OFF_PLAYER_COMMAND, room.build_kick_request(session.uin, member.player_id, member.uin))
            return
        locked = state.snapshot.seat_status[seat - 1] == room.SEAT_LOCKED
        session.send(room.SET_SEAT_STATUS_COMMAND, room.build_set_seat_status_request(
            session.uin, seat, room.SET_SEAT_OPEN if locked else room.SET_SEAT_LOCKED))

    def RoomChat(_mode, text):
        text = str(text)
        if text:
            session.send(room.ROOM_CHAT_COMMAND, room.build_room_chat_request(session.uin, text))

    def SendUserChatInRoom(_is_master, _text):
        pass  # the original forwards the line to its chat-command handling; RoomChat already sent it

    def SetSelfSeatId(_index):
        pass

    def Fetch_RoomType():
        return 0  # 0: normal room; 1: match waiting room

    def SetRoomType(_game_type):
        pass

    def fetch_SectionMode():
        return session.profile.section_mode if session.profile else 0

    def GetEquipedProp():
        empty = types.SimpleNamespace(m_nItemID=0, m_iNumOfItem=0)
        return [types.SimpleNamespace(m_stItem=empty) for _ in range(EQUIP_SLOTS)]

    def GetChatBgList():
        return []

    def room_changePlayer(index, team, role):
        if 0 <= int(index) < room.MAX_SEATS:
            avatar = state.avatars[int(index)]
            avatar.team, avatar.role = int(team), int(role)

    def room_setPlayerPos(index, x, y):
        if 0 <= int(index) < room.MAX_SEATS:
            avatar = state.avatars[int(index)]
            avatar.x, avatar.y = int(x), int(y)

    def room_playerWear(*_args):
        pass  # costume items on the room avatar are not drawn yet

    def draw_players(screen: pygame.Surface) -> None:
        for avatar in state.avatars:
            if avatar.role <= 0 or avatar.team < 0:
                continue
            image = avatars.get(avatar.role, avatar.team)
            if image is not None:
                screen.blit(image, (avatar.x - AVATAR_ANCHOR[0], avatar.y - AVATAR_ANCHOR[1]))

    excluded = {"script", "notify", "in_room_screen", "me", "am_owner", "role_id", "refresh_seat", "refresh_all_seats",
                "add_chat", "enter", "leave_to_lobby", "member_update", "ignore", "draw_players"}
    natives = {name: value for name, value in locals().items()
               if callable(value) and name not in excluded and not name.startswith("on_")}
    return natives, draw_players
