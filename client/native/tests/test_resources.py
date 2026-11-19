import pathlib
import struct
import sys
import tempfile
import unittest

sys.path.insert(0, str(pathlib.Path(__file__).resolve().parents[1]))

from qqtclient.resources import ARCHIVE_HEADER_SIZE, ARCHIVE_VERSION, Resources  # noqa: E402


def make_client(root: pathlib.Path, files: dict[str, bytes]) -> None:
    data = root / "data"
    data.mkdir(parents=True)
    (data / "object.idx").write_bytes(struct.pack("<4I", ARCHIVE_VERSION, 0, ARCHIVE_HEADER_SIZE, 0))
    (data / "object.pkg").write_bytes(b"")
    for name, content in files.items():
        path = root / name
        path.parent.mkdir(parents=True, exist_ok=True)
        path.write_bytes(content)


class MisplacedFolderTest(unittest.TestCase):
    def test_namecard_frames_found_where_the_program_looks(self):
        # docs/missing-assets.md: 12501/12502 sit with the plain name cards.
        with tempfile.TemporaryDirectory() as directory:
            root = pathlib.Path(directory)
            make_client(root, {
                "object/namecard/namecardbound1_stand.img": b"stand1",
                "object/namecard/namecardbound2_walk.img": b"walk2",
                "object/namecard/namecard7_walk.img": b"plain",
                "object/namecardbound/namecardbound3_walk.img": b"real3",
                "res/uires/icon/namecard/namecardbound1.img": b"icon1",
            })
            resources = Resources(root)
            self.assertEqual(resources.read("object/NamecardBound/namecardbound1_stand.img"), b"stand1")
            self.assertEqual(resources.read("object/namecardbound/namecardbound2_walk.img"), b"walk2")
            self.assertEqual(resources.read("res/uires/icon/namecardbound/namecardbound1.img"), b"icon1")
            self.assertEqual(resources.path("res/uiRes/icon/NamecardBound/namecardbound1.img"),
                             root / "res/uires/icon/namecard/namecardbound1.img")
            # The files stay readable where they really are; plain cards and
            # files already in the right folder are untouched.
            self.assertEqual(resources.read("object/namecard/namecardbound1_stand.img"), b"stand1")
            self.assertIsNone(resources.read("object/namecardbound/namecard7_walk.img"))
            self.assertEqual(resources.read("object/namecardbound/namecardbound3_walk.img"), b"real3")
            self.assertEqual(sorted(resources.aliases), [
                "object/namecardbound/namecardbound1_stand.img",
                "object/namecardbound/namecardbound2_walk.img",
                "res/uires/icon/namecardbound/namecardbound1.img",
            ])

    def test_real_file_wins_over_alias(self):
        with tempfile.TemporaryDirectory() as directory:
            root = pathlib.Path(directory)
            make_client(root, {
                "object/namecard/namecardbound4_walk.img": b"misplaced",
                "object/namecardbound/namecardbound4_walk.img": b"right",
            })
            self.assertEqual(Resources(root).read("object/namecardbound/namecardbound4_walk.img"), b"right")


if __name__ == "__main__":
    unittest.main()
