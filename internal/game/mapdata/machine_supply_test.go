package mapdata

import (
	"encoding/binary"
	"os"
	"path/filepath"
	"slices"
	"testing"
)

func TestMachineSupplyUsesThirdLayerAndTypeSix(t *testing.T) {
	data := testCompetitiveMapBytes(7, nil)
	put := func(layer, row, col int, id int32) {
		binary.LittleEndian.PutUint32(data[12+(layer*195+row*15+col)*4:], uint32(id))
	}
	// A marked wall is not a supply floor; empty and ordinary floors are not
	// candidates. Continuation markers don't create their own objects.
	put(1, 1, 1, 13015)
	put(2, 2, 2, 13015)
	put(2, 2, 3, -13015)
	put(2, 2, 5, 13006)
	put(2, 2, 7, 13015)
	put(2, 4, 12, 13015)
	elements := map[uint32]competitiveMapElement{
		13015: {id: 13015, canMove: 16, width: 1, height: 1},
		13006: {id: 13006, width: 1, height: 1},
	}
	cells, err := parseMachineSupplyCells(elements, data)
	want := []CompetitiveCell{{Row: 2, Col: 2}, {Row: 4, Col: 12}}
	if err != nil || !slices.Equal(cells, want) {
		t.Fatalf("supply floors %v, %v; want %v", cells, err, want)
	}
}

func TestShippedMachineSupplyFloors(t *testing.T) {
	root := os.Getenv("QQT_AI_TEST_CLIENT_ROOT")
	if root == "" {
		root = filepath.Join("..", "..", "..", "runtime", "client-patched")
	}
	catalog, err := LoadCatalog(root)
	if err != nil && os.Getenv("QQT_AI_TEST_CLIENT_ROOT") != "" {
		t.Fatal(err)
	}
	if err != nil {
		catalog, err = LoadCatalog(filepath.Join("..", "..", "..", "client", "original"))
		if err != nil {
			t.Skipf("native fixtures unavailable: %v", err)
		}
	}
	// Independently read third-layer 13015 counts in the installed maps.
	counts := []int{2, 2, 1, 4, 2, 2, 3, 3, 2, 2, 2}
	for i, count := range counts {
		entry, ok := catalog.CompetitiveMap(uint32(1301 + i))
		if !ok || len(entry.MachineSupplyCells) != 2*count {
			t.Fatalf("map %d supply floors %v", 1301+i, entry.MachineSupplyCells)
		}
		for _, c := range entry.MachineSupplyCells {
			if c.Col >= 6 && c.Col <= 8 {
				t.Fatalf("middle column is a supply floor: %v", c)
			}
		}
		entry.MachineSupplyCells[0].Row = 255
		again, _ := catalog.CompetitiveMap(uint32(1301 + i))
		if again.MachineSupplyCells[0].Row == 255 {
			t.Fatal("catalog candidate slice escaped without cloning")
		}
	}
}
