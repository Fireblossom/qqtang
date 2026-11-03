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
