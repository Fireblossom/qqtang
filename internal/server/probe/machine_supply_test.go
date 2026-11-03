package probe

import (
	"strings"
	"testing"

	"qqtang/internal/game/match"
	"qqtang/internal/protocol/game"
)

func TestMachineSupplyReliableMirrorChecksMapItemCellAndArbitrator(t *testing.T) {
	server, owner, peer, _ := newSculptureDamageTestServer(t)
	owner.CurrentMapID, peer.CurrentMapID = 1302, 1302
	if err := server.replaceCompetitiveBattle(1, 1302, owner.Profile.PlayerID, []match.CompetitiveParticipant{
		{PlayerID: owner.Profile.PlayerID, RoleID: 1, TeamID: 1}, {PlayerID: peer.Profile.PlayerID, RoleID: 2, TeamID: 2},
	}, match.CompetitiveRuleConfig{ConclusionPolicy: match.CompetitiveConclusionClientRule, PlayerLifecycle: match.CompetitivePlayerNativeDurability, NativeHitLimit: 4}); err != nil {
		t.Fatal(err)
	}
	entry, _ := server.mapCatalog.CompetitiveMap(1302)
	cell := entry.MachineSupplyCells[0]
	item := game.ProduceItem{RowAndCol: cell.Row<<4 | cell.Col, ItemID: 1}
	body, _ := item.MarshalNetworkBinary()
	r := sendReliableBossRuleEvent(t, server, owner, 1, game.NotifyProduceItem, body)
	if !strings.Contains(r.result, "ack_machine_produce_item_1") || len(r.followUp) != 0 || r.afterResponse != nil {
		t.Fatalf("valid reliable mirror rejected or duplicated: %+v", r)
	}
	r = sendReliableBossRuleEvent(t, server, peer, 2, game.NotifyProduceItem, body)
	if strings.Contains(r.result, "ack_machine_produce") {
		t.Fatal("non-arbitrator supply accepted")
	}
	for _, invalid := range []game.ProduceItem{{RowAndCol: 0x67, ItemID: 1}, {RowAndCol: item.RowAndCol, ItemID: 81}} {
		body, _ := invalid.MarshalNetworkBinary()
		r = sendReliableBossRuleEvent(t, server, owner, 3, game.NotifyProduceItem, body)
		if strings.Contains(r.result, "ack_machine_produce") {
			t.Fatalf("invalid machine supply accepted: %+v", invalid)
		}
	}
	owner.CurrentMapID = 1401
	if _, _, err := server.requireCompetitiveProduceItem(owner, item); err == nil {
		t.Fatal("rule 8 accepted rule 7 production")
	}
}
