"""Pet (宠物) natives and server messages, as QQTSection's XPet interface does
them (docs/pet-re.md). Queries read the local cache of 5 base and 20 extra
pets; the two replies update it and run the original's script refreshes.
Pet settings come from the client's config/PetCfg.ini.

Registered after shop.py and room.py: replaces the shop's GetPlayerAllPetsId
and the room's ignored 0x00D4.
"""

from __future__ import annotations

import struct
import types
from typing import Any, Callable

from .net import pet
from .resources import Resources
from .runtime import UIRuntime
from .script import call_script, log
from .session import Session
from .shop import item_view


class PetConfig:
    """config/PetCfg.ini. QQTSection's own parser (0x10009822) matches
    section and key names case-insensitively; values lose leading blanks."""

    def __init__(self, text: str) -> None:
        self.sections: dict[str, dict[str, str]] = {}
        section: dict[str, str] | None = None
        for raw in text.splitlines():
            line = raw.strip()
            if line.startswith("[") and "]" in line:
                section = self.sections.setdefault(line[1:line.index("]")].strip().lower(), {})
            elif section is not None and "=" in line and not line.startswith(("#", ";")):
                key, _, value = raw.partition("=")
                section.setdefault(key.strip().lower(), value.lstrip(" \t").rstrip("\r\n"))

    @classmethod
    def load(cls, resources: Resources) -> "PetConfig":
        data = resources.read("config/PetCfg.ini")
        if data is None:
            log("[pet] config/PetCfg.ini is missing")
            return cls("")
        return cls(data.decode("gbk", "replace"))

    def get(self, section: str, key: str) -> str | None:
        return self.sections.get(section.lower(), {}).get(key.lower())

    def exp_table(self) -> list[int]:
        values = (self.get("PetExperience", "ExpTable") or "").split(",")
        return [int(value) for value in values if value.strip().lstrip("-").isdigit()][:100]

    def res_id(self, type_id: int, level: int) -> int:
        """0x10034E84: Level<level>, then each lower level, first non-zero."""
        for current in range(int(level) & 0xFFFF, 0, -1):
            value = self.get(f"Pet{int(type_id)}", f"Level{current}")
            try:
                number = int(value.strip()) if value is not None else 0
            except ValueError:
                number = 0
            if number:
                return number
        return 0

    def skill_text(self, section: str, skill_id: int, size: int) -> str:
        """0x10034D84 / 0x10034E04: at most size-1 bytes; a cut that ends on
        a lead byte drops it."""
        value = self.get(section, f"skill{int(skill_id) & 0xFF}")
        if value is None:
            return ""
        data = value.encode("gbk", "replace")
        if len(data) >= size - 1:
            data = data[:size - 1]
            if data and data[-1] >= 0x80:
                data = data[:-1]
        return data.decode("gbk", "ignore")


def level_rate(experience: int, table: list[int]) -> tuple[float, int]:
    """0x10034C4D: progress inside the current level and the next level's
    experience; past the last entry the bar is full."""
    if not table:
        return 0.0, 0
    index = next((k for k, value in enumerate(table) if experience < value), len(table))
    if 0 < index < len(table):
        low, high = table[index - 1], table[index]
        if high == low:
            return 0.0, high
        return struct.unpack("<f", struct.pack("<f", (experience - low) / (high - low)))[0], high
    return 1.0, table[index - 1]


def pet_view(entry: pet.Pet | None) -> types.SimpleNamespace:
    """PET_BASE_INFO for the scripts. They read ord(m_szSkills[i]), so the
    skills are a str of one character per byte."""
    entry = entry or pet.Pet()
    return types.SimpleNamespace(
        m_dwPetId=entry.pet_id, m_dwPetTypeId=entry.type_id, m_dwPetExperience=entry.experience,
        m_dwPetLoyalty=entry.loyalty, m_wPetLevel=entry.level, m_wPetMood=entry.mood, m_wPetState=entry.state,
        m_szPetName=entry.name, m_dwSkillCount=len(entry.skills),
        m_szSkills=entry.skills[:pet.SKILL_SLOT].ljust(pet.SKILL_SLOT, b"\0").decode("latin-1"))


class PetState:
    def __init__(self) -> None:
        self.base: list[pet.Pet] = []
        self.extra: list[pet.Pet] = []

    def all(self) -> list[pet.Pet]:
        return self.base + self.extra

    def find(self, pet_id: int) -> pet.Pet | None:
        return next((entry for entry in self.all() if entry.pet_id == int(pet_id)), None)

    def active(self) -> pet.Pet | None:
        """The carried pet the local seat shows (0x1003139C)."""
        return next((entry for entry in self.all() if entry.pet_id != 0 and entry.state == pet.STATE_ACTIVE), None)

    def replace(self, new: pet.Pet) -> bool:
        """0x10035022; the original only rests the carried pets ahead of the
        match, Go keeps one carried pet so resting all of them is the same."""
        for group in (self.base, self.extra):
            for index, entry in enumerate(group):
                if entry.pet_id == new.pet_id:
                    group[index] = new
                    if new.state == pet.STATE_ACTIVE:
                        for other in self.all():
                            if other is not new and other.state == pet.STATE_ACTIVE:
                                other.state = pet.STATE_RESTING
                    return True
        return False


def build_pet_natives(runtime: UIRuntime, session: Session, resources: Resources) -> dict[str, Callable[..., Any]]:
    state = PetState()
    config = PetConfig.load(resources)
    session.pets = state

    def script(name: str, *args: Any) -> Any:
        function = runtime.namespace.get(name)
        return call_script(function, *args) if callable(function) else None

    def visible(widget: str) -> bool:
        return runtime.find(widget) is not None and bool(runtime.namespace["Win_IsVisible"](widget))

    def handle(event: int, pet_id: int, parameter: str = "") -> None:
        session.send(pet.HANDLE_PET_COMMAND, pet.build_handle_pet(session.uin, event, int(pet_id), parameter), pet.ROUTE)

    # Server messages --------------------------------------------------------

    def on_pets(packet) -> None:
        """0x1003116C: replace the cache, then the panel and the deal/pawn views."""
        try:
            state.base, state.extra = pet.parse_pets_info(packet.payload)
        except (struct.error, ValueError) as error:
            log(f"[pet] bad pet list: {error}")
            return
        log(f"[pet] {len(state.base)}+{len(state.extra)} pets: "
            + ", ".join(f"{entry.pet_id}/{entry.type_id} L{entry.level} s{entry.state}" for entry in state.all()))
        script("UpdatePetInfo")
        if runtime.current_screen == "selRoom":
            if visible("UI.C2CDealDlg"):
                script("UpdateC2CDealDlgAfterBargain")
            elif visible("UI.selRoom.pawnshop"):
                script("updateMyStorageInPawn")

    def on_handle(packet) -> None:
        """0x10030F6D."""
        try:
            result, event, reason, entry = pet.parse_handle_pet(packet.payload)
        except struct.error as error:
            log(f"[pet] bad handle-pet reply: {error}")
            return
        log(f"[pet] event {event} result {result} {reason}")
        if result == 0 and event not in (pet.EVENT_FREE, pet.EVENT_ADOPT) and entry is not None:
            if not state.replace(entry):
                log(f"[pet] reply for unknown pet {entry.pet_id}")
        script("UpdatePetInfo")
        if result != 0:
            runtime.show_msgbox(reason, "", 3, "UI.SysMsgbox", 0)
            script("ui_msgBox", 3)

    session.handlers.update({pet.PLAYER_PETS_COMMAND: on_pets, pet.HANDLE_PET_COMMAND: on_handle})

    # Requests -----------------------------------------------------------------

    def RequestPetsInfo():
        session.send(pet.PLAYER_PETS_COMMAND, pet.build_pets_request(session.uin), pet.ROUTE)

    def RaisePet(item_id, pet_id):
        item_id = int(item_id)
        if item_id in pet.FOOD_ITEMS:
            event = pet.EVENT_FEED
        elif item_id in pet.SKILL_ITEMS:
            event = pet.EVENT_LEARN
        elif item_id in pet.CARD_ITEMS:
            event = pet.EVENT_ADOPT
        else:
            return
        handle(event, pet_id, "%d" % item_id)

    def TakePet(pet_id):
        handle(pet.EVENT_TAKE, pet_id)

    def StopTakePet(pet_id):
        handle(pet.EVENT_STOP_TAKE, pet_id)

    def FreePet(pet_id):
        handle(pet.EVENT_FREE, pet_id)

    def ChangePetName(pet_id, name):
        if pet_view(state.find(pet_id)).m_szPetName == str(name):
            return
        handle(pet.EVENT_RENAME, pet_id, str(name))

    # Queries (cache only) ---------------------------------------------------------

    def GetPlayerAllPetsId():
        return [entry.pet_id for entry in state.all()]

    def GetCurActivePetInfo():
        """0x5509C0: the carried pet, else the last pet in the list."""
        pets = state.all()
        chosen = next((entry for entry in pets if entry.state == pet.STATE_ACTIVE), pets[-1] if pets else None)
        return pet_view(chosen)

    def GetPetBaseInfo(pet_id):
        return pet_view(state.find(pet_id))

    def GetPetAllSkillId(pet_id):
        entry = state.find(pet_id)
        return list(entry.skills) if entry is not None and len(entry.skills) <= pet.SKILL_SLOT else []

    def GetPetAllSkillName(pet_id):
        return [GetSkillNameById(skill) for skill in GetPetAllSkillId(pet_id)]

    def GetPetResId(type_id, level):
        return config.res_id(int(type_id), int(level))

    def GetPetLevelRateInfo(experience):
        rate, next_level = level_rate(int(experience), config.exp_table())
        return types.SimpleNamespace(m_fRate=rate, m_unNextLevelExp=next_level)

    def GetSkillNameById(skill_id):
        return config.skill_text("SkillName", skill_id, 20)

    def GetSkillDescription(skill_id):
        return config.skill_text("SkillDescription", skill_id, 256)

    def GetAllMyPetItems():
        items = session.profile.items if session.profile else []
        return [item_view(item) for item in items if item.quantity > 0 and item.item_id in pet.PET_ITEMS]

    # The pawnshop's pet view goes to the deal server, which Go does not run.

    def RequestPawnPetInfo(sale_index):
        log(f"[pet] pawnshop pet {sale_index}: no deal server")

    def GetPawnPetInfo():
        return pet_view(None)

    return {name: value for name, value in locals().items() if callable(value) and name[0].isupper()}
