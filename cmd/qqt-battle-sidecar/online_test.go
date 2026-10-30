package main

import (
	"encoding/hex"
	"io"
	"net"
	"testing"
	"time"

	"qqtang/internal/game/battleengine"
	"qqtang/internal/protocol/game"
)

// testMatch is a local player 1 against player 2 on an open 5x3 field, with
// a detonator pickup (scene 28 -> action 64) next to player 1.
func testMatch(t *testing.T) *onlineMatch {
	t.Helper()
	return testMatchAs(t, 1)
}

// testMatchAs is testMatch with the given arbitrator; the engine rules follow
// the local player's role as in a live match.
func testMatchAs(t *testing.T, arbitrator uint16) *onlineMatch {
	t.Helper()
	grid := battleengine.Grid{Width: 5, Height: 3, Cells: make([]battleengine.Tile, 15)}
	for index := range grid.Cells {
		grid.Cells[index].FlamePassable = true
	}
	participant := func(id uint16, team byte, col int16) battleengine.Participant {
		return battleengine.Participant{PlayerID: id, TeamID: team, Source: battleengine.ParticipantHuman, Spawn: battleengine.Cell{Row: 1, Col: col},
			SpeedPixelsPerSecond: 80, BombCapacity: 2, BombPower: 2}
	}
	rules := onlineRules(1, arbitrator)
	rules.RoundDurationMS, rules.BombFuseMS, rules.FlameDurationMS, rules.ActorHalfSizePixels = 240_000, 3000, 500, 19
	engine, err := battleengine.New(battleengine.Config{
		Seed: 1, Grid: grid,
		Rules:        rules,
		Participants: []battleengine.Participant{participant(1, 1, 1), participant(2, 2, 4)},
		Pickups:      []battleengine.Pickup{{SceneID: 28, Cell: battleengine.Cell{Row: 1, Col: 1}, State: battleengine.PickupAvailable}},
	})
	if err != nil {
		t.Fatal(err)
	}
	runtime, err := battleengine.NewRuntime(engine, nil)
	if err != nil {
		t.Fatal(err)
	}
	return &onlineMatch{
		runtime: runtime, engine: engine, local: 1, localUIN: 1000001, arbitrator: arbitrator,
		uins: map[uint16]uint32{1: 1000001, 2: 1900000002}, trapMS: defaultTrapMS, log: io.Discard,
		peerReady: map[uint16]bool{}, peerAnswered: map[uint16]bool{}, lastNumber: map[uint16]uint32{}, bombs: map[uint32]wireBomb{},
		collected: map[pickupKey]uint16{}, fieldContacts: map[fieldContactKey]bool{}, itemUses: map[[3]uint32]bool{},
		useRequested: map[uint8]uint32{}, preparedRequested: map[uint32]uint32{},
		pickupRequests: map[pickupKey]pickupRequest{}, sceneRequests: map[sceneRequestKey]uint32{},
	}
}

func heldCount(match *onlineMatch, action uint8) uint8 {
	actor, _ := actorByID(match.engine.Actors(), match.local)
	for _, slot := range actor.HeldActions {
		if slot.ActionID == action {
			return slot.Count
		}
	}
	return 0
}

func TestItemUseWaitsForServerConfirmation(t *testing.T) {
	match := testMatch(t)
	actor, _ := actorByID(match.engine.Actors(), 1)
	if _, err := match.runtime.AcceptNativePickup(1, 28, actor.Position); err != nil {
		t.Fatal(err)
	}
	if heldCount(match, 64) != 1 {
		t.Fatalf("detonator not held after pickup")
	}

	match.requestItemUse(64)
	requests := match.takeTCP()
	if len(requests) != 1 || requests[0].Command != requestPlayCommand {
		t.Fatalf("requests = %+v, want one REQUEST_PLAY", requests)
	}
	payload, _ := hex.DecodeString(requests[0].Payload)
	event, err := game.ParseGameEventPayload(payload)
	if err != nil || event.Schema != game.RequestUseItem {
		t.Fatalf("request = %+v, %v; want 0x0FAF", event, err)
	}
	use, err := game.ParseWorldUseItemEvent(event)
	if err != nil || use.PlayerID != 1 || use.ItemID != 64 {
		t.Fatalf("use = %+v, %v", use, err)
	}
	if heldCount(match, 64) != 1 {
		t.Fatal("item consumed before the server confirmed it")
	}
	match.requestItemUse(64)
	if len(match.takeTCP()) != 0 {
		t.Fatal("a pending use was requested twice")
	}

	match.applyItemUse(event.Body) // the server's 0x0FB0 echo
	if heldCount(match, 64) != 0 {
		t.Fatal("confirmed use did not consume the item")
	}
	if _, pending := match.useRequested[64]; pending {
		t.Fatal("confirmed use is still pending")
	}
}

func TestNeedleOnlyFromInsideABubble(t *testing.T) {
	match := testMatch(t)
	match.requestItemUse(63) // not held, not trapped
	match.requestItemUse(64) // not held
	if requests := match.takeTCP(); len(requests) != 0 {
		t.Fatalf("requests without held items: %+v", requests)
	}
}

func TestPreparedPropUseWaitsForServerConfirmation(t *testing.T) {
	match := testMatch(t)
	match.prepared = []preparedView{{Item: curePropID, Count: 2}}

	match.requestPreparedUse(20030) // not brought into the match
	match.requestPreparedUse(curePropID)
	requests := match.takeTCP()
	if len(requests) != 1 {
		t.Fatalf("requests = %+v, want one REQUEST_PLAY", requests)
	}
	payload, _ := hex.DecodeString(requests[0].Payload)
	event, err := game.ParseGameEventPayload(payload)
	if err != nil || event.Schema != game.RequestPreparedUseProp {
		t.Fatalf("request = %+v, %v; want 0x1174", event, err)
	}
	use, err := game.ParsePreparedUsePropEvent(event)
	if err != nil || use.PlayerID != 1 || use.ItemID != curePropID {
		t.Fatalf("use = %+v, %v", use, err)
	}
	match.requestPreparedUse(curePropID)
	if len(match.takeTCP()) != 0 {
		t.Fatal("a pending use was requested twice")
	}

	match.applyPreparedUse(event.Body) // the server's 0x1175 notification
	if left := match.preparedCount(curePropID); left != 1 {
		t.Fatalf("count after the confirmed use = %d, want 1", left)
	}
	if _, pending := match.preparedRequested[curePropID]; pending {
		t.Fatal("confirmed use is still pending")
	}
}

func TestNonArbitratorRequestsPickupAndWaitsForTheArbitrator(t *testing.T) {
	match := testMatchAs(t, 2)
	events, err := match.runtime.Step([]battleengine.Action{{PlayerID: 1}})
	if err != nil {
		t.Fatal(err)
	}
	match.publishEvents(match.engine, events)
	var requested []byte
	for _, message := range match.outbound {
		if message.data.DataID == uint32(game.RequestGetItem) && message.arbitratorOnly {
			requested = message.data.Data
		}
	}
	if requested == nil {
		t.Fatalf("no pickup request to the arbitrator in %+v", match.outbound)
	}
	if heldCount(match, 64) != 0 {
		t.Fatal("the non-arbitrator collected the item on its own")
	}
	request, err := game.ParsePlayerItemEvent(game.GameEvent{Schema: game.RequestGetItem, Body: requested})
	if err != nil || request.PlayerID != 1 || request.ItemID != 28 {
		t.Fatalf("request = %+v, %v", request, err)
	}

	match.applyRemote(2, game.BattleMessageData{DataID: uint32(game.NotifyPlayerGetItem), Data: requested}) // the arbitrator's 0x0FAD
	if heldCount(match, 64) != 1 {
		t.Fatal("the arbitrator's grant did not give the item")
	}
	if len(match.pickupRequests) != 0 {
		t.Fatalf("granted request still pending: %+v", match.pickupRequests)
	}
}

func TestHumanRendezvousIsAnsweredOnce(t *testing.T) {
	server, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		t.Fatal(err)
	}
	defer server.Close()
	match := testMatch(t)
	if match.conn, err = net.DialUDP("udp4", nil, server.LocalAddr().(*net.UDPAddr)); err != nil {
		t.Fatal(err)
	}
	defer match.conn.Close()
	peer, err := (game.LegacyUDPControlPacket{
		Header: game.LegacyUDPControlHeader{PacketNumber: 1, PlayerID: 2, UIN: 1900000002, Type: game.LegacyUDPRoomPeerType},
		RoomPeer: &game.LegacyUDPRoomPeerPayload{TargetPlayerID: 1, TargetUIN: 1000001,
			Endpoint: game.LegacyUDPEndpoint{IPv4: [4]byte{127, 0, 0, 1}, Port: 40000}},
	}).Encode()
	if err != nil {
		t.Fatal(err)
	}
	match.handleDatagram(peer)
	match.handleDatagram(peer)
	if !match.peerReady[2] {
		t.Fatal("peer 2 is not ready")
	}
	buffer := make([]byte, 256)
	_ = server.SetReadDeadline(time.Now().Add(200 * time.Millisecond))
	size, _, err := server.ReadFromUDP(buffer)
	if err != nil {
		t.Fatalf("no answer: %v", err)
	}
	answer, err := game.DecodeLegacyUDPControlPacket(buffer[:size])
	if err != nil || answer.RoomPeer == nil || answer.RoomPeer.TargetPlayerID != 2 || answer.Header.PlayerID != 1 {
		t.Fatalf("answer = %+v, %v", answer, err)
	}
	if _, _, err := server.ReadFromUDP(buffer); err == nil {
		t.Fatal("the rendezvous was answered twice")
	}
}
