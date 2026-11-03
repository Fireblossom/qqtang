package mapdata

import (
	"fmt"
	"path/filepath"
)

// AdventureBattlefield is the playable layout of one rule-12 stage. Stages
// are 20x18 up to 40x40, larger than the competitive 15x13 field, so the
// competitive airdrop bounds do not apply; the native tables (hidden-item
// cells, spawn groups, objective cells) and the collision projection are
// read exactly as for competitive maps.
type AdventureBattlefield struct {
	ID              uint32
	NativeRule      uint32
	HiddenItemCells []CompetitiveCell
	SpawnGroupA     []CompetitiveCell
	SpawnGroupB     []CompetitiveCell
	ObjectiveCells  []CompetitiveCell
	Battlefield     CompetitiveBattlefield
}

// LoadAdventureBattlefield parses an adventure stage's .map file.
func LoadAdventureBattlefield(clientRoot string, entry AdventureMap) (AdventureBattlefield, error) {
	elements, err := loadCompetitiveMapElements(filepath.Join(clientRoot, "object", "mapElem", "mapElem.py"))
	if err != nil {
		return AdventureBattlefield{}, err
	}
	metadata, err := loadMapMetadata(filepath.Join(clientRoot, "map"), elements, entry.ID, entry.FileName, false)
	if err != nil {
		return AdventureBattlefield{}, fmt.Errorf("adventure stage %d: %w", entry.ID, err)
	}
	return AdventureBattlefield{
		ID: entry.ID, NativeRule: metadata.nativeRule, HiddenItemCells: metadata.hiddenItemCells,
		SpawnGroupA: metadata.spawnGroupA, SpawnGroupB: metadata.spawnGroupB, ObjectiveCells: metadata.objectiveCells,
		Battlefield: metadata.battlefield,
	}, nil
}
