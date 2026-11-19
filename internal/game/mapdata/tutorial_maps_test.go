package mapdata

import (
	"os"
	"testing"
)

func TestCatalogKeepsTheTutorialStagesApart(t *testing.T) {
	root := os.Getenv("QQT_TEST_CLIENT_ROOT")
	if root == "" {
		t.Skip("QQT_TEST_CLIENT_ROOT is not set")
	}
	catalog, err := LoadCatalog(root)
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range []uint32{1501, 1502, 1503, 1504} {
		entry, ok := catalog.TutorialMap(id)
		if !ok || entry.NativeRule != 9 || entry.Family != "practice" || len(entry.Battlefield.Cells) != 15*13 {
			t.Fatalf("tutorial map %d: %+v (found %v)", id, entry.ID, ok)
		}
		if _, competitive := catalog.CompetitiveMapMetadata(id); competitive {
			t.Fatalf("tutorial map %d is listed as a competitive map", id)
		}
	}
	// 1501's Kubi sits under an anemone at column 8, row 10 (Task.txt).
	entry, _ := catalog.TutorialMap(1501)
	if cell := entry.Battlefield.Cells[10*15+8]; cell.Collision != CompetitiveCellBreakable {
		t.Fatalf("1501 (8, 10) collision %d, want breakable", cell.Collision)
	}
	if _, ok := catalog.TutorialMap(311); ok {
		t.Fatal("map 311 is a tutorial map")
	}
}
