package battleengine

import (
	"os"
	"path/filepath"
	"testing"

	"qqtang/internal/game/mapdata"
)

func treasureTestConfig() Config {
	config := testConfig()
	config.Grid = testOpenGrid(15, 13)
	config.Rules.ObjectiveSceneIDs = [MaxObjectiveKinds]uint32{150, 151, 152}
	config.Rules.ObjectiveDeathDrops = true
	config.Rules.ExternalConclusion = true
	for index := range config.Participants {
		config.Participants[index].MaxBombCapacity = 5
		config.Participants[index].MaxBombPower = 5
	}
	return config
}

func TestObjectiveRulesValidation(t *testing.T) {
	for name, edit := range map[string]func(*Rules){
		"ordinary scene":       func(rules *Rules) { rules.ObjectiveSceneIDs[0] = SceneBombPowerSmall },
		"field scene":          func(rules *Rules) { rules.ObjectiveSceneIDs[0] = 42 },
		"duplicate":            func(rules *Rules) { rules.ObjectiveSceneIDs = [MaxObjectiveKinds]uint32{150, 0, 150} },
		"drops, no objectives": func(rules *Rules) { rules.ObjectiveDeathDrops = true },
	} {
		config := testConfig()
		edit(&config.Rules)
		if _, err := New(config); err == nil {
			t.Errorf("%s: rules were accepted", name)
		}
	}
	if _, err := New(treasureTestConfig()); err != nil {
		t.Fatalf("treasure rules: %v", err)
	}
}

func TestObjectivePickupsAreRejectedByDefault(t *testing.T) {
	config := testConfig()
	config.Pickups = []Pickup{{SceneID: 150, Cell: Cell{Row: 1, Col: 2}, State: PickupAvailable}}
	if _, err := New(config); err == nil {
		t.Fatal("rule-1 engine accepted a treasure gem")
	}
}

func TestObjectivePickupIsCountedWithoutEffect(t *testing.T) {
	config := treasureTestConfig()
	cell := Cell{Row: 1, Col: 2}
	config.Pickups = []Pickup{{SceneID: 151, Cell: cell, State: PickupAvailable}}
	engine := mustEngine(t, config)
	before := engine.actors[0].Participant
	event, err := engine.ApplyVerifiedPickupAt(1, 151, PositionAtCellCenter(cell))
	if err != nil {
		t.Fatal(err)
	}
	if event.Effect != PickupEffectObjective || event.RewardBefore != 0 || event.RewardAfter != 1 {
		t.Fatalf("pickup event = %+v", event)
	}
	actor := engine.actors[0]
	if actor.CarriedObjectives != [MaxObjectiveKinds]uint16{0, 1, 0, 0} {
		t.Fatalf("carried = %v", actor.CarriedObjectives)
	}
	if actor.Participant != before {
		t.Fatalf("attributes changed: %+v, was %+v", actor.Participant, before)
	}
}

func TestDeathDropsOnlyCarriedObjectives(t *testing.T) {
	engine := mustEngine(t, treasureTestConfig())
	actor := &engine.actors[0]
	actor.CarriedObjectives = [MaxObjectiveKinds]uint16{2, 0, 1}
	actor.nativeBasicPickupDelta = [3]int16{1, 1, 0}
	actor.HeldActions[0] = HeldActionSlot{ActionID: 41, Count: 1}
	actor.State = ActorTrapped
	events, err := engine.ConfirmTrappedDeath(1)
	if err != nil {
		t.Fatal(err)
	}
	var dropped []uint32
	for _, event := range events {
		if event.Kind == EventPickupDropped {
			dropped = append(dropped, event.SceneID)
		}
	}
	if len(dropped) != 3 || dropped[0] != 150 || dropped[1] != 150 || dropped[2] != 152 {
		t.Fatalf("drops = %v, want [150 150 152]", dropped)
	}
	if actor.CarriedObjectives != ([MaxObjectiveKinds]uint16{}) {
		t.Fatalf("carried objectives survived death: %v", actor.CarriedObjectives)
	}
	if countEventKind(events, EventMatchEnded) != 0 || engine.Terminal().Ended {
		t.Fatal("an external-conclusion round ended on elimination")
	}
}

func TestDeathWithoutObjectivesDropsOrdinaryItems(t *testing.T) {
	engine := mustEngine(t, treasureTestConfig())
	actor := &engine.actors[0]
	actor.nativeBasicPickupDelta = [3]int16{1, 0, 0}
	actor.State = ActorTrapped
	events, err := engine.ConfirmTrappedDeath(1)
	if err != nil {
		t.Fatal(err)
	}
	if got := countEventKind(events, EventPickupDropped); got != 1 {
		t.Fatalf("drops = %d, want the one ordinary item", got)
	}
}

func TestVerifiedDeathAcceptsObjectiveDrops(t *testing.T) {
	engine := mustEngine(t, treasureTestConfig())
	drops := []Pickup{{SceneID: 152, Cell: Cell{Row: 5, Col: 5}, State: PickupAvailable}}
	if _, err := engine.ApplyVerifiedElimination(1, 2, drops); err != nil {
		t.Fatal(err)
	}
	if engine.Terminal().Ended {
		t.Fatal("an external-conclusion round ended on elimination")
	}
}

func TestExternalConclusionStillEndsAtDeadline(t *testing.T) {
	config := treasureTestConfig()
	engine := mustEngine(t, config)
	engine.actors[0].State = ActorEliminated
	for engine.ElapsedMS() < config.Rules.RoundDurationMS {
		if _, err := engine.Step(nil); err != nil {
			t.Fatal(err)
		}
	}
	if outcome := engine.Terminal(); !outcome.Ended || !outcome.TimedOut {
		t.Fatalf("outcome = %+v, want timed out", outcome)
	}
}

func TestRespawnRestoresStartingActor(t *testing.T) {
	engine := mustEngine(t, treasureTestConfig())
	actor := &engine.actors[0]
	start := actor.Participant
	actor.BombPower, actor.BombCapacity = 5, 4
	actor.CarriedObjectives[0] = 3
	actor.MovementStatus = MovementStatusFast
	actor.State = ActorTrapped
	if _, err := engine.ConfirmTrappedDeath(1); err != nil {
		t.Fatal(err)
	}
	if _, err := engine.RespawnActor(2, start.Spawn); err == nil {
		t.Fatal("respawned an active actor")
	}
	if _, err := engine.RespawnActor(1, Cell{Row: 99, Col: 0}); err == nil {
		t.Fatal("respawned outside the grid")
	}
	cell := Cell{Row: 2, Col: 4}
	event, err := engine.RespawnActor(1, cell)
	if err != nil {
		t.Fatal(err)
	}
	if event.Kind != EventActorRespawned || event.PlayerID != 1 || event.Cell != cell {
		t.Fatalf("event = %+v", event)
	}
	want := start
	want.Spawn = cell
	if actor.State != ActorActive || actor.Position != PositionAtCellCenter(cell) || actor.Participant != want {
		t.Fatalf("respawned actor = %+v", *actor)
	}
	if actor.CarriedObjectives != ([MaxObjectiveKinds]uint16{}) || actor.MovementStatus != MovementStatusNone {
		t.Fatalf("respawned actor kept state: %+v", *actor)
	}
	if want := engine.ElapsedMS() + NativePostTransformationProtectionMS; actor.HarmProtectionExpiresAt != want || event.EffectExpiresAt != want {
		t.Fatalf("protection until %d (event %d), want %d", actor.HarmProtectionExpiresAt, event.EffectExpiresAt, want)
	}
	if _, err := engine.Step(nil); err != nil {
		t.Fatal(err)
	}
}

func TestRespawnNeedsExternalConclusion(t *testing.T) {
	engine := mustEngine(t, testConfig())
	engine.actors[0].State = ActorEliminated
	if _, err := engine.RespawnActor(1, Cell{Row: 1, Col: 1}); err == nil {
		t.Fatal("rule-1 engine respawned an actor")
	}
}

// Every shipped treasure map builds with the sidecar's options and its
// GAME_BEGIN gems, and stays refused without AllowSpecialRules.
func TestShippedTreasureMapsBuildWithSpecialRules(t *testing.T) {
	root := filepath.Join("..", "..", "..", "runtime", "client-patched")
	if _, err := os.Stat(filepath.Join(root, "map", "mapDesc.py")); os.IsNotExist(err) {
		t.Skip("verified runtime client is not present")
	}
	catalog, err := mapdata.LoadCatalog(root)
	if err != nil {
		t.Fatal(err)
	}
	built := 0
	for _, mapID := range catalog.AllCompetitiveIDs() {
		entry, ok := catalog.CompetitiveMap(mapID)
		if !ok || entry.Rule != mapdata.CompetitiveRuleTreasure {
			continue
		}
		participants := []Participant{}
		for playerID := uint16(1); playerID <= 2; playerID++ {
			participant, err := ParticipantFromNativeRole(playerID, 1, byte(playerID), ParticipantHuman)
			if err != nil {
				t.Fatal(err)
			}
			participants = append(participants, participant)
		}
		supply := entry.PlanCompetitiveSupply(7, []uint16{1, 1}, false)
		items := append(supply.WallItems, entry.CompetitiveObjectiveSceneItems(1, 7)...)
		options := CompetitiveMapConfigOptions{
			SpawnSeed: 3, ItemSeed: 7, SpawnMode: NativeSpawnModeForMap(entry, false), Participants: participants,
			Rules:             Rules{TickMS: 20, ObjectiveSceneIDs: [MaxObjectiveKinds]uint32{150, 151, 152}, ObjectiveDeathDrops: true, ExternalConclusion: true},
			RecordedWallItems: items, RecordedDelayedItems: supply.DelayedItems, UseRecordedWallItems: true,
		}
		if _, err := ConfigFromCompetitiveMap(entry, options); err == nil {
			t.Fatalf("map %d built without AllowSpecialRules", mapID)
		}
		options.AllowSpecialRules = true
		config, err := ConfigFromCompetitiveMap(entry, options)
		if err != nil {
			t.Fatalf("map %d: %v", mapID, err)
		}
		if _, err := New(config); err != nil {
			t.Fatalf("map %d engine: %v", mapID, err)
		}
		gems := 0
		for _, pickup := range config.Pickups {
			if pickup.SceneID >= 150 && pickup.SceneID <= 152 {
				gems++
			}
		}
		if gems != 14 {
			t.Fatalf("map %d placed %d gems, want 14", mapID, gems)
		}
		built++
	}
	if built == 0 {
		t.Fatal("no treasure maps in the shipped catalog")
	}
	if err := ValidateNativeRuntimeMap(mapdata.CompetitiveMap{NativeRule: 5, Rule: mapdata.CompetitiveRuleTreasure}); err == nil {
		t.Fatal("server validation accepted a treasure map")
	}
}

// A relived actor is protected from flames for 3 s, like one whose avatar
// just ended (0x005B5202).
func TestRespawnProtectsFromFlames(t *testing.T) {
	config := treasureTestConfig()
	config.Rules.BombFuseMS = 300
	engine := mustEngine(t, config)
	engine.actors[0].State = ActorEliminated
	cell := Cell{Row: 2, Col: 4}
	if _, err := engine.RespawnActor(1, cell); err != nil {
		t.Fatal(err)
	}
	if _, err := engine.ApplyVerifiedBombPlacementAt(2, cell, 2, 0, engine.ElapsedMS()); err != nil {
		t.Fatal(err)
	}
	exploded := 0
	for engine.ElapsedMS() < 1000 {
		events, err := engine.Step(nil)
		if err != nil {
			t.Fatal(err)
		}
		exploded += countEventKind(events, EventBombExploded)
	}
	if exploded != 1 {
		t.Fatalf("explosions = %d, want 1", exploded)
	}
	if state := engine.actors[0].State; state != ActorActive {
		t.Fatalf("a protected actor was hit: state %d", state)
	}
}

func TestDepartureRecyclesCarriedObjectivesOnlyWhenEnabled(t *testing.T) {
	for _, enabled := range []bool{false, true} {
		config := treasureTestConfig()
		config.Rules.ObjectiveDepartureRecycle = enabled
		engine := mustEngine(t, config)
		engine.actors[0].CarriedObjectives = [MaxObjectiveKinds]uint16{2, 0, 1}
		runtime, err := NewRuntime(engine, nil)
		if err != nil {
			t.Fatal(err)
		}
		if err = runtime.AcceptHumanDeparture(1); err != nil {
			t.Fatal(err)
		}
		want := []uint32(nil)
		if enabled {
			want = []uint32{150, 150, 152}
		}
		if got := engine.recycledPickupSceneIDs; len(got) != len(want) || (len(want) > 0 && (got[0] != want[0] || got[1] != want[1] || got[2] != want[2])) {
			t.Fatalf("enabled %v: recycled %v, want %v", enabled, got, want)
		}
		if engine.actors[0].CarriedObjectives != [MaxObjectiveKinds]uint16{} || len(engine.pickups) != 0 {
			t.Fatalf("enabled %v: carried %v, pickups %+v", enabled, engine.actors[0].CarriedObjectives, engine.pickups)
		}
	}
}

func TestDepartureRecycleNeedsObjectives(t *testing.T) {
	config := testConfig()
	config.Rules.ObjectiveDepartureRecycle = true
	if _, err := New(config); err == nil {
		t.Fatal("departure recycling without objectives was accepted")
	}
}

// Every shipped sculpture map builds with its GAME_BEGIN fragments, which
// keep their wall-placement slots but leave the engine's pickups.
func TestShippedSculptureMapsSeparateLooseFragments(t *testing.T) {
	root := filepath.Join("..", "..", "..", "runtime", "client-patched")
	if _, err := os.Stat(filepath.Join(root, "map", "mapDesc.py")); os.IsNotExist(err) {
		t.Skip("verified runtime client is not present")
	}
	catalog, err := mapdata.LoadCatalog(root)
	if err != nil {
		t.Fatal(err)
	}
	built := 0
	for _, mapID := range catalog.AllCompetitiveIDs() {
		entry, ok := catalog.CompetitiveMap(mapID)
		if !ok || entry.Rule != mapdata.CompetitiveRuleSculpture {
			continue
		}
		participants := []Participant{}
		for playerID := uint16(1); playerID <= 2; playerID++ {
			participant, err := ParticipantFromNativeRole(playerID, 1, byte(playerID), ParticipantHuman)
			if err != nil {
				t.Fatal(err)
			}
			participants = append(participants, participant)
		}
		supply := entry.PlanCompetitiveSupply(7, []uint16{1, 1}, false)
		items := append(supply.WallItems, entry.CompetitiveObjectiveSceneItems(1, 7)...)
		options := CompetitiveMapConfigOptions{
			SpawnSeed: 3, ItemSeed: 7, SpawnMode: NativeSpawnModeForMap(entry, false), Participants: participants,
			Rules:             Rules{TickMS: 20, ExternalConclusion: true, LooseObjectSceneIDs: [MaxLooseObjectKinds]uint32{161, 162, 163, 164, 165}},
			RecordedWallItems: items, RecordedDelayedItems: supply.DelayedItems, UseRecordedWallItems: true, AllowSpecialRules: true,
		}
		config, err := ConfigFromCompetitiveMap(entry, options)
		if err != nil {
			t.Fatalf("map %d: %v", mapID, err)
		}
		if _, err := New(config); err != nil {
			t.Fatalf("map %d engine: %v", mapID, err)
		}
		if len(config.LooseObjects) != 7 {
			t.Fatalf("map %d: %d loose fragments, want 7", mapID, len(config.LooseObjects))
		}
		placed, err := PlaceHiddenPickups(7, competitiveCells(entry.HiddenItemCells), items)
		if err != nil {
			t.Fatal(err)
		}
		pickups, loose := 0, 0
		for _, pickup := range placed {
			if pickup.SceneID >= 161 && pickup.SceneID <= 165 {
				if config.LooseObjects[loose] != pickup {
					t.Fatalf("map %d fragment %d = %+v, want %+v", mapID, loose, config.LooseObjects[loose], pickup)
				}
				loose++
				continue
			}
			if config.Pickups[pickups] != pickup {
				t.Fatalf("map %d pickup %d = %+v, want %+v", mapID, pickups, config.Pickups[pickups], pickup)
			}
			pickups++
		}
		built++
	}
	if built == 0 {
		t.Fatal("no sculpture maps in the shipped catalog")
	}
	if _, err := New(Config{Grid: testOpenGrid(3, 3), Rules: Rules{TickMS: 20, LooseObjectSceneIDs: [MaxLooseObjectKinds]uint32{SceneBombPowerSmall}}}); err == nil {
		t.Fatal("an ordinary pickup was accepted as a loose object")
	}
}

func TestRuleGrantedHeldActions(t *testing.T) {
	engine := mustEngine(t, testConfig())
	if !engine.GrantHeldAction(1, 66, 1) || !engine.GrantHeldAction(1, 66, 1) {
		t.Fatal("grant refused")
	}
	if got := actorHeldActionCount(&engine.actors[0], 66); got != 2 {
		t.Fatalf("held 66 = %d, want 2", got)
	}
	if !engine.ConsumeHeldAction(1, 66) || !engine.ConsumeHeldAction(1, 66) || engine.ConsumeHeldAction(1, 66) {
		t.Fatal("consume did not follow the count")
	}
	if engine.GrantHeldAction(99, 66, 1) {
		t.Fatal("granted to an unknown player")
	}
}
