"""Lobby (uiSelRoom) natives: room pages, player list and periodic refresh."""

from __future__ import annotations

import types
from typing import Any, Callable

from .net import lobby
from .net.player import GameInfo
from .runtime import UIRuntime
from .script import call_script
from .session import Session

ROOMS_PER_PAGE = 8
ROOM_REFRESH_SECONDS = 5
PLAYER_REFRESH_SECONDS = 10
PLAYER_PAGE = 30
PLAYER_MODE_SECTION = 0

# ROOM_INFO.iRoomFlag as the lobby script reads it: 0 is an empty slot, 1 a
# joinable preparing room; 2 and 3 render as disabled (in match / full).
SLOT_EMPTY, SLOT_JOINABLE, SLOT_IN_MATCH, SLOT_FULL = 0, 1, 2, 3
# Modes listed in uiSelRoom.allGameModeList (all of them are served locally).
VALID_GAME_MODES = [0, 1, 2, 3, 4, 5, 6, 7, 8, 13]


def native_game_info(info: GameInfo) -> types.SimpleNamespace:
    return types.SimpleNamespace(
        m_dwWinNum=info.wins, m_dwLossNum=info.losses, m_dwEqualNum=info.draws, m_dwOrgID=info.organization_id,
        m_dwPoint=info.points, m_dwMoney=info.money, m_wDegree=info.degree, m_bRoleID=info.role_id,
        m_dwPetID=info.pet_id, m_dwExtWinNum=info.extended_wins, m_dwExtLossNum=info.extended_losses,
        m_dwExtEqualNum=info.extended_draws, m_dwExtPoint=info.extended_points,
    )


def native_player(player: lobby.Player) -> types.SimpleNamespace:
    flag_index = int.from_bytes(player.kin_flag[4:8], "big", signed=True) if len(player.kin_flag) == 8 else -1
    return types.SimpleNamespace(
        m_dwPlayerUin=player.uin, m_szPlayerNickname=player.nickname, m_wPlayerID=player.player_id,
        m_bGender=player.gender, m_bIconID=player.icon_id, m_dwIdentity=player.identity,
        m_stGameInfo=native_game_info(player.game_info), m_bExtItemNum=len(player.ext_item_ids),
        m_dwExtItemID=list(player.ext_item_ids), m_dwKinIndex=player.kin_index, m_nKinNameLen=len(player.kin_name.encode("gbk", "replace")),
        m_szKinName=player.kin_name, m_stKinFlagID=types.SimpleNamespace(m_iFlagID=flag_index), m_iHonor=player.honor,
    )


class LobbyState:
    def __init__(self) -> None:
        self.page = 0
        self.rooms: dict[int, lobby.Room] = {}
        self.players: list[lobby.Player] = []


def build_lobby_natives(runtime: UIRuntime, session: Session) -> dict[str, Callable[..., Any]]:
    state = LobbyState()

    def script(name: str, *args: Any) -> None:
        function = runtime.namespace.get(name)
        if callable(function) and runtime.current_screen == "selRoom":
            call_script(function, *args)

    def page_count() -> int:
        rooms = session.profile.room_count if session.profile else 0
        return max(1, (rooms + ROOMS_PER_PAGE - 1) // ROOMS_PER_PAGE)

    def first_room_id() -> int:
        minimum = session.profile.minimum_room_id if session.profile else 1
        return minimum + state.page * ROOMS_PER_PAGE

    def request_rooms() -> None:
        session.send(lobby.ROOM_LIST_COMMAND, lobby.build_room_list_request(session.uin, first_room_id(), ROOMS_PER_PAGE, session.game_type))

    def request_players() -> None:
        session.send(lobby.PLAYER_LIST_COMMAND, lobby.build_player_list_request(session.uin, 0, PLAYER_PAGE))

    def refresh_page_buttons() -> None:
        script("ui_sectSetBtnState", 1, int(state.page > 0))
        script("ui_sectSetBtnState", 2, int(state.page + 1 < page_count()))

    def on_room_list(packet) -> None:
        state.rooms = {room.room_id: room for room in lobby.parse_room_list(packet.payload)}
        session.room_names.update({room.room_id: room.name for room in state.rooms.values()})
        script("updateRoom")
        refresh_page_buttons()

    def on_room_push(packet) -> None:
        for room in lobby.parse_room_push(packet.payload):
            if room.capacity == 0:
                # A dissolved room is pushed as an empty entry with no capacity.
                state.rooms.pop(room.room_id, None)
            else:
                state.rooms[room.room_id] = room
                session.room_names[room.room_id] = room.name
        script("updateRoom")

    def on_player_list(packet) -> None:
        # The native list always starts with the local player
        # (Sect.meGetPlayerInfo reads entry 0).
        others = [player for player in lobby.parse_player_list(packet.payload) if player.uin != session.uin]
        state.players = ([local_player()] if session.profile else []) + others
        script("updatePlayer")

    def local_player() -> lobby.Player:
        profile = session.profile
        return lobby.Player(session.uin, session.nickname, profile.player_id, session.gender, 0, profile.identity,
                            profile.game_info, [], 0, "", b"\xff" * 8, profile.honor)

    def on_entered() -> None:
        state.page, state.rooms, state.players = 0, {}, [local_player()]
        request_rooms()
        request_players()
        runtime.every("lobby-rooms", ROOM_REFRESH_SECONDS, request_rooms)
        runtime.every("lobby-players", PLAYER_REFRESH_SECONDS, request_players)

    def on_disconnected() -> None:
        runtime.every("lobby-rooms", 0, None)
        runtime.every("lobby-players", 0, None)

    session.handlers[lobby.ROOM_LIST_COMMAND] = on_room_list
    session.handlers[lobby.ROOM_PUSH_COMMAND] = on_room_push
    session.handlers[lobby.PLAYER_LIST_COMMAND] = on_player_list
    session.on_entered.append(on_entered)
    session.on_disconnected.append(on_disconnected)

    # Natives ----------------------------------------------------------------

    def NotifyRoom(game_type):
        session.game_type, state.page = int(game_type), 0
        request_rooms()

    def SetGameType(game_type):
        session.game_type = int(game_type)

    def SectUserClickBtn(button):
        if button == 1 and state.page > 0:
            state.page -= 1
        elif button == 2 and state.page + 1 < page_count():
            state.page += 1
        else:
            return
        state.rooms = {}
        request_rooms()

    def GetRoomCnt(*_args):
        rooms = session.profile.room_count if session.profile else 0
        return max(0, min(ROOMS_PER_PAGE, rooms - state.page * ROOMS_PER_PAGE))

    def GetRoomInfo(index, _game_type=None):
        number = first_room_id() + int(index)
        room = state.rooms.get(number)
        info = types.SimpleNamespace(
            sUIRoomID=number, shRoomLogicalID=number, sIsVIP=0, m_szRoomName="", iRoomFlag=SLOT_EMPTY,
            iRuleType=0, IsRoomLocked=0, sPlayerInRoom=0, sMaxPlayer=0, m_nMapID=0,
        )
        if room is not None:
            if not room.flag & lobby.ROOM_FLAG_PREPARING:
                slot = SLOT_IN_MATCH
            elif room.players >= room.capacity:
                slot = SLOT_FULL
            else:
                slot = SLOT_JOINABLE
            info.sIsVIP = int(bool(room.flag & lobby.ROOM_FLAG_VIP))
            info.m_szRoomName = room.name
            info.iRoomFlag = slot
            info.iRuleType = int(bool(room.flag & lobby.ROOM_FLAG_FREE_RULE))
            info.IsRoomLocked = int(bool(room.flag & lobby.ROOM_FLAG_PASSWORD))
            info.sPlayerInRoom, info.sMaxPlayer, info.m_nMapID = room.players, room.capacity, room.map_id
        return info

    def GetPlayerCnt(mode=PLAYER_MODE_SECTION):
        return len(state.players) if mode == PLAYER_MODE_SECTION else 0

    def fetch_PlayerInfoInPlayerList(index, mode=PLAYER_MODE_SECTION):
        players = state.players if mode == PLAYER_MODE_SECTION else []
        if not 0 <= index < len(players):
            return types.SimpleNamespace(m_nPlayerStatus=0, m_PlayerInfo=types.SimpleNamespace(m_dwPlayerUin=0))
        return types.SimpleNamespace(m_nPlayerStatus=1, m_PlayerInfo=native_player(players[index]))

    def fetch_PlayerIdentityByUin(uin):
        if session.profile and uin == session.uin:
            return session.profile.identity
        return next((player.identity for player in state.players if player.uin == uin), 0)

    def fetch_ValidGameModes():
        return list(VALID_GAME_MODES)

    def GetLocalShopVertion(*_args):
        return 0

    def GetLastShopVertion():
        return 0

    def sc_getCurSectInfo():
        section = session.section
        return types.SimpleNamespace(
            m_szSectionName=section.name if section else "", m_nSectionID=section.id if section else 0,
            m_nCurrentNumOfPlayer=section.current_players if section else 0, m_nMaxNumOfPlayer=section.max_players if section else 0,
        )

    excluded = {"script", "local_player", "page_count", "first_room_id", "request_rooms", "request_players", "refresh_page_buttons"}
    return {name: value for name, value in locals().items()
            if callable(value) and name not in excluded and not name.startswith("on_")}
