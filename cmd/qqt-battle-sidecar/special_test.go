package main

import (
	"io"
	"os"
	"testing"

	"qqtang/internal/game/battleengine"
	"qqtang/internal/game/mapdata"
	"qqtang/internal/protocol/game"
)

func TestDoorShuffleGivesMap115ThreePairings(t *testing.T) {
	doors := []battleengine.Cell{{Row: 1, Col: 1}, {Row: 1, Col: 13}, {Row: 11, Col: 1}, {Row: 11, Col: 13}}
	counts := map[[4]int]int{}
	for seed := uint32(0); seed < 0x8000; seed++ {
		rule := &specialRule{}
		for _, c := range doors {
			rule.doors = append(rule.doors, doorState{cell: c, target: -1})
		}
		rule.shuffleDoors(seed)
		var pairing [4]int
		for i, d := range rule.doors {
			pairing[i] = d.target
			if rule.doors[d.target].target != i {
				t.Fatalf("seed %d: one-way pair %v", seed, rule.doors)
			}
		}
		counts[pairing]++
	}
	// docs/transdoor-strip-re.md §4: 10994 / 10938 / 10836 seeds.
	if len(counts) != 3 || counts[[4]int{2, 3, 0, 1}] != 10994 || counts[[4]int{3, 2, 1, 0}] != 10938 || counts[[4]int{1, 0, 3, 2}] != 10836 {
		t.Fatalf("pairings %v", counts)
	}
}

// specialMatch is a two-player 15x13 match on map 115's element layers.
func specialMatch(t *testing.T, arbitrator uint16) *onlineMatch {
	t.Helper()
	root := os.Getenv("QQT_TEST_CLIENT_ROOT")
	if root == "" {
		t.Skip("QQT_TEST_CLIENT_ROOT is not set")
	}
	layers, err := mapdata.LoadMapLayers(root, "field15_8.map")
	if err != nil {
		t.Fatal(err)
	}
	grid := battleengine.Grid{Width: 15, Height: 13, Cells: make([]battleengine.Tile, 15*13)}
	for index := range grid.Cells {
		grid.Cells[index].FlamePassable = true
	}
	rules := onlineRules(1, arbitrator)
	rules.NativeSpecialElements = true
	rules.RoundDurationMS, rules.BombFuseMS, rules.FlameDurationMS, rules.ActorHalfSizePixels = 240_000, 3000, 500, 19
	participant := func(id uint16, cell battleengine.Cell) battleengine.Participant {
		return battleengine.Participant{PlayerID: id, TeamID: byte(id), Source: battleengine.ParticipantHuman, Spawn: cell,
			SpeedPixelsPerSecond: 160, BombCapacity: 1, BombPower: 1, MaxBombCapacity: 6, MaxBombPower: 6}
	}
	engine, err := battleengine.New(battleengine.Config{Seed: 1, Grid: grid, Rules: rules,
		Participants: []battleengine.Participant{participant(1, battleengine.Cell{Row: 1, Col: 0}), participant(2, battleengine.Cell{Row: 6, Col: 7})}})
	if err != nil {
		t.Fatal(err)
	}
	runtime, _ := battleengine.NewRuntime(engine, nil)
	return &onlineMatch{runtime: runtime, engine: engine, local: 1, localUIN: 1000001, arbitrator: arbitrator,
		uins: map[uint16]uint32{1: 1000001, 2: 1000002}, trapMS: defaultTrapMS, log: io.Discard, lastNumber: map[uint16]uint32{},
		bombs: map[uint32]wireBomb{}, collected: map[pickupKey]uint16{}, eliminatedAt: map[uint16]uint32{},
		special: newSpecialRule(layers, 1)}
}

func TestIceTotemSlowsTheLocalPlayerWhileActive(t *testing.T) {
	m := specialMatch(t, 1)
	if len(m.special.totems) != 4 || len(m.special.doors) != 4 {
		t.Fatalf("totems %d doors %d", len(m.special.totems), len(m.special.doors))
	}
	local, _ := actorByID(m.engine.Actors(), 1)
	for m.engine.ElapsedMS() < totemFirstMS+100 {
		advanceTo(t, m, m.engine.ElapsedMS()+20)
		m.specialTick(local, false)
	}
	if rate, _ := m.engine.NativeEffectiveSpeedRate(1, battleengine.DirectionUp); len(m.special.totemRestore) == 0 {
		t.Fatalf("no slow at (1,0) next to the (0,0) totem; rate %d", rate)
	}
	if view := m.specialView(); len(view.Totems) != 4 {
		t.Fatalf("active totems %d", len(view.Totems))
	}
	for m.engine.ElapsedMS() < totemFirstMS+totemActiveMS+totemRestoreMS+100 {
		advanceTo(t, m, m.engine.ElapsedMS()+20)
		m.specialTick(local, false)
	}
	if len(m.special.totemRestore) != 0 {
		t.Fatalf("%d slows left", len(m.special.totemRestore))
	}
}

func TestTeleporterMovesTheLocalPlayerAfterTwoSeconds(t *testing.T) {
	m := specialMatch(t, 1)
	m.special.shuffleDoors(0) // pairs (1,1) with (11,1) [seed 0 -> pairing {2,3,0,1}]
	before, _ := actorByID(m.engine.Actors(), 1)
	if err := m.engine.ApplyVerifiedMovementCheckpoint(1, battleengine.PositionAtCellCenter(battleengine.Cell{Row: 1, Col: 1})); err != nil {
		t.Fatal(err)
	}
	m.special.shuffleAt = 1 << 30
	m.specialTick(before, true)
	if m.special.transit == nil || m.special.transit.exit {
		t.Fatal("no entry")
	}
	action := battleengine.Action{PlayerID: 1, Move: battleengine.DirectionDown, PlaceBomb: true}
	m.specialInput(&action)
	if action.Move != battleengine.DirectionNone || action.PlaceBomb {
		t.Fatal("input not held")
	}
	still, _ := actorByID(m.engine.Actors(), 1)
	advanceTo(t, m, m.engine.ElapsedMS()+doorTransitMS)
	m.specialTick(still, false)
	exit := m.special.doors[m.special.doors[0].target].cell
	if after, _ := actorByID(m.engine.Actors(), 1); after.Position.Cell() != exit || m.special.transit == nil || !m.special.transit.exit {
		t.Fatalf("at %v, want %v", after.Position.Cell(), exit)
	}
}

func TestHaloRequestIsGrantedByTheArbitratorOnce(t *testing.T) {
	m := specialMatch(t, 1)
	m.special.halos = []*haloState{{cell: battleengine.Cell{Row: 6, Col: 8}, bonus: map[uint16]uint32{}}}
	body, _ := (game.MapElementEvent{EventID: game.MapElementHaloRequest, PlayerID: 2, Time: 1, DestRow: 6, DestCol: 8}).MarshalNetworkBinary()
	m.specialRemote(2, game.GameEvent{Schema: game.NotifyMapElement, Body: body})
	m.specialRemote(2, game.GameEvent{Schema: game.NotifyMapElement, Body: body}) // cooling down now
	h := m.special.halos[0]
	if _, ok := h.bonus[2]; !ok || h.cooldownUntil != m.engine.ElapsedMS()+haloCooldownMS {
		t.Fatalf("bonus %v cooldown %d", h.bonus, h.cooldownUntil)
	}
}
