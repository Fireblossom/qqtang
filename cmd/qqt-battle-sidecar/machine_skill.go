package main

// Rule 7's charged skills (招式): REQUEST_WRESTLE 0x115C and
// NOTIFY_WRESTLE_SKILL 0x115B. docs/wrestle-skills-re.md has the evidence.
//
// The host (client/native/qqtclient/battle/machine_skill.py) runs the input
// side: the bubble-key charge, the combo recognizer (0x005CAE1D, table
// 0x007A23D4) and the sender's debounce and facing test (0x006184BA). It
// asks for a 0x115C with action.skill. Here:
//   - the request is built and sent to the arbitrator (0x006184BA);
//   - the arbitrator checks the 2 s per-player cooldown and that the player
//     faces the other half, turning a failed request into type 9, and
//     sends 0x115B (0x005FFF01);
//   - every client applies 0x115B (0x005FF2B8): the pose, and the dodge of
//     types 6/7; the arbitrator alone throws the bubbles (0x1159, types
//     0..5) or kicks the bubble in front (0x139C, types 6/7).

import (
	"qqtang/internal/game/battleengine"
	"qqtang/internal/protocol/game"
)

const (
	machineSkillCharge     = 8 // charging pose, misc470
	machineSkillRefused    = 9
	machineSkillCooldownMS = 2000 // 0x005FFFF0
	machineSkillKick       = 6
	machineSkillDodge      = 7
)

// machineSkillThrowOffsets are the packed-cell steps (row<<4|col, 8-bit
// wrap) of the three extra bubbles after the one on the target cell:
// types 0/1 凤舞 0x005FF40C, 2/3 长龙 0x005FF58E, 4/5 天牢 0x005FF712.
var machineSkillThrowOffsets = [3][3]byte{
	{0x0f, 0xf1, 0xe2}, // +0x0F, -0x0F, -0x1E: an anti-diagonal of four
	{0x10, 0xf0, 0xe0}, // +0x10, -0x10, -0x20: a column of four
	{0x11, 0xf1, 0x02}, // +0x11, -0x0F, +0x02: four around (row, col+1)
}

type machineSkillState struct {
	lastUse map[uint16]uint32 // arbitrator's cooldown table
	notices []machineSkillNotice
}

// machineSkillNotice is one applied 0x115B for the host's poses: Pose is the
// pose index of 0x005AE26F (0..5 = Type, 6 kick, 7 dodge, 8 charge), or -1.
type machineSkillNotice struct {
	Player uint16 `json:"player"`
	Type   byte   `json:"type"`
	Pose   int8   `json:"pose"`
	Time   uint32 `json:"time"`
}

func (match *onlineMatch) takeMachineSkillNotices() []machineSkillNotice {
	if match.machine == nil {
		return nil
	}
	notices := match.machine.skill.notices
	match.machine.skill.notices = nil
	return notices
}

func (match *onlineMatch) machineFirstTeam(actor battleengine.Actor) bool {
	return actor.TeamID == match.machine.teams[0]
}

// requestMachineSkill sends the host's 0x115C. Time is not written by the
// original sender (its stack slot is left as it was); the scene clock is used.
func (match *onlineMatch) requestMachineSkill(skill int8) []battleengine.Event {
	if match.machine == nil || skill < 0 || skill > machineSkillCharge || match.machineFinishing() {
		return nil
	}
	actor, ok := actorByID(match.engine.Actors(), match.local)
	if !ok || actor.State != battleengine.ActorActive {
		return nil
	}
	cell := actor.Position.Cell()
	request := game.WrestleAction{
		SkillType: nativeWalkDirection(actor.Facing)<<4 | byte(skill),
		PlayerPos: byte(cell.Row<<4 | cell.Col), PlayerID: match.local, Time: match.engine.ElapsedMS(),
	}
	if match.machineFirstTeam(actor) {
		request.FirstTeam = 1
	}
	body, err := request.MarshalNetworkBinary()
	if err != nil {
		return nil
	}
	match.request(game.RequestWrestle, request.Time, body)
	match.mirror(game.RequestWrestle, body)
	if match.isArbitrator() {
		return match.arbitrateMachineSkill(request)
	}
	return nil
}

// arbitrateMachineSkill is 0x005FFF01.
func (match *onlineMatch) arbitrateMachineSkill(request game.WrestleAction) []battleengine.Event {
	if _, ok := actorByID(match.engine.Actors(), request.PlayerID); !ok {
		return nil
	}
	skill, facing := request.SkillType&0x0f, request.SkillType>>4
	now := match.engine.ElapsedMS()
	if skill < machineSkillCharge && !match.machine.skill.cooldownPasses(request.PlayerID, now) {
		request.SkillType = facing<<4 | machineSkillRefused
	}
	if skill < machineSkillCharge {
		// Only towards the other half: first team right (0), second left (2).
		wanted := byte(2)
		if request.FirstTeam != 0 {
			wanted = 0
		}
		if facing != wanted {
			request.SkillType = facing<<4 | machineSkillRefused
		}
	}
	body, err := request.MarshalNetworkBinary()
	if err != nil {
		return nil
	}
	match.queue(game.NotifyWrestleSkill, now, now, body)
	match.mirror(game.NotifyWrestleSkill, body)
	return match.applyMachineSkill(request)
}

// cooldownPasses is 0x005FFFF0: a skill needs the previous one at least
// 2000 ms back (unsigned, so the first seconds of a match always pass), and
// every test records the new time, passed or not.
func (state *machineSkillState) cooldownPasses(player uint16, now uint32) bool {
	if state.lastUse == nil {
		state.lastUse = map[uint16]uint32{}
	}
	last := state.lastUse[player]
	state.lastUse[player] = now
	return last < now-machineSkillCooldownMS
}

// applyMachineSkill is the 0x115B handler 0x005FF2B8.
func (match *onlineMatch) applyMachineSkill(notice game.WrestleAction) []battleengine.Event {
	if _, ok := actorByID(match.engine.Actors(), notice.PlayerID); !ok {
		return nil
	}
	skill := notice.SkillType & 0x0f
	pose := int8(-1)
	if skill < 6 {
		pose = int8(skill)
	}
	var events []battleengine.Event
	switch {
	case skill < 6:
		if match.isArbitrator() {
			events = match.throwMachineSkill(notice, skill)
		}
	case skill == machineSkillKick || skill == machineSkillDodge:
		pose, events = match.kickOrDodge(notice)
	case skill == machineSkillCharge:
		pose = machineSkillCharge
	case skill == machineSkillRefused:
	default:
		return nil
	}
	match.machine.skill.notices = append(match.machine.skill.notices, machineSkillNotice{Player: notice.PlayerID, Type: skill, Pose: pose, Time: match.engine.ElapsedMS()})
	match.logf("player %d skill %d (pose %d)", notice.PlayerID, skill, pose)
	return events
}

// throwMachineSkill: the arbitrator throws one bubble to the target cell and
// up to three more to the cells of the pattern that are on the other half
// (0x005FFDAC: row 0..12, col 8..14 for the first team, 0..6 otherwise, no
// map element, no bubble), each as an ordinary 0x1159 from the packed
// position of the request. All four share one time; the destination tells
// them apart (0x005D3D74).
func (match *onlineMatch) throwMachineSkill(notice game.WrestleAction, skill byte) []battleengine.Event {
	actor, ok := actorByID(match.engine.Actors(), notice.PlayerID)
	if !ok || actor.State != battleengine.ActorActive {
		return nil
	}
	target, ok := machineSkillTarget(match.engine, actor.Position.Cell(), actor.Facing, match.machineFirstTeam(actor))
	if !ok {
		return nil
	}
	now := match.engine.ElapsedMS()
	throw := game.KickBombAction{
		BombRowAndCol: notice.PlayerPos, Direction: notice.SkillType >> 4, Power: actor.BombPower + 1,
		Time: now, PlayerID: notice.PlayerID, BombPower: uint16(actor.BombPower + 1),
	}
	if actor.SceneFourEffectExpiresAt > now {
		throw.BombProp = 1
	}
	base := byte(target.Row<<4 | target.Col)
	destinations := []byte{base}
	for _, step := range machineSkillThrowOffsets[skill/2] {
		if cell := base + step; machineSkillExtraCell(match.engine, cell, notice.FirstTeam != 0) {
			destinations = append(destinations, cell)
		}
	}
	var events []battleengine.Event
	for _, destination := range destinations {
		throw.BombDestRowAndCol = destination
		thrown := match.applyMachineThrow(throw)
		if len(thrown) == 0 {
			continue
		}
		events = append(events, thrown...)
		if body, err := throw.MarshalNetworkBinary(); err == nil {
			match.queue(game.PlayerThrowBomb, now, now, body)
			match.mirror(game.PlayerThrowBomb, body)
		}
	}
	return events
}

func machineSkillExtraCell(engine *battleengine.Engine, packed byte, firstTeam bool) bool {
	cell := battleengine.Cell{Row: int16(packed >> 4), Col: int16(packed & 0x0f)}
	lo, hi := int16(0), int16(6)
	if firstTeam {
		lo, hi = 8, 14
	}
	if cell.Row > 12 || cell.Col < lo || cell.Col > hi {
		return false
	}
	return machineSkillCellFree(engine, cell)
}

func machineSkillCellFree(engine *battleengine.Engine, cell battleengine.Cell) bool {
	tile, ok := engine.Grid().Cell(cell)
	if !ok || tile.MapElementOccupied || tile.Kind != battleengine.CellOpen {
		return false
	}
	for _, bomb := range engine.Bombs() {
		if bomb.Cell == cell {
			return false
		}
	}
	return true
}

// machineSkillTarget is 0x005FFB05: the same search as the aiming cursor
// (00619019), but the half is chosen by team, not by column: from 6 or 7
// cells ahead to the far edge, wrapping, then back towards the player. For
// a horizontal facing the cell must be on the other half (first team
// 7..14, second 0..7).
func machineSkillTarget(engine *battleengine.Engine, from battleengine.Cell, direction battleengine.Direction, firstTeam bool) (battleengine.Cell, bool) {
	dr, dc := int16(0), int16(0)
	switch direction {
	case battleengine.DirectionRight:
		dc = 1
	case battleengine.DirectionLeft:
		dc = -1
	case battleengine.DirectionUp:
		dr = -1
	case battleengine.DirectionDown:
		dr = 1
	default:
		return from, false
	}
	lo, hi := int16(0), int16(7)
	if firstTeam {
		lo, hi = 7, 14
	}
	base := from.Col
	if base+dc >= 0 && base+dc < 15 {
		base += dc
	}
	first := int16(6)
	if base == from.Col {
		first = 7
	}
	valid := func(n int16) (battleengine.Cell, bool) {
		to := battleengine.Cell{Row: (from.Row + dr*n + 13) % 13, Col: (base + dc*n + 15) % 15}
		if dc != 0 && (to.Col < lo || to.Col > hi) {
			return to, false
		}
		return to, to != from && machineSkillCellFree(engine, to)
	}
	for n := first; n < 15; n++ {
		if cell, ok := valid(n); ok {
			return cell, true
		}
	}
	for n := first - 1; n > 0; n-- {
		if cell, ok := valid(n); ok {
			return cell, true
		}
	}
	return from, false
}

// kickOrDodge is 0x005FF896 (types 6 and 7 alike). With exactly one bubble
// in the cell in front of the packed position (towards the other half) the
// player kicks it (pose 6): the arbitrator sends 0x139C to the skill target.
// Otherwise every client moves the player one cell back if it can (pose 7)
// and gives it 300 ms of harm protection either way.
func (match *onlineMatch) kickOrDodge(notice game.WrestleAction) (int8, []battleengine.Event) {
	row, col := int16(notice.PlayerPos>>4), int16(notice.PlayerPos&0x0f)
	front, back := col-1, col+1
	if notice.FirstTeam != 0 {
		front, back = col+1, col-1
	}
	source := battleengine.Cell{Row: row, Col: front}
	var inFront []battleengine.Bomb
	for _, bomb := range match.engine.Bombs() {
		if bomb.Cell == source {
			inFront = append(inFront, bomb)
		}
	}
	if len(inFront) == 1 {
		if match.isArbitrator() {
			return machineSkillKick, match.kickMachineSkill(notice, source, inFront[0])
		}
		return machineSkillKick, nil
	}
	moved, err := match.engine.ApplyMachineSkillDodge(notice.PlayerID, row, back)
	if err != nil {
		match.logf("skill dodge of %d: %v", notice.PlayerID, err)
	}
	if moved {
		return machineSkillDodge, nil
	}
	return -1, nil
}

func (match *onlineMatch) kickMachineSkill(notice game.WrestleAction, source battleengine.Cell, bomb battleengine.Bomb) []battleengine.Event {
	actor, ok := actorByID(match.engine.Actors(), notice.PlayerID)
	known, placed := match.bombs[bomb.ID]
	if !ok || !placed {
		return nil
	}
	target, ok := machineSkillTarget(match.engine, actor.Position.Cell(), actor.Facing, match.machineFirstTeam(actor))
	if !ok {
		// The original sends whatever its output shorts held; send nothing.
		return nil
	}
	moved := game.MoveBombEvent{
		PlayerID: notice.PlayerID, ClientTime: uint16(notice.Time),
		FromRow: uint16(source.Row), FromCol: uint16(source.Col), ToRow: uint16(target.Row), ToCol: uint16(target.Col),
		Direction: notice.SkillType >> 4 & 3, BombPlayerID: known.owner, BombTime: known.placedAt, BombPower: bomb.Power + 1,
	}
	body, err := moved.MarshalNetworkBinary()
	if err != nil {
		return nil
	}
	accepted, err := match.runtime.AcceptNativeBombMovement(notice.PlayerID, bomb.ID, source, target)
	if err != nil {
		match.logf("skill kick by %d refused: %v", notice.PlayerID, err)
		return nil
	}
	match.moveWireBomb(bomb.ID, target)
	now := match.engine.ElapsedMS()
	match.queue(game.NotifyPlayerMoveBomb, now, now, body)
	match.mirror(game.NotifyPlayerMoveBomb, body)
	match.noteKick(match.engine, accepted, moved.Direction)
	return []battleengine.Event{accepted}
}

// machineSkillRemote routes the two schemas: 0x115C to the arbitrator,
// 0x115B from it.
func (match *onlineMatch) machineSkillRemote(sender uint16, event game.GameEvent) ([]battleengine.Event, bool) {
	if event.Schema != game.RequestWrestle && event.Schema != game.NotifyWrestleSkill {
		return nil, false
	}
	if match.machine == nil {
		return nil, true
	}
	action, err := game.ParseWrestleAction(event)
	if err != nil {
		return nil, true
	}
	if event.Schema == game.RequestWrestle {
		if action.PlayerID == sender && match.isArbitrator() {
			return match.arbitrateMachineSkill(action), true
		}
		return nil, true
	}
	if sender != match.arbitrator || match.isArbitrator() {
		return nil, true
	}
	return match.applyMachineSkill(action), true
}
