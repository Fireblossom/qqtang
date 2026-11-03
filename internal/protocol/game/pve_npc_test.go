package game

import (
	"reflect"
	"testing"
)

func TestCreatePVENPCRoundTripIs72BytesEach(t *testing.T) {
	want := CreatePVENPCEvent{NPCs: []PVENPC{
		{NPCID: 30001, NPCBaseID: 30033, NickName: []byte{0xce, 0xd2}, ImageID: 28, MoveStrategy: 3, HP: 300, Speed: 3, Harm: 1, BattleStrategy: 1, BornRow: 4, BornCol: 7, View: 3},
		{NPCID: 30002, NPCBaseID: 30013, IsBoss: true, NickName: []byte("boss"), ImageID: 84, Defense: 50, HP: 15000, BornRow: 28, BornCol: 23},
	}}
	body, err := want.MarshalNetworkBinary()
	if err != nil || len(body) != 4+2*72 {
		t.Fatalf("body %d %v", len(body), err)
	}
	got, err := ParseCreatePVENPCEvent(GameEvent{Schema: NotifyCreatePVENPC, Body: body})
	if err != nil || !reflect.DeepEqual(got, want) {
		t.Fatalf("round trip %+v %v", got, err)
	}
}

func TestGameNextMapRequestRoundTrip(t *testing.T) {
	// The capture: player 1, time 0x9d2a, route 1, next stage 1602.
	body, err := (GameNextMapRequest{PlayerID: 1, ClientTime: 0x9d2a, ContinueID: 1, NextMapID: 1602}).MarshalNetworkBinary()
	if err != nil || len(body) != 14 {
		t.Fatal(err)
	}
	got, err := ParseGameNextMapRequest(GameEvent{Schema: RequestGameNextMap, Body: body})
	if err != nil || got.NextMapID != 1602 || got.ContinueID != 1 {
		t.Fatalf("%+v %v", got, err)
	}
}
