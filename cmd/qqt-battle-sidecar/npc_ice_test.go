package main

import (
	"bufio"
	"encoding/binary"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"qqtang/internal/game/battleengine"
	"qqtang/internal/protocol/game"
)

func TestNPCIceMatchesNativeHandlerAndProtectedSpeedGetter(t *testing.T) {
	f, err := os.Open(filepath.Join("..", "..", "client", "native", "tests", "fixtures", "npc_ice_v848.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	scanner := bufio.NewScanner(f)
	type caseKey struct {
		base byte
		name string
	}
	states := map[caseKey]*boxNPCActor{}
	checked := 0
	for scanner.Scan() {
		var row struct {
			Kind, Case, Action string
			Base               byte
			Dt                 uint32
			Before, After      struct {
				Active bool
				Left   int32
				Rate   byte
			}
		}
		if err := json.Unmarshal(scanner.Bytes(), &row); err != nil {
			t.Fatal(err)
		}
		if row.Kind == "config" {
			if row.Action != "freeze" {
				t.Fatal(row.Action)
			}
			continue
		}
		key := caseKey{row.Base, row.Case}
		n := states[key]
		if n == nil {
			n = &boxNPCActor{speed: row.Base}
			states[key] = n
		}
		if n.ice.active != row.Before.Active || n.ice.left != row.Before.Left || n.effectiveNPCSpeed() != row.Before.Rate {
			t.Fatalf("native before %+v, state%+v", row, n.ice)
		}
		if row.Kind == "apply" {
			n.ice.refresh()
		} else {
			n.ice.frame(row.Dt)
		}
		if n.ice.active != row.After.Active || n.ice.left != row.After.Left || n.effectiveNPCSpeed() != row.After.Rate {
			t.Fatalf("native after %+v, state%+v", row, n.ice)
		}
		checked++
	}
	if err := scanner.Err(); err != nil {
		t.Fatal(err)
	}
	if checked != 36 {
		t.Fatalf("native cases%d", checked)
	}
}

func TestBoxNPCIceObservationPreservesBaseSpeedAndExpiresWithoutDeath(t *testing.T) {
	m, peer := npcTestMatch(t, 1, 1), npcTestMatch(t, 2, 1)
	n, r := addTestNPC(t, m, 20001, 27), addTestNPC(t, peer, 20001, 27)
	m.boxSpecialNPCHit(n.pos.Cell(), 10000, 0)
	if !n.ice.active || n.dead || n.trapped || n.speed != 3 {
		t.Fatal("ice changed base attributes or retired NPC")
	}
	ices := mirrored(t, m, game.NotifyPlayerTrapped)
	if len(ices) != 1 || len(ices[0].Body) != 6 || binary.BigEndian.Uint16(ices[0].Body) != n.id {
		t.Fatal("missing native six-byte ice observation")
	}
	peer.boxNPCRemote(1, ices[0])
	n.navigator.walking, n.navigator.direction = true, 0
	start := n.pos
	m.advanceBoxNPC(n, 20)
	if n.pos.X-start.X != 2 || n.ice.left != 9980 {
		t.Fatalf("wrong NPC rate2 movement %+v ice%+v", n.pos, n.ice)
	}
	out := m.box.npc.outbox[len(m.box.npc.outbox)-1]
	move, err := game.ParsePlayerMove(out.message.Data)
	if err != nil || move.Entries[0].Move.Speed != 2 {
		t.Fatalf("ice wire speed %+v %v", move, err)
	}
	peer.boxNPCRemote(1, game.GameEvent{Schema: game.PlayerMoveSchema, Body: out.message.Data})
	if r.speed != 3 || r.effectiveNPCSpeed() != 2 || !r.ice.active {
		t.Fatal("effective wire speed overwrote permanent NPC speed")
	}
	n.ice.frame(9980)
	if n.effectiveNPCSpeed() != 2 {
		t.Fatal("zero timer expired one update early")
	}
	n.ice.frame(1)
	if n.dead || n.effectiveNPCSpeed() != 3 {
		t.Fatal("ice expiration lost base speed or NPC identity")
	}
	before := len(m.box.npc.outbox)
	m.publishBoxNPCMove(n, 21, false)
	if len(m.box.npc.outbox) != before+1 {
		t.Fatal("rate change waited for movement heartbeat")
	}
}

func TestBoxNPCIceFieldTakesPriorityButSlowEffectDoesNotPreventLaterTrap(t *testing.T) {
	m := npcTestMatch(t, 1, 1)
	n := addTestNPC(t, m, 20001, 27)
	if _, err := m.engine.PlaceRuleNPCBomb(n.id, n.pos.Cell(), 3, 0, 0); err != nil {
		t.Fatal(err)
	}
	for len(m.engine.Flames()) == 0 {
		if m.engine.ElapsedMS() > 4000 {
			t.Fatal("no control flame")
		}
		if _, err := m.engine.Step(nil); err != nil {
			t.Fatal(err)
		}
	}
	now := m.engine.ElapsedMS()
	n.pos, n.lastTick = battleengine.PositionAtCellCenter(m.engine.Flames()[0].Cell), now
	m.box.effects = []boxEffect{{Cell: n.pos.Cell(), Style: 10000, Start: now, Until: now + 500}}
	m.advanceBoxNPC(n, now)
	if n.trapped || !n.ice.active {
		t.Fatal("mixed ice/fire field did not select ice")
	}
	m.box.effects = nil
	m.advanceBoxNPC(n, now+20)
	if !n.trapped {
		t.Fatal("lingering speed effect granted flame immunity")
	}
}

func TestBoxNPCInitialFireBurstTrapsWithoutWaitingForField(t *testing.T) {
	m := npcTestMatch(t, 1, 1)
	n := addTestNPC(t, m, 20001, 25)
	m.boxSpecialNPCHit(n.pos.Cell(), 10001, 4000)
	if !n.trapped || n.trappedAt != 4000 || len(mirrored(t, m, game.PlayerBeExploded)) != 1 {
		t.Fatal("initial fire burst skipped arbitrator-owned NPC")
	}
}
