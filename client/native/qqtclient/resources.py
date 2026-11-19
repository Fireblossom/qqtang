"""Read-only access to the original client's files and data/object.pkg."""

from __future__ import annotations

import os
import pathlib
import struct
import zlib

ARCHIVE_VERSION = 100
ARCHIVE_HEADER_SIZE = 16

# Files the release put in the wrong folder (docs/missing-assets.md): the
# program asks for the first path, the client holds the file under the
# second. Each entry is (wanted folder, actual folder, file-name prefix);
# a wanted file that exists for real is never replaced. The original
# client simply drew nothing (it does not check that an image exists).
MISPLACED_FOLDERS = (
    # 边框名片 12501/12502: uiShop/uiRoom ask for object/NamecardBound/…
    # and the card icons res/uires/icon/namecardbound/… (itemCFG type
    # 'namecardbound'), but these two sit with the plain name cards.
    ("object/namecardbound", "object/namecard", "namecardbound"),
    ("res/uires/icon/namecardbound", "res/uires/icon/namecard", "namecardbound"),
)


def normalize(path: str) -> str:
    return path.strip().replace("\\", "/").lstrip("/").lower()


class ObjectArchive:
    """Index of data/object.idx + data/object.pkg (see internal/game/itemcatalog/archive.go)."""

    def __init__(self, client_root: pathlib.Path):
        index = (client_root / "data" / "object.idx").read_bytes()
        self.package = client_root / "data" / "object.pkg"
        version, count, index_offset, index_size = struct.unpack_from("<4I", index, 0)
        if version != ARCHIVE_VERSION:
            raise ValueError(f"object archive index version {version}, want {ARCHIVE_VERSION}")
        if index_offset != ARCHIVE_HEADER_SIZE or index_offset + index_size != len(index):
            raise ValueError("invalid object archive index range")
        self.entries: dict[str, tuple[int, int, int]] = {}
        offset = ARCHIVE_HEADER_SIZE
        for _ in range(count):
            (length,) = struct.unpack_from("<H", index, offset)
            offset += 2
            raw_path = index[offset:offset + length].decode("gbk")
            offset += length
            package_index, package_offset, expanded, compressed = struct.unpack_from("<4I", index, offset)
            offset += 16
            if package_index != 0:
                raise ValueError(f"object archive entry {raw_path!r} uses package {package_index}")
            self.entries[normalize(raw_path)] = (package_offset, compressed, expanded)
        if offset != len(index):
            raise ValueError("object archive index has trailing bytes")

    def read(self, path: str) -> bytes | None:
        entry = self.entries.get(normalize(path))
        if entry is None:
            return None
        package_offset, compressed, expanded = entry
        with open(self.package, "rb") as file:
            file.seek(package_offset)
            data = zlib.decompress(file.read(compressed))
        if len(data) != expanded:
            raise ValueError(f"object resource {path!r} expanded to {len(data)} bytes, want {expanded}")
        return data


def add_folder_aliases(files: dict[str, pathlib.Path]) -> dict[str, str]:
    """Map each misplaced file to the path the program asks for (host
    addition). Returns {wanted: actual} for the log and the tests."""
    aliases: dict[str, str] = {}
    for wanted, actual, prefix in MISPLACED_FOLDERS:
        for path in [key for key in files if key.startswith(actual + "/")]:
            name = path[len(actual) + 1:]
            if "/" in name or not name.startswith(prefix):
                continue
            target = f"{wanted}/{name}"
            if target not in files:
                files[target] = files[path]
                aliases[target] = path
    return aliases


class Resources:
    """Case-insensitive lookup: loose files first, then data/object.pkg."""

    def __init__(self, client_root: pathlib.Path):
        self.root = client_root
        self.files: dict[str, pathlib.Path] = {}
        for directory, _, names in os.walk(client_root):
            for name in names:
                full = pathlib.Path(directory) / name
                self.files[normalize(full.relative_to(client_root).as_posix())] = full
        self.aliases = add_folder_aliases(self.files)
        self.archive = ObjectArchive(client_root)

    def read(self, path: str) -> bytes | None:
        loose = self.files.get(normalize(path))
        if loose is not None:
            return loose.read_bytes()
        return self.archive.read(path)

    def path(self, path: str) -> pathlib.Path | None:
        return self.files.get(normalize(path))
