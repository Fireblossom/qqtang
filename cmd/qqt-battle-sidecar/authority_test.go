package main

import (
	"qqtang/internal/game/battleengine"
	"qqtang/internal/game/mapdata"
	"qqtang/internal/protocol/game"
	"strconv"
	"testing"
)

func TestNewArbitratorKeepsDeadPlayerRespawnDeadline(t *testing.T) {
	m := treasureMatch(t, 2)
	events, err := m.runtime.AcceptNativeElimination(1, 2, nil)
	if err != nil {
		t.Fatal(err)
	}
	m.noteEliminations(events)
	advanceTo(t, m, 3000)
	before := m.engine.ElapsedMS()
	died := m.eliminatedAt[1]
	m.changeArbitrator(1)
	if m.arbitrator != 1 || m.engine.ElapsedMS() != before || m.eliminatedAt[1] != died {
		t.Fatal("switch reset death/scene state")
	}
	advanceTo(t, m, 5000)
	if got := m.respawnDue(); len(got) != 1 {
		t.Fatalf("new arbitrator did not relive existing death: %+v", got)
	}
}
func TestKickFollowerKeepsBatchDeadlineAndRemainingPool(t *testing.T) {
	m := treasureMatch(t, 2)
	m.kick = newKickRule(mapdata.CompetitiveMap{ID: 704}, 1)
	// An empty batch still moves the scheduling boundary.
	if events := m.dropBatch(game.DispatchBombData{PlayerID: 2, Time: 60000}); len(events) != 0 {
		t.Fatal(events)
	}
	m.kick.observeItems([]game.DispatchBombItem{{ItemID: 24}})
	if m.kick.lastBatch != 60000 || m.kick.pool[3].left != 3 {
		t.Fatalf("follower state %+v", m.kick)
	}
	m.changeArbitrator(1)
	if m.kick.due(60000) {
		t.Fatal("new arbitrator restarted airdrop schedule")
	}
}
func TestDepartedPlayersLeaveUDPRecipientsAndNeverRelive(t *testing.T) {
	m := treasureMatch(t, 1)
	m.depart(2)
	if _, ok := m.uins[2]; ok {
		t.Fatal("departed player remains UDP target")
	}
	if len(m.peerIDs()) != 0 {
		t.Fatal("departed peer remains rendezvous target")
	}
	m.changeArbitrator(2)
	if m.arbitrator != 1 {
		t.Fatal("departed player became arbitrator")
	}
}
func TestSculptureDepartureReturnsFragmentThroughBird(t *testing.T) {
	for material := int16(12); material <= 16; material++ {
		t.Run(strconv.Itoa(int(material)), func(t *testing.T) {
			rule := sculptureTestRule(t)
			cfg := battleengine.Config{Seed: 1, Grid: rule.engine.Grid(), Rules: battleengine.Rules{TickMS: 20, RoundDurationMS: 240000, BombFuseMS: 3000, FlameDurationMS: 500, ActorHalfSizePixels: 19, ExternalConclusion: true, LooseObjectSceneIDs: [battleengine.MaxLooseObjectKinds]uint32{161, 162, 163, 164, 165}}, Participants: []battleengine.Participant{rule.engine.Actors()[0].Participant, rule.engine.Actors()[1].Participant}}
			engine, err := battleengine.New(cfg)
			if err != nil {
				t.Fatal(err)
			}
			rule.engine = engine
			rule.carried[1] = material
			rule.onDeparture(1)
			rule.onDeparture(1)
			m := &onlineMatch{engine: engine, sculpture: rule, log: discardLog{}}
			for engine.ElapsedMS() < 35000 {
				if _, err = engine.Step(nil); err != nil {
					t.Fatal(err)
				}
			}
			before := len(rule.loose)
			m.collectLooseDispatches()
			if len(rule.loose) != before+1 || int16(rule.loose[len(rule.loose)-1].material) != material || rule.carrying(1) != bunNone {
				t.Fatalf("recycled fragments %+v", rule.loose)
			}
			landed := rule.loose[len(rule.loose)-1]
			actor := sculptureActor(2, 1, landed.cell)
			schema, request, wanted := rule.wanted(actor)
			if !wanted || schema != game.RequestGetBun || !rule.allowTake(request, actor) {
				t.Fatal("landed fragment cannot be picked up again")
			}
			rule.applyTake(request)
			if rule.carrying(2) != material {
				t.Fatal("landed special material changed on pickup")
			}
		})
	}
}

type discardLog struct{}

func (discardLog) Write(p []byte) (int, error) { return len(p), nil }

func TestPromotedArbitratorKeepsConfirmedPickupDeduplication(t *testing.T) {
	m := treasureMatch(t, 2)
	pos := battleengine.PositionAtCellCenter(battleengine.Cell{Row: 1, Col: 2})
	body, _ := (game.PlayerItemEvent{PlayerID: 1, ClientTime: 10, ItemID: 152, PosX: uint16(pos.X), PosY: uint16(pos.Y)}).MarshalNetworkBinary()
	m.applyRemote(2, game.BattleMessageData{DataID: uint32(game.NotifyPlayerGetItem), Data: body})
	m.changeArbitrator(1)
	if got := m.arbitratePickup(game.GameEvent{Schema: game.RequestGetItem, Body: body}); len(got) != 0 {
		t.Fatalf("replayed an old authority grant: %+v", got)
	}
	actor, _ := actorByID(m.engine.Actors(), 1)
	if actor.CarriedObjectives[2] != 1 {
		t.Fatal("duplicate request manufactured second gem")
	}
}
func TestPromotedArbitratorResolvesItsOwnPendingPickup(t *testing.T) {
	m := treasureMatch(t, 2)
	pos := battleengine.PositionAtCellCenter(battleengine.Cell{Row: 1, Col: 2})
	m.runtime.ReconcileHumanMovement(1, pos)
	m.requestPickup(152, pos.Cell(), pos)
	m.changeArbitrator(1)
	actor, _ := actorByID(m.engine.Actors(), 1)
	if actor.CarriedObjectives[2] != 1 || len(m.pickupRequests) != 0 {
		t.Fatal("local request was stranded at old authority")
	}
}

func TestNewArbitratorDoesNotWrapFutureDeathClock(t *testing.T) {
	m := treasureMatch(t, 2)
	events, err := m.runtime.AcceptNativeElimination(1, 2, nil)
	if err != nil {
		t.Fatal(err)
	}
	for i := range events {
		if events[i].Kind == battleengine.EventActorEliminated {
			events[i].TimeMS = 20
		}
	}
	m.noteEliminations(events)
	m.changeArbitrator(1)
	if got := m.respawnDue(); len(got) != 0 {
		t.Fatal("future sender clock caused immediate respawn")
	}
	advanceTo(t, m, 5020)
	if got := m.respawnDue(); len(got) != 1 {
		t.Fatal("wire death deadline was not preserved")
	}
}

func TestBirdWireKeepsFullDelayedArrayForAuthorityMirrors(t *testing.T) {
	m := treasureMatch(t, 1)
	var events []battleengine.Event
	for i := 0; i < 70; i++ {
		event := battleengine.Event{Kind: battleengine.EventPickupDispatched, TimeMS: 60000, SceneID: 1, Cell: battleengine.Cell{Row: int16(i / 15), Col: int16(i % 15)}}
		if i >= 60 {
			event.SceneID = 2
			event.ScheduledDueMS = 60000
		}
		events = append(events, event)
	}
	m.publishEvents(m.engine, events)
	packets := mirrored(t, m, game.NotifyDispatchItem)
	if len(packets) != 1 {
		t.Fatalf("bird packets %d", len(packets))
	}
	wire, err := game.ParseDispatchItemEvent(packets[0])
	if err != nil {
		t.Fatal(err)
	}
	if len(wire.Items) != 60 || len(wire.DelayedItems) != 10 || wire.DelayedItems[9].DispatchTime != 60000 {
		t.Fatalf("lost serialized delayed identities: %+v", wire)
	}
}
