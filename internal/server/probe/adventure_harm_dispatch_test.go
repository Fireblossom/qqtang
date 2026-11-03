package probe

import (
	"strings"
	"testing"

	"qqtang/internal/game/match"
	"qqtang/internal/protocol/game"
)

func TestAdventureHarmCarriesHPLeftFromOwnerOrArbitrator(t *testing.T) {
	server, owner, peer := newRoomPeerUDPTestServer(t)
	state, err := server.sessionRoom(owner)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = state.SetReady(peer.Profile.PlayerID, true); err != nil {
		t.Fatal(err)
	}
	const gameID uint32 = 78
	if _, err = server.worldState().StartMatchWithID(owner.UIN, gameID); err != nil {
		t.Fatal(err)
	}
	battle, err := match.NewAdventureBattle(gameID, 1601, owner.Profile.PlayerID, []match.AdventureParticipant{
		{PlayerID: owner.Profile.PlayerID, TeamID: 1}, {PlayerID: peer.Profile.PlayerID, TeamID: 1},
	})
	if err != nil {
		t.Fatal(err)
	}
	server.battles = map[uint32]*match.AdventureBattle{gameID: battle}
	server.projectRoomMatch(owner.RoomID, gameID, 1601)
	harm := func(session *connectionSession, object uint16, hpLeft uint16) gameEventMessageResult {
		body, err := (game.EntityHarmedEvent{ObjectID: object, Time: 5000, PosX: 100, PosY: 100, LossHP: hpLeft}).MarshalNetworkBinary()
		if err != nil {
			t.Fatal(err)
		}
		payload, err := game.MarshalGameEventPayload(session.UIN, 1, game.PlayerBeHarmed, body)
		if err != nil {
			t.Fatal(err)
		}
		packet := testLocalRoutedPacketWithPayload(t, game.GameEventRequestCommand, 3, 0xffff, session.Profile.PlayerID, session.UIN, payload)
		return server.handleGameEventMessage(ListenerConfig{Response: ResponseConfig{QQTGameEventRelay: true}}, session, "test", "local", "remote", packet)
	}
	if result := harm(owner, 30001, 120); !result.handled || !strings.Contains(result.result, "adventure_30001_harmed_hp_left_120") {
		t.Fatalf("arbitrator NPC harm = %+v", result)
	}
	if result := harm(peer, peer.Profile.PlayerID, 280); !result.handled || !strings.Contains(result.result, "hp_left_280") {
		t.Fatalf("own harm = %+v", result)
	}
	if result := harm(peer, owner.Profile.PlayerID, 10); strings.Contains(result.result, "hp_left") {
		t.Fatalf("a peer reported another player's HP: %+v", result)
	}
}
