package main

import (
	"testing"

	"qqtang/internal/game/battleengine"
)

// botEngine is player 1 alone on an open 7x5 field against a distant player
// 2, with the given breakable cells and pickups, and real bubble timing.
func botEngine(t *testing.T, walls []battleengine.Cell, pickups []battleengine.Pickup) *battleengine.Engine {
	t.Helper()
	grid := battleengine.Grid{Width: 7, Height: 5, Cells: make([]battleengine.Tile, 35)}
	for index := range grid.Cells {
		grid.Cells[index].FlamePassable = true
	}
	for _, cell := range walls {
		grid.Cells[int(cell.Row)*7+int(cell.Col)] = battleengine.Tile{Kind: battleengine.CellBreakable, MapElementOccupied: true, Durability: 1}
	}
	participant := func(id uint16, team byte, cell battleengine.Cell) battleengine.Participant {
		return battleengine.Participant{PlayerID: id, TeamID: team, Source: battleengine.ParticipantHuman, Spawn: cell,
			SpeedPixelsPerSecond: 160, BombCapacity: 1, BombPower: 1}
	}
	rules, _, err := ruleOptions(treasureMapForTest(), 1, 1)
	if err != nil {
		t.Fatal(err)
	}
	rules.RoundDurationMS, rules.BombFuseMS, rules.FlameDurationMS, rules.ActorHalfSizePixels = 180_000, 3000, 500, 19
	engine, err := battleengine.New(battleengine.Config{
		Seed: 1, Grid: grid, Rules: rules, Pickups: pickups,
		Participants: []battleengine.Participant{participant(1, 1, battleengine.Cell{Row: 2, Col: 1}), participant(2, 2, battleengine.Cell{Row: 4, Col: 6})},
	})
	if err != nil {
		t.Fatal(err)
	}
	return engine
}

func runBot(t *testing.T, engine *battleengine.Engine, bot *autoplayBot, untilMS uint32) []battleengine.Event {
	t.Helper()
	var events []battleengine.Event
	for engine.ElapsedMS() < untilMS {
		stepEvents, err := engine.Step([]battleengine.Action{bot.act(engine)})
		if err != nil {
			t.Fatal(err)
		}
		events = append(events, stepEvents...)
	}
	return events
}

func TestBotCollectsVisibleGem(t *testing.T) {
	gem := battleengine.Cell{Row: 0, Col: 5}
	engine := botEngine(t, nil, []battleengine.Pickup{{SceneID: 152, Cell: gem, State: battleengine.PickupAvailable}})
	runBot(t, engine, newAutoplayBot(1), 6000)
	actor, _ := actorByID(engine.Actors(), 1)
	if actor.CarriedObjectives[2] != 1 {
		t.Fatalf("carried = %v at %+v", actor.CarriedObjectives, actor.Position)
	}
}

func TestBotBombsWallAndSurvives(t *testing.T) {
	wall := battleengine.Cell{Row: 2, Col: 3}
	engine := botEngine(t, []battleengine.Cell{wall}, nil)
	events := runBot(t, engine, newAutoplayBot(1), 12_000)
	placed, destroyed := 0, 0
	for _, event := range events {
		switch event.Kind {
		case battleengine.EventBombPlaced:
			placed++
		case battleengine.EventCellDestroyed:
			destroyed++
		case battleengine.EventActorTrapped:
			t.Fatalf("the bot trapped itself at %d", event.TimeMS)
		}
	}
	if placed == 0 || destroyed == 0 {
		t.Fatalf("placed %d bubbles, destroyed %d walls", placed, destroyed)
	}
}
