package main

import (
	"os"
	"qqtang/internal/game/battleengine"
	"qqtang/internal/game/mapdata"
	"qqtang/internal/protocol/game"
	"testing"
)

func tankTestMatch(t *testing.T, local uint16) *onlineMatch {
	m := treasureMatch(t, 1)
	m.local = local
	m.tank = &tankRule{bases: [2]tankBase{{team: 1, anchor: battleengine.Cell{Row: 0, Col: 0}, hp: 100}, {team: 2, anchor: battleengine.Cell{Row: 0, Col: 2}, hp: 100}}}
	return m
}
func TestTankBaseHitsUseCentreAndMirrorBothTeams(t *testing.T) {
	owner, peer := tankTestMatch(t, 1), tankTestMatch(t, 2)
	explosion := game.BombExplodeEvent{PlayerID: 1, ClientTime: 5000, Bombs: []game.ExplodedBomb{{PlayerID: 1, Row: 1, Column: 2, RowMin: 1, RowMax: 1, ColumnMin: 0, ColumnMax: 4}}}
	owner.tankExplosion(&explosion, true)
	peer.tankExplosion(&explosion, false)
	if len(explosion.MapElems) != 2 {
		t.Fatalf("base entries: %+v", explosion.MapElems)
	}
	for i := range owner.tank.bases {
		if owner.tank.bases[i].hp != 90 || peer.tank.bases[i].hp != 90 {
			t.Fatalf("base %d differs: %+v %+v", i, owner.tank.bases, peer.tank.bases)
		}
	}
	reports := mirrored(t, owner, game.RequestTankBaseHP)
	if len(reports) != 2 || len(mirrored(t, peer, game.RequestTankBaseHP)) != 0 {
		t.Fatal("only arbitrator must report both bases")
	}
	for _, report := range reports {
		hp, err := game.ParseTankBaseHPChangeEvent(report)
		if err != nil || hp.PlayerID != 1 || hp.ChangeHP != -10 {
			t.Fatalf("report %+v %v", hp, err)
		}
	}
	// A ray along the edge rather than centre does not damage the base.
	explosion.Bombs[0].Row = 0
	explosion.Bombs[0].Column = 0
	explosion.Bombs[0].RowMin = 0
	explosion.Bombs[0].RowMax = 0
	explosion.MapElems = nil
	owner.tankExplosion(&explosion, true)
	if owner.tank.bases[0].hp != 90 || len(explosion.MapElems) != 0 {
		t.Fatal("edge damaged base")
	}
}
func TestTankRepairArmorAndDestroyedBase(t *testing.T) {
	m := tankTestMatch(t, 1)
	m.tank.bases[0].hp = 95
	m.tankPickup(battleengine.Event{Kind: battleengine.EventPickupCollected, PlayerID: 1, SceneID: tankRepairScene, TimeMS: 4000})
	if m.tank.bases[0].hp != 100 {
		t.Fatal("repair did not clamp")
	}
	reports := mirrored(t, m, game.RequestTankBaseHP)
	if len(reports) != 1 {
		t.Fatal("repair report missing")
	}
	m.tankPickup(battleengine.Event{Kind: battleengine.EventPickupCollected, PlayerID: 1, SceneID: tankArmorScene, TimeMS: 5000})
	explosion := game.BombExplodeEvent{ClientTime: 15000, Bombs: []game.ExplodedBomb{{PlayerID: 2, Row: 1, Column: 1, RowMin: 1, RowMax: 1, ColumnMin: 1, ColumnMax: 1}}}
	m.tankExplosion(&explosion, true)
	if m.tank.bases[0].hp != 95 {
		t.Fatal("armor expired before native strict boundary")
	}
	explosion.ClientTime++
	m.tankExplosion(&explosion, true)
	if m.tank.bases[0].hp != 85 {
		t.Fatal("armor did not expire")
	}
	m.tank.bases[0].hp = 0
	m.tankPickup(battleengine.Event{Kind: battleengine.EventPickupCollected, PlayerID: 1, SceneID: tankRepairScene, TimeMS: 16000})
	if m.tank.bases[0].hp != 0 {
		t.Fatal("destroyed base repaired")
	}
}
func TestTankDestroyedBaseStopsRelive(t *testing.T) {
	m := tankTestMatch(t, 1)
	events, err := m.runtime.AcceptNativeElimination(2, 0, nil)
	if err != nil {
		t.Fatal(err)
	}
	m.noteEliminations(events)
	advanceTo(t, m, m.eliminatedAt[2]+5000)
	m.tank.bases[1].hp = 0
	if events := m.respawnDue(); len(events) != 0 {
		t.Fatalf("destroyed base relived player: %+v", events)
	}
	m.tank.bases[1].hp = 10
	if events := m.respawnDue(); len(events) != 1 {
		t.Fatalf("intact base did not relive player: %+v", events)
	}
}
func TestTankMapsBuildWithNativeBasesAndSupplies(t *testing.T) {
	root := os.Getenv("QQT_TEST_CLIENT_ROOT")
	if root == "" {
		t.Skip("set QQT_TEST_CLIENT_ROOT for installed map validation")
	}
	catalog, err := mapdata.LoadCatalog(root)
	if err != nil {
		t.Fatal(err)
	}
	for id := uint32(1701); id <= 1708; id++ {
		entry, ok := catalog.CompetitiveMap(id)
		if !ok {
			t.Fatalf("missing map %d", id)
		}
		rules, _, err := ruleOptions(entry, 1, 1)
		if err != nil {
			t.Fatal(err)
		}
		a, _ := battleengine.ParticipantFromNativeRole(1, 7, 1, battleengine.ParticipantHuman)
		b, _ := battleengine.ParticipantFromNativeRole(2, 8, 2, battleengine.ParticipantHuman)
		supply := entry.PlanCompetitiveSupply(123, []uint16{7, 8}, false)
		config, err := battleengine.ConfigFromCompetitiveMap(entry, battleengine.CompetitiveMapConfigOptions{SpawnSeed: 7, ItemSeed: 123, SpawnMode: battleengine.NativeSpawnModeForMap(entry, false), Rules: rules, Participants: []battleengine.Participant{a, b}, RecordedWallItems: supply.WallItems, RecordedDelayedItems: supply.DelayedItems, UseRecordedWallItems: true, AllowSpecialRules: true})
		if err != nil {
			t.Fatalf("map %d: %v", id, err)
		}
		rule := newTankRule(entry, game.GameBeginData{Players: []game.PlayerGameInfo{{PlayerID: 1, TeamID: 1}, {PlayerID: 2, TeamID: 2}}})
		installTankBases(&config, rule)
		if _, err = battleengine.New(config); err != nil {
			t.Fatalf("map %d engine: %v", id, err)
		}
		for _, base := range rule.bases {
			corner, _ := config.Grid.Cell(base.anchor)
			centre, _ := config.Grid.Cell(base.centre())
			if corner.Kind != battleengine.CellSolid || centre.Kind != battleengine.CellOpen || !centre.FlamePassable {
				t.Fatalf("map %d bad base", id)
			}
		}
	}
}

func TestTankArmorUsesWirePickupClock(t *testing.T) {
	m := treasureMatch(t, 2)
	m.tank = &tankRule{bases: [2]tankBase{{team: 1, hp: 100}, {team: 2, hp: 100}}}
	rules, respawn, err := ruleOptions(mapdata.CompetitiveMap{Rule: mapdata.CompetitiveRuleTank}, 1, 2)
	if err != nil {
		t.Fatal(err)
	}
	rules.RoundDurationMS, rules.BombFuseMS, rules.FlameDurationMS, rules.ActorHalfSizePixels = 240000, 3000, 500, 19
	actor := m.engine.Actors()[0]
	grid := m.engine.Grid()
	m.engine, err = battleengine.New(battleengine.Config{Seed: 1, Grid: grid, Rules: rules, Participants: []battleengine.Participant{actor.Participant, {PlayerID: 2, TeamID: 2, Source: battleengine.ParticipantHuman, Spawn: battleengine.Cell{Row: 1, Col: 4}, SpeedPixelsPerSecond: 80, BombCapacity: 2, BombPower: 2}}, Pickups: []battleengine.Pickup{{SceneID: tankArmorScene, Cell: actor.Position.Cell(), State: battleengine.PickupAvailable}}})
	if err != nil {
		t.Fatal(err)
	}
	m.runtime, _ = battleengine.NewRuntime(m.engine, nil)
	m.respawn = respawn
	advanceTo(t, m, 6000)
	body, _ := (game.PlayerItemEvent{PlayerID: 1, ClientTime: 4200, ItemID: tankArmorScene, PosX: uint16(actor.Position.X), PosY: uint16(actor.Position.Y)}).MarshalNetworkBinary()
	events := m.applyRemote(2, game.BattleMessageData{DataID: uint32(game.NotifyPlayerGetItem), Data: body})
	if len(events) != 1 || m.tank.bases[0].armorUntil != 14200 {
		t.Fatalf("wire clock lost: events=%+v armor=%d local=%d", events, m.tank.bases[0].armorUntil, m.engine.ElapsedMS())
	}
}
