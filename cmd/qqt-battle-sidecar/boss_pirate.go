package main

// The pirate interpreter 0x005B2AC1 (RoleID 33 first mate, 34 Captain Hook;
// docs/boss-pirate-re.md). It ignores BOSS_INFO.Skills: 0x005B37C0 loads five
// fixed scripts per role, and one call runs a whole script in the same frame
// with one Time. A skill that cannot run aborts the script without advancing
// it, so the same script is retried; skills already sent stay sent. The gate
// is 5 s of game time, 5 s since the last skill sent, and 15 s after skill 7.

import (
	"qqtang/internal/game/battleengine"
	"qqtang/internal/protocol/game"
)

const (
	pirateStartMS     = 5000
	pirateSpacingMS   = 5000
	pirateRestMS      = 15_000
	pirateTrapCells   = 5
	pirateWaveScene   = 1 // wire power 2
	piratePowerScene  = 2 // wire power 5
	pirateFieldCols   = 15
	pirateFirstMate   = 33
	pirateCaptainHook = 34
)

// pirateScripts are DAT_007F2A20 (33) and DAT_007F2AD4 (34).
var pirateScripts = map[uint16][5][]int{
	pirateFirstMate:   {{9, 4, 6}, {6}, {6}, {6}, {7}},
	pirateCaptainHook: {{9, 4, 5, 6}, {6}, {6}, {8}, {7}},
}

// The lines sent after skills 4 (0x007F2CDC) and 6 (0x007F2BE8), GBK.
var (
	pirateThunderLine = "\x20\x20\x20\x20\xce\xde\xd4\xc6\xb5\xc4\xc7\xe7\xbf\xd5\x2c\xb3\xd9\xd3\xeb\xbf\xec\xb5\xc4\xd7\xe9\xba\xcf\x21\xd7\xf1\xb4\xd3\xb5\xe7\xd3\xeb\xbb\xf0\xd6\xae\xb9\xc5\xc0\xcf\xc6\xf5\xd4\xbc\x21\xc8\xa5\xb4\xa5\xbb\xf7\xcb\xfb\x21\x2d\x2d\xb5\xe7\xb9\xe2\xc0\xd7\xd8\xbd\x21"                                                                             // 无云的晴空……电光雷亟!
	pirateChaosLine   = "\x20\x20\x20\x20\xb1\xc8\xba\xda\xb0\xb5\xbb\xb9\xd2\xaa\xc6\xe1\xba\xda\xd5\xdf\x2c\xb1\xc8\xd2\xb9\xcd\xed\xbb\xb9\xd2\xaa\xc9\xee\xb3\xc1\xd5\xdf\x2c\xbb\xec\xe3\xe7\xba\xa3\xd6\xd0\xd7\xee\xd0\xe9\xce\xde\xb5\xc4\xb5\xc4\xb4\xe6\xd4\xda\x2c\xd2\xc0\xd1\xad\xd7\xc5\xc4\xa7\xbd\xe7\xcb\xe9\xc6\xac\xd6\xae\xd4\xb5\x2c\xbd\xb5\xc1\xd9\xc8\xcb\xbd\xe7" // 比黑暗还要漆黑者……降临人界
)

type pirateState struct {
	inited bool
	script int
	last   int
	at     [10]uint32 // skill s last sent (4..9)
}

func (ai *bossGeneral) pirateSkills(w *bossWorld, b *bossActor) {
	p := &ai.pirate
	now := w.now
	if !p.inited {
		p.inited, p.last = true, 1
		for s := range p.at {
			p.at[s] = now
		}
	}
	if now < pirateStartMS || p.last == 7 && now-p.at[7] < pirateRestMS {
		return
	}
	p.at[1] = 0 // N+0x780 is cleared on every call, so the initial last=1 passes
	if p.last <= 0 || now-p.at[p.last] < pirateSpacingMS {
		return
	}
	match := w.match
	body := game.NPCUseSkillEvent{ObjectID: b.id, Time: now, PosX: uint16(b.pos.X), PosY: uint16(b.pos.Y)}
	for _, s := range pirateScripts[b.role][p.script%5] {
		body.SkillID, body.Items = uint16(s), nil
		switch s {
		case 4:
			if b.role == pirateFirstMate && b.hp >= 8 {
				return
			}
			cells := match.bossSkillCells(pirateTrapCells)
			if len(cells) < pirateTrapCells {
				return
			}
			body.Items = skillItems(bossSceneTrap, cells)
			match.bossUseSkill(b, body)
			p.last, p.at[4] = 4, now
			match.bossTalk(b, now, pirateThunderLine)
		case 5, 7, 8:
			match.bossUseSkill(b, body)
			p.last, p.at[s] = s, now
		case 6:
			if b.role == pirateFirstMate && b.hp >= 5 {
				return
			}
			k := min(int(now/16000)+5, 15)
			cells := match.bossSkillCells(2 * k)
			if len(cells) < 2*k {
				return
			}
			body.Items = skillItems(pirateWaveScene, cells)
			match.bossUseSkill(b, body)
			cells = match.bossSkillCells(k)
			if len(cells) < k {
				return // the first wave is already out
			}
			body.Items = skillItems(piratePowerScene, cells)
			match.bossUseSkill(b, body)
			p.last, p.at[6] = 6, now
			match.bossTalk(b, now, pirateChaosLine)
		case 9:
			p.last, p.at[9] = 9, now
		}
	}
	p.script++
}

func (match *onlineMatch) bossTalk(b *bossActor, now uint32, words string) {
	body, err := (game.NPCTalkEvent{ObjectID: b.id, Words: []byte(words)}).MarshalNetworkBinary()
	if err != nil {
		return
	}
	match.queueNPC(&match.boss.outbox, b.id, game.NotifyNPCTalk, now, body)
	match.mirror(game.NotifyNPCTalk, body)
}

// pirateWave is receiver case 6 (0x005FDD7B): every item is a Boss bubble of
// the first item's type (power 5 for scene 2, else 2), flying along its row
// from an edge this client picks per bubble at 400 px/s.
func (match *onlineMatch) pirateWave(b *bossActor, skill game.NPCUseSkillEvent) {
	if len(skill.Items) == 0 {
		return
	}
	wire := byte(2)
	if skill.Items[0].SceneID == piratePowerScene {
		wire = 5
	}
	bombs := make([]battleengine.RuleBomb, 0, len(skill.Items))
	for _, item := range skill.Items {
		from := int16(0)
		if match.boss.rng.Intn(2) == 0 {
			from = pirateFieldCols - 1
		}
		bombs = append(bombs, battleengine.RuleBomb{Cell: battleengine.Cell{Row: int16(item.Row), Col: int16(item.Col)}, Power: wire - 1, FromCol: from})
	}
	events, err := match.engine.DropRuleBatch(b.id, skill.Time, bombs, nil)
	if err != nil {
		match.logf("Boss %d bubble wave: %v", b.id, err)
		return
	}
	index := 0
	for _, event := range events {
		if event.Kind != battleengine.EventBombPlaced || index >= len(bombs) {
			continue
		}
		drop := bombs[index]
		index++
		match.bombs[event.BombID] = wireBomb{owner: b.id, placedAt: skill.Time, cell: drop.Cell}
		if distance := drop.Cell.Col - drop.FromCol; distance != 0 {
			direction := byte(0)
			if distance < 0 {
				direction, distance = 2, -distance
			}
			match.boss.bombFlights[event.BombID] = kickFlight{From: cell{Row: drop.Cell.Row, Col: drop.FromCol}, Direction: direction, Cells: distance,
				Start: skill.Time, Until: skill.Time + uint32(distance)*battleengine.NativeBombFlightMSPerCell}
		}
	}
	match.boss.events = append(match.boss.events, events...)
}

// bossInput applies the pirates' bubble lock (receiver cases 5/8): each
// client locks only its own player; a Boss's death adds one more
// (0x005B9390), which lasts until the round ends.
func (match *onlineMatch) bossInput(action *battleengine.Action) {
	if match.boss != nil && match.boss.bubbleLock > 0 {
		action.PlaceBomb = false
	}
}
