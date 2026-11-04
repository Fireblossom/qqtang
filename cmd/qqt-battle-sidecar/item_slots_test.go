package main

import (
	"encoding/hex"
	"reflect"
	"testing"

	"qqtang/internal/game/mapdata"
	"qqtang/internal/protocol/game"
)

func TestPreparedSlotsFollowLocalRoomBarAndSkipHoles(t *testing.T) {
	// Captured from 005B974A with real 0087EC6D grants/005DAC82 writes.
	wire := []preparedView{{Item: 20020, Count: 5}, {Item: 20058, Count: 7}, {Item: 20059, Count: 9}}
	bar := []uint32{20059, 0, 20020, 20058, 0, 0, 0}
	selected := selectPreparedOrder(wire, bar)
	want := []preparedView{{Item: 20059, Count: 9}, {Item: 20020, Count: 5}, {Item: 20058, Count: 7}}
	if !reflect.DeepEqual(selected, want) {
		t.Fatalf("room-bar order %+v, want %+v", selected, want)
	}
	if len(selectPreparedOrder(wire, []uint32{0, 0, 0, 0, 0, 0, 0})) != 0 {
		t.Fatal("empty local bar fell back to wire order")
	}
	if !reflect.DeepEqual(selectPreparedOrder(wire, nil), wire) {
		t.Fatal("omitted CLI bar changed compatibility order")
	}
	all := append(append([]preparedView(nil), wire...), []preparedView{{Item: 20060, Count: 1}, {Item: 20061, Count: 1}, {Item: 20062, Count: 1}, {Item: 20063, Count: 1}, {Item: 20064, Count: 1}}...)
	want = []preparedView{{Item: 20063, Count: 1}, {Item: 20020, Count: 5}, {Item: 20058, Count: 7}, {Item: 20059, Count: 9}}
	if got := selectPreparedOrder(all, []uint32{20064, 20063, 0, 20020, 0, 20058, 20059}); !reflect.DeepEqual(got, want) {
		t.Fatalf("native first-seven matching changed: %v", got)
	}
}

func TestInitialSlotsSkipMissingPropFactoryDefinitions(t *testing.T) {
	m := testMatch(t)
	m.prepared = []preparedView{{Item: 20046, Count: 3}, {Item: 20020, Count: 5}}
	m.propTable = map[uint32]mapdata.AdventureProp{20020: {Tier: 3}}
	m.filterPreparedDefinitions()
	if got := m.mixedSlots(); !reflect.DeepEqual(got, [][2]uint32{{20020, 5}}) {
		t.Fatalf("missing prop factory left a ghost slot: %v", got)
	}
}

func TestFollowerCanRequestAnOwnedActionBeyondFirstPage(t *testing.T) {
	m := testMatchAs(t, 2)
	for _, id := range []uint8{41, 42, 43, 44, 45, 46, 63, 64} {
		m.engine.GrantHeldAction(1, id, 1)
	}
	m.requestItemUse(64)
	packets := m.takeTCP()
	if len(packets) != 1 {
		t.Fatal("following client rejected an owned action beyond seven slots")
	}
	payload, _ := hex.DecodeString(packets[0].Payload)
	event, err := game.ParseGameEventPayload(payload)
	if err != nil || event.Schema != game.RequestUseItem {
		t.Fatalf("page-two request envelope: %+v %v", event, err)
	}
	m.engine.GrantHeldAction(1, 41, 255)
	state := view(m.runtime, nil)
	if state.Actors[0].Held[0] != [2]uint32{41, 256} || len(state.Actors[0].Held) != 8 {
		t.Fatal("host JSON view truncated the native inventory")
	}
}

func TestMixedSlotsAppendAndCompactLikeNativeVector(t *testing.T) {
	m := testMatch(t)
	m.prepared = []preparedView{{Item: 20059, Count: 9}, {Item: 20020, Count: 5}, {Item: 20058, Count: 7}}
	m.initSlotOrder()
	for _, id := range []uint8{41, 42, 43, 44, 45, 46, 63, 64, 65} {
		m.engine.GrantHeldAction(1, id, 1)
	}
	want := [][2]uint32{{20059, 9}, {20020, 5}, {20058, 7}, {41, 1}, {42, 1}, {43, 1}, {44, 1}, {45, 1}, {46, 1}, {63, 1}, {64, 1}, {65, 1}}
	if got := m.mixedSlots(); !reflect.DeepEqual(got, want) {
		t.Fatalf("mixed inventory %v, want native %v", got, want)
	}
	m.prepared[1].Count = 0
	want = append(want[:1], want[2:]...)
	if got := m.mixedSlots(); !reflect.DeepEqual(got, want) {
		t.Fatalf("exhausted prop left a reserved hole: %v", got)
	}
}

func TestMixedSlotsPreserveGrantChronologyAndReacquisition(t *testing.T) {
	m := testMatch(t)
	m.engine.GrantHeldAction(1, 41, 1)
	m.prepared = []preparedView{{Item: 20020, Count: 5}}
	m.initSlotOrder()
	m.engine.GrantHeldAction(1, 64, 2)
	if got := m.mixedSlots(); !reflect.DeepEqual(got, [][2]uint32{{41, 1}, {20020, 5}, {64, 2}}) {
		t.Fatalf("initial rule-granted item changed position: %v", got)
	}
	// No intermediate host reply: the old stack must still lose its position.
	m.engine.ConsumeHeldAction(1, 41)
	m.engine.GrantHeldAction(1, 41, 1)
	if got := m.mixedSlots(); !reflect.DeepEqual(got, [][2]uint32{{20020, 5}, {64, 2}, {41, 1}}) {
		t.Fatalf("reacquired item kept its exhausted stack's position: %v", got)
	}
}
