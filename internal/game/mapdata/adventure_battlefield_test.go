package mapdata

import (
	"os"
	"testing"
)

func TestEveryAdventureStageBattlefieldLoads(t *testing.T) {
	root := os.Getenv("QQT_TEST_CLIENT_ROOT")
	if root == "" {
		t.Skip("QQT_TEST_CLIENT_ROOT is not set")
	}
	catalog, err := LoadCatalog(root)
	if err != nil {
		t.Fatal(err)
	}
	loaded := 0
	for id := uint32(1600); id < 1700; id++ {
		entry, ok := catalog.Map(id)
		if !ok {
			continue
		}
		field, err := LoadAdventureBattlefield(root, entry)
		if err != nil {
			t.Errorf("stage %d: %v", id, err)
			continue
		}
		if field.NativeRule != 12 || field.Battlefield.Width < 15 || len(field.SpawnGroupA) == 0 {
			t.Errorf("stage %d: rule %d, %dx%d, %d spawns", id, field.NativeRule, field.Battlefield.Width, field.Battlefield.Height, len(field.SpawnGroupA))
		}
		loaded++
	}
	if loaded != 50 {
		t.Fatalf("loaded %d stages, want 50", loaded)
	}
}
