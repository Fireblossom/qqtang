package main

import (
	"io"
	"testing"

	"qqtang/internal/game/battleengine"
	"qqtang/internal/game/mapdata"
	"qqtang/internal/protocol/game"
)

// kickMatch is local player 1 against player 2 on an open 15x13 field under
// the rule-2 options.
func kickMatch(t *testing.T, arbitrator uint16) *onlineMatch {
	t.Helper()
	grid := battleengine.Grid{Width: 15, Height: 13, Cells: make([]battleengine.Tile, 15*13)}
	for index := range grid.Cells {
		grid.Cells[index].FlamePassable = true
	}
	participant := func(id uint16, team byte, cell battleengine.Cell) battleengine.Participant {
		return battleengine.Participant{PlayerID: id, TeamID: team, Source: battleengine.ParticipantHuman, Spawn: cell,
			SpeedPixelsPerSecond: 160, BombCapacity: 1, BombPower: 1}
	}
	entry := mapdata.CompetitiveMap{ID: 704, Rule: mapdata.CompetitiveRuleKickBomb}
	rules, respawn, err := ruleOptions(entry, 1, arbitrator)
	if err != nil || respawn != nil {
		t.Fatalf("rule 2 options: respawn %v, %v", respawn, err)
	}
	rules.RoundDurationMS, rules.BombFuseMS, rules.FlameDurationMS, rules.ActorHalfSizePixels = 240_000, 3000, 500, 19
	engine, err := battleengine.New(battleengine.Config{
		Seed: 1, Grid: grid, Rules: rules,
		Participants: []battleengine.Participant{participant(1, 1, battleengine.Cell{Row: 6, Col: 11}), participant(2, 2, battleengine.Cell{Row: 1, Col: 1})},
	})
	if err != nil {
		t.Fatal(err)
	}
	runtime, err := battleengine.NewRuntime(engine, nil)
	if err != nil {
		t.Fatal(err)
	}
	match := &onlineMatch{
		runtime: runtime, engine: engine, local: 1, localUIN: 1000001, arbitrator: arbitrator,
		uins: map[uint16]uint32{1: 1000001, 2: 1000002}, trapMS: defaultTrapMS, log: io.Discard,
		peerReady: map[uint16]bool{}, peerAnswered: map[uint16]bool{}, lastNumber: map[uint16]uint32{}, bombs: map[uint32]wireBomb{},
		collected: map[pickupKey]uint16{}, fieldContacts: map[fieldContactKey]bool{}, itemUses: map[[3]uint32]bool{},
		useRequested: map[uint8]uint32{}, preparedRequested: map[uint32]uint32{},
		pickupRequests: map[pickupKey]pickupRequest{}, sceneRequests: map[sceneRequestKey]uint32{},
		eliminatedAt: map[uint16]uint32{},
		kick:         newKickRule(entry, 1),
	}
	return match
}

func TestKickBatchesFollowTutorial3(t *testing.T) {
	// Batch times of Tutorial3.qbv. Its frames are about 16 ms apart and the
	// original times each batch up to a frame after sending it; on 20 ms
	// ticks the early batches come up to 50 ms sooner.
	recorded := []uint32{5000, 12469, 19094, 25000, 30250, 34907, 39032, 43032, 47032, 51032, 55032, 59032}
	rule := newKickRule(mapdata.CompetitiveMap{ID: 704}, 1)
	var batches []uint32
	for now := uint32(3000); len(batches) < len(recorded); now += tickMS {
		if rule.due(now) {
			batches = append(batches, now)
			rule.applied(now)
		}
	}
	for index, want := range recorded {
		if got := batches[index]; got+60 < want || got > want {
			t.Fatalf("batch %d at %d, recorded %d (all %v)", index, got, want, batches)
		}
	}
}

func TestKickBatchContents(t *testing.T) {
	match := kickMatch(t, 1)
	for _, test := range []struct {
		now           uint32
		normal, power int
	}{{5000, 5, 1}, {40_000, 7, 3}, {119_000, 12, 8}} {
		data := match.kick.batch(match.engine, test.now)
		normal, power := 0, 0
		seen := map[[2]byte]bool{}
		for _, bomb := range data.Bombs {
			switch {
			case bomb.SceneID == 1 && bomb.Prop == 2:
				normal++
			case bomb.SceneID == 2 && bomb.Prop == 5:
				power++
			default:
				t.Fatalf("bubble %+v", bomb)
			}
			seen[[2]byte{bomb.Row, bomb.Col}] = true
		}
		for _, item := range data.Items {
			seen[[2]byte{item.Row, item.Col}] = true
		}
		if normal != test.normal || power != test.power || len(data.Items) != 1 || len(seen) != normal+power+1 {
			t.Fatalf("at %d: %d normal, %d power, items %v, %d cells", test.now, normal, power, data.Items, len(seen))
		}
	}
	early := newKickRule(mapdata.CompetitiveMap{ID: 701}, 1)
	for draw := 0; draw < 100; draw++ {
		switch item := early.drawItem(9000); item {
		case 81, 27:
			t.Fatalf("drew %d before 10 s or off maps 703/704", item)
		}
	}
}

func TestKickBatchOnBothSides(t *testing.T) {
	arbitrator := kickMatch(t, 1)
	for arbitrator.engine.ElapsedMS() < kickBatchFirstMS {
		if _, err := arbitrator.runtime.Step(nil); err != nil {
			t.Fatal(err)
		}
	}
	events := arbitrator.kickTick()
	if len(arbitrator.engine.Bombs()) != 6 || len(events) != 7 {
		t.Fatalf("bubbles %v, events %v", arbitrator.engine.Bombs(), events)
	}
	sent := mirrored(t, arbitrator, game.NotifyDispatchBomb)
	if len(sent) != 1 || len(arbitrator.outbound) != 1 {
		t.Fatalf("mirrored %v, queued %v", sent, arbitrator.outbound)
	}
	batch, err := game.ParseDispatchBombEvent(sent[0])
	if err != nil || batch.PlayerID != 1 || batch.Time != kickBatchFirstMS {
		t.Fatalf("batch %+v, %v", batch, err)
	}
	for _, bomb := range arbitrator.engine.Bombs() {
		if known := arbitrator.bombs[bomb.ID]; known.owner != 1 || known.placedAt != batch.Time || known.cell != bomb.Cell || bomb.OwnerID != 1 {
			t.Fatalf("bubble %+v named %+v", bomb, known)
		}
	}
	if arbitrator.kickTick() != nil {
		t.Fatal("a second batch at once")
	}

	peer := kickMatch(t, 2)
	peer.applyRemote(1, game.BattleMessageData{DataID: game.NotifyDispatchBomb, Data: sent[0].Body})
	if len(peer.engine.Bombs()) != 0 {
		t.Fatal("a batch from a non-arbitrator was applied")
	}
	var body []byte
	batch.PlayerID = 2
	if body, err = batch.MarshalNetworkBinary(); err != nil {
		t.Fatal(err)
	}
	peer.applyRemote(2, game.BattleMessageData{DataID: game.NotifyDispatchBomb, Data: body})
	bombs := peer.engine.Bombs()
	if len(bombs) != 6 {
		t.Fatalf("peer bubbles %v", bombs)
	}
	for index, bomb := range bombs {
		wire := batch.Bombs[index]
		if bomb.OwnerID != 2 || bomb.Cell != (battleengine.Cell{Row: int16(wire.Row), Col: int16(wire.Col)}) || bomb.Power != wire.Prop-1 || bomb.ExplodeAtMS != batch.Time+3001 {
			t.Fatalf("peer bubble %+v from %+v", bomb, wire)
		}
		if id, found := peer.wireBombID(2, batch.Time, bomb.Cell); !found || id != bomb.ID {
			t.Fatalf("bubble %+v is not named by its batch", bomb)
		}
	}
}

func TestWrappingKickRequestKeepsTheKickersDirection(t *testing.T) {
	match := kickMatch(t, 2)
	if _, err := match.engine.DropRuleBatch(2, match.engine.ElapsedMS(), []battleengine.RuleBomb{{Cell: battleengine.Cell{Row: 6, Col: 12}, Power: 1, FromCol: 12}}, nil); err != nil {
		t.Fatal(err)
	}
	bomb := match.engine.Bombs()[0]
	match.bombs[bomb.ID] = wireBomb{owner: 2, placedAt: 3000, cell: bomb.Cell}
	if err := match.engine.ApplyVerifiedMovementCheckpoint(1, battleengine.Position{X: 11*40 + 29, Y: 6*40 + 20}); err != nil {
		t.Fatal(err)
	}
	events, err := match.runtime.Step([]battleengine.Action{{PlayerID: 1, Move: battleengine.DirectionRight}})
	if err != nil {
		t.Fatal(err)
	}
	var request battleengine.Event
	for _, event := range events {
		if event.Kind == battleengine.EventBombKickRequested {
			request = event
		}
	}
	if request.Cell != (battleengine.Cell{Row: 6, Col: 5}) {
		t.Fatalf("kick request %+v", request)
	}
	body, ok := match.bombMoveBody(match.engine, request)
	if !ok {
		t.Fatal("no kick body")
	}
	kick, err := game.ParseMoveBombEvent(game.GameEvent{Schema: game.RequestMoveBomb, Body: body})
	if err != nil || kick.Direction != 0 || kick.ToCol != 5 || kick.BombPower != 2 {
		t.Fatalf("kick %+v, %v", kick, err)
	}
}
