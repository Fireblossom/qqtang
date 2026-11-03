package battleengine

import (
	"os"
	"testing"

	"qqtang/internal/game/mapdata"
)

func TestAdventureStageBuildsAOneTeamEngineWithoutTraps(t *testing.T) {
	root := os.Getenv("QQT_TEST_CLIENT_ROOT")
	if root == "" {
		t.Skip("QQT_TEST_CLIENT_ROOT is not set")
	}
	catalog, err := mapdata.LoadCatalog(root)
	if err != nil {
		t.Fatal(err)
	}
	entry, _ := catalog.Map(1601)
	field, err := mapdata.LoadAdventureBattlefield(root, entry)
	if err != nil {
		t.Fatal(err)
	}
	p, err := ParticipantFromNativeRole(1, 1, 1, ParticipantHuman)
	if err != nil {
		t.Fatal(err)
	}
	p.BombResumeMS = 2000
	config, err := ConfigFromAdventureStage(field, AdventureConfigOptions{ItemSeed: 7, GameTimeSeconds: 600, Rules: Rules{TickMS: 20},
		Participants: []Participant{p}, WallItems: []mapdata.CompetitiveWallItem{{SceneID: 30067, Quantity: 3}}})
	if err != nil {
		t.Fatal(err)
	}
	e := mustEngine(t, config)
	if e.Grid().Width != 40 || e.rules.RoundDurationMS != 600_000 || len(e.Pickups()) != 3 {
		t.Fatalf("grid %dx%d round %d pickups %d", e.Grid().Width, e.Grid().Height, e.rules.RoundDurationMS, len(e.Pickups()))
	}
	spawn := e.Actors()[0].Position.Cell()
	if spawn != (Cell{Row: int16(field.SpawnGroupA[0].Row), Col: int16(field.SpawnGroupA[0].Col)}) {
		t.Fatalf("spawn %+v", spawn)
	}
}

func TestAdventureFlamesDoNotTrapAndBubblesComeBack(t *testing.T) {
	cfg := testConfig()
	cfg.Rules.NativeAdventure, cfg.Rules.ExternalConclusion = true, true
	for i := range cfg.Participants {
		cfg.Participants[i].TeamID = 1
		cfg.Participants[i].BombCapacity, cfg.Participants[i].BombResumeMS = 1, 400
	}
	cfg.Rules.BombFuseMS = 3000
	for range cfg.Participants {
	}
	e := mustEngine(t, cfg)
	id := e.Actors()[0].PlayerID
	place := func() bool {
		events, err := e.Step([]Action{{PlayerID: id, PlaceBomb: true}})
		if err != nil {
			t.Fatal(err)
		}
		for _, ev := range events {
			if ev.Kind == EventBombPlaced {
				return true
			}
		}
		return false
	}
	if !place() {
		t.Fatal("first bubble refused")
	}
	if place() {
		t.Fatal("second bubble before the refill")
	}
	for i := 0; i < 25; i++ { // > 400 ms without placing
		e.Step(nil)
	}
	e.actors[0].Position = PositionAtCellCenter(Cell{Row: e.actors[0].Position.Cell().Row, Col: e.actors[0].Position.Cell().Col + 1})
	if !place() {
		t.Fatal("refilled bubble refused")
	}
	for i := 0; i < 250; i++ {
		e.Step(nil)
	}
	for _, a := range e.Actors() {
		if a.State == ActorTrapped || a.State == ActorEliminated {
			t.Fatalf("player %d was trapped by a flame", a.PlayerID)
		}
	}
}

func TestBattlefieldLockKeepsTheActorInside(t *testing.T) {
	cfg := testConfig()
	cfg.Grid = testOpenGrid(8, 3)
	cfg.Rules.NativeAdventure, cfg.Rules.ExternalConclusion = true, true
	e := mustEngine(t, cfg)
	if err := e.SetActorFieldLock(1, 0, 0, 3, 3); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 60; i++ {
		e.Step([]Action{{PlayerID: 1, Move: DirectionRight}})
	}
	if col := e.Actors()[0].Position.Cell().Col; col != 2 {
		t.Fatalf("locked actor reached column %d", col)
	}
	e.SetActorFieldLock(1, 0, 0, 0, 0)
	for i := 0; i < 60; i++ {
		e.Step([]Action{{PlayerID: 1, Move: DirectionRight}})
	}
	if col := e.Actors()[0].Position.Cell().Col; col <= 2 {
		t.Fatalf("released actor stayed at column %d", col)
	}
}

func TestAdventureSkillAttributesAddAndRestore(t *testing.T) {
	cfg := testConfig()
	if err := mustEngine(t, cfg).AdjustAdventureAttributes(cfg.Participants[0].PlayerID, -1, 0, 0); err == nil {
		t.Fatal("skill attributes outside the adventure rule")
	}
	cfg.Rules.NativeAdventure, cfg.Rules.ExternalConclusion = true, true
	cfg.Rules.SpeedPixelsPerSecondByRate = nativeSpeedPixelsPerSecondByRate
	for i := range cfg.Participants {
		cfg.Participants[i].TeamID = 1
		cfg.Participants[i].SpeedRate, cfg.Participants[i].BombCapacity, cfg.Participants[i].BombPower = 3, 2, 4
		cfg.Participants[i].SpeedPixelsPerSecond = nativeSpeedPixelsPerSecondByRate[3]
	}
	e := mustEngine(t, cfg)
	a := &e.actors[0]
	if err := e.AdjustAdventureAttributes(a.PlayerID, -2, -50, -2); err != nil {
		t.Fatal(err)
	}
	c := e.actorCapabilities(a, DirectionRight)
	if c.EffectiveSpeedRate != 1 || c.EffectiveBombCapacity != 0 || e.adventureBombPower(a) != 1 {
		t.Fatalf("speed %d count %d power %d", c.EffectiveSpeedRate, c.EffectiveBombCapacity, e.adventureBombPower(a))
	}
	if _, ok := e.prospectiveBomb(0, true); ok {
		t.Fatal("placed a bubble with count 0")
	}
	_ = e.AdjustAdventureAttributes(a.PlayerID, -2, 0, 0)
	if e.effectiveSpeedPixelsPerSecond(a, DirectionRight) != 0 {
		t.Fatal("speed rate 0 still moves")
	}
	_ = e.AdjustAdventureAttributes(a.PlayerID, 4, 50, 2)
	c = e.actorCapabilities(a, DirectionRight)
	if c.EffectiveSpeedRate != 3 || c.EffectiveBombCapacity != 2 || e.adventureBombPower(a) != 4 {
		t.Fatalf("restored speed %d count %d power %d", c.EffectiveSpeedRate, c.EffectiveBombCapacity, e.adventureBombPower(a))
	}
}
