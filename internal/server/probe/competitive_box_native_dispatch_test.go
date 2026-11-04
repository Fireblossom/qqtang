package probe

import (
	"encoding/binary"
	"testing"

	"qqtang/internal/game/mapdata"
	"qqtang/internal/game/match"
	"qqtang/internal/protocol/game"
)

func boxNativeDispatchServer(t *testing.T, mapID uint32) (*Server, *connectionSession, *connectionSession, *match.CompetitiveBattle) {
	t.Helper()
	server, owner, peer, _ := newSculptureDamageTestServer(t)
	selected, ok := server.mapCatalog.CompetitiveMap(mapID)
	if !ok {
		t.Fatal("native map missing")
	}
	spec, _ := mapdata.LookupCompetitiveRule(selected.Rule)
	owner.CurrentMapID, peer.CurrentMapID = mapID, mapID
	if err := server.replaceCompetitiveBattle(1, mapID, owner.Profile.PlayerID, []match.CompetitiveParticipant{{PlayerID: owner.Profile.PlayerID, RoleID: 7, TeamID: 1}, {PlayerID: peer.Profile.PlayerID, RoleID: 8, TeamID: 2}}, match.CompetitiveRuleConfig{ConclusionPolicy: match.CompetitiveConclusionClientRule, PlayerLifecycle: match.CompetitivePlayerNativeDurability, NativeHitLimit: spec.NativePlayerHitLimit}); err != nil {
		t.Fatal(err)
	}
	battle, err := server.competitiveBattle(1)
	if err != nil {
		t.Fatal(err)
	}
	return server, owner, peer, battle
}
func boxKnockBody(player uint16, at uint32, avatar bool) []byte {
	b := make([]byte, 11)
	binary.BigEndian.PutUint16(b, player)
	binary.BigEndian.PutUint32(b[2:], at)
	binary.BigEndian.PutUint16(b[6:], 100)
	binary.BigEndian.PutUint16(b[8:], 160)
	if avatar {
		b[10] = 1
	}
	return b
}
func boxNativePacket(player uint16, schema uint16, body []byte) game.GameplayDataPackage {
	return game.GameplayDataPackage{GameID: 1, PlayerID: player, MessageIndexes: []uint32{1}, Messages: []game.BattleMessageData{{DataID: uint32(schema), Data: body}}}
}

func TestBoxNPCIceIsAnArbitratorObservationAndNeverRetiresTheEntity(t *testing.T) {
	server, owner, peer, battle := boxNativeDispatchServer(t, 1401)
	const npc = 20001
	if err := battle.RegisterNativeNPCEntities([]uint16{npc}); err != nil {
		t.Fatal(err)
	}
	body := make([]byte, 6)
	binary.BigEndian.PutUint16(body, npc)
	ack := sendReliableBossRuleEvent(t, server, owner, 1, game.NotifyPlayerTrapped, body)
	if len(ack.response) == 0 || !battle.AuthorizesNativeNPCProxy(owner.Profile.PlayerID, npc) {
		t.Fatal("ice rejected or retired NPC")
	}
	denied := sendReliableBossRuleEvent(t, server, peer, 2, game.NotifyPlayerTrapped, body)
	if len(denied.response) != 0 {
		t.Fatal("ordinary observer obtained NPC ice authority")
	}
	packet := boxNativePacket(owner.Profile.PlayerID, game.NotifyPlayerTrapped, body)
	if err := validateGameplayFastPackagesWithBattle([]game.GameplayDataPackage{packet}, owner.Profile.PlayerID, 1, battle); err != nil {
		t.Fatal(err)
	}
	if _, err := consumeCompetitiveFastPackages([]game.GameplayDataPackage{packet}, battle); err != nil {
		t.Fatal(err)
	}
	if !battle.AuthorizesNativeNPCProxy(owner.Profile.PlayerID, npc) {
		t.Fatal("fast ice retired NPC")
	}
	packet.PlayerID = peer.Profile.PlayerID
	if err := validateGameplayFastPackagesWithBattle([]game.GameplayDataPackage{packet}, peer.Profile.PlayerID, 1, battle); err == nil {
		t.Fatal("own-package shortcut bypassed NPC observation authority")
	}
	if _, err := battle.RecordNativeNPCDeath(npc); err != nil {
		t.Fatal(err)
	}
	late := sendReliableBossRuleEvent(t, server, owner, 3, game.NotifyPlayerTrapped, body)
	if len(late.response) == 0 || battle.AuthorizesNativeNPCProxy(owner.Profile.PlayerID, npc) {
		t.Fatal("late ice was not harmless or revived NPC")
	}
}

func TestBoxKnockAndLaterDeathConsumeExactlyFourLayers(t *testing.T) {
	server, owner, peer, battle := boxNativeDispatchServer(t, 1401)
	// 005FED14 does not report a death for a transformed shell.
	shell := sendReliableBossRuleEvent(t, server, peer, 1, game.NotifyPlayerKnocked, boxKnockBody(peer.Profile.PlayerID, 100, true))
	if len(shell.response) == 0 || shell.completeCompetitiveAfterSend {
		t.Fatal("avatar knock was not a harmless observation")
	}
	for hit := uint32(1); hit <= 4; hit++ {
		// Native collision time and receiver's current scene clock differ by one
		// frame. 1162 is an observation, not a synthetic 10F4 life charge.
		tick := hit * 4000
		knocked := boxKnockBody(peer.Profile.PlayerID, tick, false)
		death := boxKnockBody(peer.Profile.PlayerID, tick+20, false)
		report := sendReliableBossRuleEvent(t, server, owner, 10+hit, game.NotifyPlayerKnocked, knocked)
		if len(report.response) == 0 || battle.IsConcluded() {
			t.Fatalf("knock %d consumed a layer/finalized early: %s", hit, report.result)
		}
		sendReliableBossRuleEvent(t, server, peer, 20+hit, game.NotifyPlayerKnocked, knocked)
		result := sendReliableBossRuleEvent(t, server, peer, 30+hit, game.NotifyPlayerDieEvent, death)
		if len(result.response) == 0 || result.completeCompetitiveAfterSend != (hit == 4) {
			t.Fatalf("death %d complete=%t result=%s", hit, result.completeCompetitiveAfterSend, result.result)
		}
		duplicate := sendReliableBossRuleEvent(t, server, peer, 40+hit, game.NotifyPlayerDieEvent, death)
		if duplicate.completeCompetitiveAfterSend {
			t.Fatal("duplicate death concluded twice")
		}
	}
}

func TestBoxFireHarm500ReliableAndNPCProxyAreOneLayer(t *testing.T) {
	server, owner, peer, battle := boxNativeDispatchServer(t, 1401)
	const npc = 20001
	if err := battle.RegisterNativeNPCEntities([]uint16{npc}); err != nil {
		t.Fatal(err)
	}
	avatar := append(boxKnockBody(peer.Profile.PlayerID, 100, true), 0x01, 0xf4)
	avatarPacket := boxNativePacket(npc, game.PlayerBeHarmed, avatar)
	if err := validateGameplayFastPackagesWithBattle([]game.GameplayDataPackage{avatarPacket}, owner.Profile.PlayerID, 1, battle); err != nil {
		t.Fatal(err)
	}
	if _, _, err := normalizeCompetitiveDurabilityFastPackages([]game.GameplayDataPackage{avatarPacket}, battle); err != nil {
		t.Fatal(err)
	}
	// The 500 marker is not valid when this same NPC package targets itself.
	npcHarm := append(boxKnockBody(npc, 200, false), 0x01, 0xf4)
	if err := validateGameplayFastPackagesWithBattle([]game.GameplayDataPackage{boxNativePacket(npc, game.PlayerBeHarmed, npcHarm)}, owner.Profile.PlayerID, 1, battle); err == nil {
		t.Fatal("participant fire-box marker leaked to NPC target")
	}
	for hit := uint32(1); hit <= 4; hit++ {
		body := append(boxKnockBody(peer.Profile.PlayerID, hit*4000, false), 0x01, 0xf4)
		packet := boxNativePacket(npc, game.PlayerBeHarmed, body)
		if err := validateGameplayFastPackagesWithBattle([]game.GameplayDataPackage{packet}, owner.Profile.PlayerID, 1, battle); err != nil {
			t.Fatalf("native fire-box harm rejected: %v", err)
		}
		if _, _, err := normalizeCompetitiveDurabilityFastPackages([]game.GameplayDataPackage{packet}, battle); err != nil {
			t.Fatal(err)
		}
		ack := sendReliableBossRuleEvent(t, server, peer, hit, game.PlayerBeHarmed, body)
		if len(ack.response) == 0 {
			t.Fatalf("reliable fire-box harm: %s", ack.result)
		}
		death := boxKnockBody(peer.Profile.PlayerID, hit*4000, false)
		result := sendReliableBossRuleEvent(t, server, owner, 10+hit, game.NotifyPlayerDieEvent, death)
		if result.completeCompetitiveAfterSend != (hit == 4) {
			t.Fatalf("fire hit %d concluded=%t", hit, result.completeCompetitiveAfterSend)
		}
	}
}

func TestBoxNPCBoxDeathReliableOrdersAndObserverAuthority(t *testing.T) {
	for _, first := range []uint16{game.NotifyPlayerKnocked, game.NotifyPlayerKilled} {
		t.Run(map[uint16]string{game.NotifyPlayerKnocked: "knock_first", game.NotifyPlayerKilled: "kill_first"}[first], func(t *testing.T) {
			server, owner, peer, battle := boxNativeDispatchServer(t, 1401)
			const npc = 20001
			battle.RegisterNativeNPCEntities([]uint16{npc})
			knock := boxKnockBody(npc, 60020, false)
			// Existing participant observations are allowed; they confer no NPC proxy right.
			observed := sendReliableBossRuleEvent(t, server, peer, 1, game.NotifyPlayerKnocked, boxKnockBody(owner.Profile.PlayerID, 59000, false))
			if len(observed.response) == 0 {
				t.Fatal("participant observation regressed")
			}
			denied := sendReliableBossRuleEvent(t, server, peer, 2, game.NotifyPlayerKnocked, knock)
			if len(denied.response) != 0 || !battle.AuthorizesNativeNPCProxy(owner.Profile.PlayerID, npc) {
				t.Fatal("ordinary peer retired an NPC")
			}
			shell := sendReliableBossRuleEvent(t, server, owner, 3, game.NotifyPlayerKnocked, boxKnockBody(npc, 60000, true))
			if len(shell.response) == 0 || !battle.AuthorizesNativeNPCProxy(owner.Profile.PlayerID, npc) {
				t.Fatal("avatar knock retired underlying NPC")
			}
			killed, err := (game.PlayerKilledEvent{PlayerInteractionEvent: game.PlayerInteractionEvent{PlayerID: peer.Profile.PlayerID, DestinationPlayerID: npc, ClientTime: 60020, PosX: 100, PosY: 160}}).MarshalNetworkBinary()
			if err != nil {
				t.Fatal(err)
			}
			bodies := map[uint16][]byte{game.NotifyPlayerKnocked: knock, game.NotifyPlayerKilled: killed}
			second := uint16(game.NotifyPlayerKilled)
			if first == second {
				second = game.NotifyPlayerKnocked
			}
			for i, schema := range []uint16{first, second, first, second} {
				ack := sendReliableBossRuleEvent(t, server, owner, uint32(10+i), schema, bodies[schema])
				if len(ack.response) == 0 || ack.completeCompetitiveAfterSend {
					t.Fatalf("NPC report 0x%04x failed/settled match: %s", schema, ack.result)
				}
				if battle.AuthorizesNativeNPCProxy(owner.Profile.PlayerID, npc) {
					t.Fatalf("NPC stayed alive after 0x%04x", schema)
				}
			}
		})
	}
}

func TestBoxNPCFastObservationDoesNotAuthorizeOrdinaryPeer(t *testing.T) {
	_, owner, peer, battle := boxNativeDispatchServer(t, 1401)
	const npc = 20001
	battle.RegisterNativeNPCEntities([]uint16{npc})
	body := boxKnockBody(npc, 60020, false)
	packet := boxNativePacket(peer.Profile.PlayerID, game.NotifyPlayerKnocked, body)
	if err := validateGameplayFastPackagesWithBattle([]game.GameplayDataPackage{packet}, peer.Profile.PlayerID, 1, battle); err == nil {
		t.Fatal("participant package obtained NPC observation authority")
	}
	packet.PlayerID = owner.Profile.PlayerID
	if err := validateGameplayFastPackagesWithBattle([]game.GameplayDataPackage{packet}, owner.Profile.PlayerID, 1, battle); err != nil {
		t.Fatal(err)
	}
	if _, err := consumeCompetitiveFastPackages([]game.GameplayDataPackage{packet}, battle); err != nil {
		t.Fatal(err)
	}
	if battle.AuthorizesNativeNPCProxy(owner.Profile.PlayerID, npc) {
		t.Fatal("fast knock left NPC alive")
	}
}

func TestMechanicalRuleDoesNotGainBoxObservationsOrNPCProxy(t *testing.T) {
	server, owner, peer, battle := boxNativeDispatchServer(t, 1301)
	ack := sendReliableBossRuleEvent(t, server, owner, 1, game.NotifyPlayerKnocked, boxKnockBody(peer.Profile.PlayerID, 4000, false))
	if len(ack.response) != 0 || battle.IsConcluded() {
		t.Fatal("rule7 accepted box observation")
	}
	harm := append(boxKnockBody(peer.Profile.PlayerID, 4000, false), 0x01, 0xf4)
	if err := validateGameplayFastPackagesWithBattle([]game.GameplayDataPackage{boxNativePacket(20001, game.PlayerBeHarmed, harm)}, owner.Profile.PlayerID, 1, battle); err == nil {
		t.Fatal("rule7 gained native NPC proxy")
	}
}

func TestBoxNPCFastKillAndKnockRetirementAreIdempotent(t *testing.T) {
	for _, schema := range []uint16{game.NotifyPlayerKilled, game.NotifyPlayerKnocked} {
		_, owner, peer, battle := boxNativeDispatchServer(t, 1401)
		const npc = 20001
		if err := battle.RegisterNativeNPCEntities([]uint16{npc}); err != nil {
			t.Fatal(err)
		}
		body := boxKnockBody(npc, 60020, false)
		if schema == game.NotifyPlayerKilled {
			var err error
			body, err = (game.PlayerKilledEvent{PlayerInteractionEvent: game.PlayerInteractionEvent{PlayerID: peer.Profile.PlayerID, DestinationPlayerID: npc, ClientTime: 60020, PosX: 100, PosY: 160}}).MarshalNetworkBinary()
			if err != nil {
				t.Fatal(err)
			}
		}
		packet := boxNativePacket(peer.Profile.PlayerID, schema, body)
		if err := validateGameplayFastPackagesWithBattle([]game.GameplayDataPackage{packet}, peer.Profile.PlayerID, 1, battle); err == nil {
			t.Fatalf("peer authorized NPC target for 0x%04x", schema)
		}
		packet.PlayerID = owner.Profile.PlayerID
		for repeat := 0; repeat < 2; repeat++ {
			if err := validateGameplayFastPackagesWithBattle([]game.GameplayDataPackage{packet}, owner.Profile.PlayerID, 1, battle); err != nil {
				t.Fatal(err)
			}
			result, err := consumeCompetitiveFastPackages([]game.GameplayDataPackage{packet}, battle)
			if err != nil || result != nil || battle.IsConcluded() || battle.AuthorizesNativeNPCProxy(owner.Profile.PlayerID, npc) {
				t.Fatalf("NPC retirement schema0x%04x duplicate%d: %+v %v", schema, repeat, result, err)
			}
		}
	}
}
