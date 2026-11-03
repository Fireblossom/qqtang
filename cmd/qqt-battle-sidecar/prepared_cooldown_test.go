package main

import (
	"testing"

	"qqtang/internal/game/mapdata"
	"qqtang/internal/protocol/game"
)

func TestPreparedCooldownStartsOnConfirmationAndSharesTier(t *testing.T) {
	m := testMatch(t)
	m.prepared = []preparedView{{Item: 20020, Count: 3}, {Item: 20008, Count: 3}, {Item: 20058, Count: 3}}
	m.propTable = map[uint32]mapdata.AdventureProp{
		20020: {Tier: 3, InitialInterval: 20000},
		20008: {Tier: 3, InitialInterval: 20000},
		20058: {Tier: 102},
	}
	advanceTo(t, m, 19980)
	m.requestPreparedUse(20020)
	if len(m.takeTCP()) != 0 {
		t.Fatal("prop was usable before InitialInterval")
	}
	advanceTo(t, m, 20000)
	m.requestPreparedUse(20020)
	if len(m.takeTCP()) != 1 || m.preparedCount(20020) != 3 || m.preparedCooldown(20020).LastUse != 0 {
		t.Fatal("request changed inventory or started cooldown before confirmation")
	}
	body, _ := (game.PreparedUsePropEvent{PlayerID: 2, ItemID: 20020}).MarshalNetworkBinary()
	m.applyPreparedUse(body)
	if m.preparedCooldown(20020).LastUse != 0 {
		t.Fatal("another player's use started the local cooldown")
	}
	body, _ = (game.PreparedUsePropEvent{PlayerID: 1, ItemID: 20020}).MarshalNetworkBinary()
	m.applyPreparedUse(body)
	view := m.preparedView()
	if m.preparedCount(20020) != 2 || view[0].Cooldown.LastUse != 20000 || view[1].Cooldown.LastUse != 20000 || view[2].Cooldown.LastUse != 0 {
		t.Fatalf("Tier cooldown projection is wrong: %+v", view)
	}
	m.requestPreparedUse(20008)
	if len(m.takeTCP()) != 0 {
		t.Fatal("another item in the same Tier bypassed cooldown")
	}
	m.requestPreparedUse(20058)
	if len(m.takeTCP()) != 1 {
		t.Fatal("independent firework Tier was blocked")
	}
	advanceTo(t, m, 34980)
	m.requestPreparedUse(20020)
	if len(m.takeTCP()) != 0 {
		t.Fatal("reuse cooldown ended early")
	}
	advanceTo(t, m, 35000)
	m.requestPreparedUse(20020)
	if len(m.takeTCP()) != 1 {
		t.Fatal("mask's 400ms speed guard incorrectly extended usability")
	}
}

func TestPreparedCooldownUsesNativeModePredicate(t *testing.T) {
	m := testMatch(t)
	m.propTable = map[uint32]mapdata.AdventureProp{20003: {GameMode: 1}, 20050: {GameMode: 2}, 20020: {GameMode: 0}}
	if !m.preparedCooldown(20003).Allowed || m.preparedCooldown(20050).Allowed || !m.preparedCooldown(20020).Allowed {
		t.Fatal("competitive mode predicate changed")
	}
	if missing := m.preparedCooldown(20046); missing == nil || missing.Allowed {
		t.Fatal("missing native prop definition was shown as usable")
	}
	m.adventure = &adventureRule{}
	if m.preparedCooldown(20003).Allowed || !m.preparedCooldown(20050).Allowed || !m.preparedCooldown(20020).Allowed {
		t.Fatal("adventure mode predicate changed")
	}
}
