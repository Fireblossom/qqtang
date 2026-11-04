package probe

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"qqtang/internal/game/mapdata"
	"qqtang/internal/game/match"
	"qqtang/internal/protocol/game"
)

// A small real catalog keeps the doorway regression runnable without the
// optional installed client directory used by older integration tests.
func adventureDoorwayCatalog(t *testing.T) *mapdata.Catalog {
	t.Helper()
	root := t.TempDir()
	for _, dir := range []string{"map", "config", "object/mapElem"} {
		if err := os.MkdirAll(filepath.Join(root, dir), 0700); err != nil {
			t.Fatal(err)
		}
	}
	write := func(name, data string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(root, name), []byte(data), 0600); err != nil {
			t.Fatal(err)
		}
	}
	write("object/mapElem/mapElem.py", "class QQTMapElem1(QQTMapElem):\n    imageID = 1\n    GridAttr = (0,)\n")
	var description strings.Builder
	for _, id := range []int{1601, 1602, 1603, 1646, 1648, 1650} {
		name := fmt.Sprintf("pve%d.map", id)
		fmt.Fprintf(&description, "(%d, 'pve', 'test', 15, 13, 4, '%s', 'x', 'x', 0, '', 1, 3),\n", id, name)
		write("map/"+name, name)
		write(fmt.Sprintf("config/battlefield_map%d.dat", id), "test")
	}
	write("map/mapDesc.py", description.String())
	write("config/Continue.ini", "[public]\ncontinuenum=2\n[continue1]\nmaps=1601,1602,1603\n[continue2]\nmaps=1646,1648,1650\n")
	catalog, err := mapdata.LoadCatalog(root)
	if err != nil {
		t.Fatal(err)
	}
	return catalog
}

func adventureDoorwayPacket(t *testing.T, session *connectionSession, player uint16, route, target uint32) []byte {
	t.Helper()
	body, err := (game.GameNextMapRequest{PlayerID: player, ClientTime: 60000, ContinueID: route, NextMapID: target}).MarshalNetworkBinary()
	if err != nil {
		t.Fatal(err)
	}
	payload, err := game.MarshalGameEventPayload(session.UIN, 1, game.RequestGameNextMap, body)
	if err != nil {
		t.Fatal(err)
	}
	return testLocalRoutedPacketWithPayload(t, game.GameEventRequestCommand, 3, 0xffff, 1, session.UIN, payload)
}

func TestAdventureDoorwayPeersAcknowledgeAlreadyAppliedTransition(t *testing.T) {
	catalog := adventureDoorwayCatalog(t)
	for _, edge := range []struct{ from, to, wire, route uint32 }{{1601, 1602, 1602, 1}, {1646, 1648, 1647, 2}} {
		for _, projected := range []bool{false, true} {
			t.Run(fmt.Sprintf("%d_to_%d_projected_%v", edge.from, edge.to, projected), func(t *testing.T) {
				players := []match.AdventureParticipant{{PlayerID: 1, TeamID: 1, RoleID: 1}, {PlayerID: 2, TeamID: 1, RoleID: 2}}
				battle, err := match.NewAdventureBattleWithStage(7, edge.from, 1, 1, 2, players)
				if err != nil {
					t.Fatal(err)
				}
				if _, err = battle.RecordNPCDeath(10); err != nil {
					t.Fatal(err)
				}
				transition, err := battle.PrepareNextStage(2)
				if err != nil {
					t.Fatal(err)
				}
				if err = battle.AdvanceStageWithRule(edge.to, 22, 3, transition.ArbitratorPlayerID, transition.Survivors); err != nil {
					t.Fatal(err)
				}
				server := &Server{mapCatalog: catalog, battles: map[uint32]*match.AdventureBattle{7: battle}, logWriter: io.Discard}
				// The guest has already advanced the room. The owner's old
				// request can arrive either side of its queued stage projection.
				for _, player := range []uint16{1, 2} {
					profile := game.DefaultPlayerProfile()
					profile.PlayerID = player
					mapID := edge.from
					if projected {
						mapID = edge.to
					}
					session := &connectionSession{UIN: 1_000_000 + uint32(player), Profile: profile, RoomID: 1, CurrentGameID: 7, CurrentStageGameID: 8, CurrentMapID: mapID}
					before, err := battle.PlayerStatistics(player)
					if err != nil {
						t.Fatal(err)
					}
					result, err := server.handleAdventureNextMap(session, adventureDoorwayPacket(t, session, player, edge.route, edge.wire))
					if err != nil || result.result != fmt.Sprintf("qqt_adventure_next_map_duplicate_%d", edge.to) || len(result.response) == 0 || len(result.followUp) != 0 || len(result.startFollowUp) != 0 || result.completeAfterSend || result.settlement != nil || result.roomSettlement != nil || result.afterResponse != nil {
						t.Fatalf("duplicate transition produced work: %+v err=%v", result, err)
					}
					after, err := battle.PlayerStatistics(player)
					previous, current := battle.StageMapIDs()
					if err != nil || after != before || previous != edge.from || current != edge.to || battle.ItemSeed() != 22 || battle.CanAdvanceStage() {
						t.Fatalf("duplicate mutated the current stage %d -> %d", previous, current)
					}
				}
			})
		}
	}
}

func TestAdventureDoorwayDuplicateDoesNotBypassStageValidation(t *testing.T) {
	catalog := adventureDoorwayCatalog(t)
	for _, row := range []struct {
		name            string
		previous        bool
		profile, player uint16
		route, target   uint32
	}{
		{"fresh", false, 1, 1, 1, 1602},
		{"identity", true, 1, 2, 1, 1602},
		{"nonparticipant", true, 3, 3, 1, 1602},
		{"wrong_route", true, 1, 1, 2, 1602},
		{"wrong_target", true, 1, 1, 1, 1648},
		{"uncleared_next", true, 1, 1, 1, 1603},
		{"ambiguous_zero", true, 1, 1, 1, 0},
	} {
		t.Run(row.name, func(t *testing.T) {
			battle, err := match.NewAdventureBattleWithStage(7, 1602, 22, 3, 1, []match.AdventureParticipant{{PlayerID: 1, TeamID: 1, RoleID: 1}})
			if err != nil {
				t.Fatal(err)
			}
			if row.previous {
				battle, err = match.NewAdventureBattle(7, 1601, 1, battle.Participants())
				if err != nil {
					t.Fatal(err)
				}
				if err = battle.AdvanceStageWithRule(1602, 22, 3, 1, battle.Participants()); err != nil {
					t.Fatal(err)
				}
			}
			profile := game.DefaultPlayerProfile()
			profile.PlayerID = row.profile
			session := &connectionSession{UIN: 1_000_001, Profile: profile, RoomID: 1, CurrentGameID: 7, CurrentStageGameID: 8, CurrentMapID: 1602}
			server := &Server{mapCatalog: catalog, battles: map[uint32]*match.AdventureBattle{7: battle}, logWriter: io.Discard}
			result, err := server.handleAdventureNextMap(session, adventureDoorwayPacket(t, session, row.player, row.route, row.target))
			_, current := battle.StageMapIDs()
			if err == nil || len(result.followUp) != 0 || current != 1602 || battle.ItemSeed() != 22 || battle.CanAdvanceStage() {
				t.Fatalf("invalid doorway advanced stage: %+v err=%v map=%d", result, err, current)
			}
		})
	}
}

func TestAdventureDoorwayStillAdvancesClearedCurrentStage(t *testing.T) {
	catalog := adventureDoorwayCatalog(t)
	battle, err := match.NewAdventureBattle(7, 1601, 1, []match.AdventureParticipant{{PlayerID: 1, TeamID: 1, RoleID: 1}})
	if err != nil {
		t.Fatal(err)
	}
	if err = battle.AdvanceStageWithRule(1602, 22, 1, 1, battle.Participants()); err != nil {
		t.Fatal(err)
	}
	if _, err = battle.RecordNPCDeath(10); err != nil {
		t.Fatal(err)
	}
	profile := game.DefaultPlayerProfile()
	session := &connectionSession{UIN: 1_000_001, Profile: profile, RoomID: 1, CurrentGameID: 7, CurrentStageGameID: 8, CurrentMapID: 1602}
	server := &Server{mapCatalog: catalog, battles: map[uint32]*match.AdventureBattle{7: battle}, logWriter: io.Discard}
	result, err := server.handleAdventureNextMap(session, adventureDoorwayPacket(t, session, 1, 1, 1603))
	previous, current := battle.StageMapIDs()
	if err != nil || result.followUpResult != "qqt_adventure_next_map_1603" || len(result.followUp) == 0 || previous != 1602 || current != 1603 {
		t.Fatalf("normal doorway did not advance: %+v err=%v %d -> %d", result, err, previous, current)
	}
	session.CurrentMapID = 1603
	if _, err = server.handleAdventureNextMap(session, adventureDoorwayPacket(t, session, 1, 1, 1602)); err == nil {
		t.Fatal("two-stage-old doorway was accepted as the latest duplicate")
	}
}
