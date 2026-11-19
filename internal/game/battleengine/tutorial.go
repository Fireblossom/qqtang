package battleengine

import (
	"fmt"
	"sort"

	"qqtang/internal/game/mapdata"
)

// The newbie tutorial (native rule 9, maps 1501..1504, then map 311) is a
// local game whose Task.txt controller (Client.exe vtable 0x007A2348) adds
// actors, moves them and traps them while the game runs. These entry points
// serve only that controller (cmd/qqt-battle-sidecar tutorial mode); no other
// caller uses them. See client/native/docs/tutorial-re.md.

// TutorialRoundMS is rule 9's game time (vtable slot 0x006097D3).
const TutorialRoundMS = 240_000

// TutorialMapConfigOptions describes one tutorial stage's local GAME_BEGIN.
type TutorialMapConfigOptions struct {
	SpawnSeed    uint32
	ItemSeed     uint32
	Rules        Rules
	Participants []Participant
	// WallItems are the hidden items for the stage's breakable cells.
	WallItems []mapdata.CompetitiveWallItem
}

// ConfigFromTutorialMap is ConfigFromCompetitiveMap for the tutorial's local
// game: any native rule (9 for the stages, 1 for map 311), the local player
// alone, the given hidden items and Rules.NativeTutorial with an external
// conclusion. The engine's own deadline is pushed past the stage timers the
// caller keeps (240 s, 70 s on 1504).
func ConfigFromTutorialMap(entry mapdata.CompetitiveMap, options TutorialMapConfigOptions) (Config, error) {
	grid, err := gridFromCompetitiveMap(entry, true)
	if err != nil {
		return Config{}, err
	}
	rules := options.Rules
	rules.NativeTutorial = true
	rules.ExternalConclusion = true
	rules.StartClockMS = NativeRoundStartClockMS
	rules.RoundDurationMS = 10 * TutorialRoundMS
	rules.BombFuseMS = NativeBombFuseMS
	rules.FlameDurationMS = NativeFlameDurationMS
	rules.ActorHalfSizePixels = NativeActorHalfSizePixels
	rules.SpeedPixelsPerSecondByRate = nativeSpeedPixelsPerSecondByRate
	participants, err := placeNativeSpawns(options.SpawnSeed, NativeSpawnFree, competitiveCells(entry.SpawnGroupA), competitiveCells(entry.SpawnGroupB), options.Participants, false)
	if err != nil {
		return Config{}, fmt.Errorf("tutorial map %d: %w", entry.ID, err)
	}
	pickups, err := hiddenPickupsFromWallItems(entry, options.ItemSeed, options.WallItems, rules, true)
	if err != nil {
		return Config{}, err
	}
	seed := uint64(options.SpawnSeed)<<32 | uint64(options.ItemSeed)
	return Config{Seed: seed, Grid: grid, Rules: rules, Participants: participants, Pickups: pickups}, nil
}

// AddActor puts a new actor on an open cell during a tutorial game (action 6,
// 0x005C6425, and action 15). The participant carries its own attributes:
// the controller gives every scripted actor 2 bubbles of power 2.
func (engine *Engine) AddActor(participant Participant, cell Cell) error {
	if engine == nil {
		return fmt.Errorf("battle engine is nil")
	}
	if !engine.rules.NativeTutorial {
		return fmt.Errorf("adding actors needs tutorial rules")
	}
	if participant.PlayerID == 0 || participant.TeamID == 0 {
		return fmt.Errorf("added actor needs a player and a team ID")
	}
	if engine.actorIndex(participant.PlayerID) >= 0 {
		return fmt.Errorf("actor %d already exists", participant.PlayerID)
	}
	if len(engine.actors) >= MaxParticipants {
		return fmt.Errorf("battle already has %d actors", len(engine.actors))
	}
	if participant.BombCapacity == 0 || participant.BombPower == 0 || participant.SpeedRate == 0 {
		return fmt.Errorf("added actor %d needs bubbles, power and a speed rate", participant.PlayerID)
	}
	participant.MaxBombCapacity = max(participant.MaxBombCapacity, participant.BombCapacity)
	participant.MaxBombPower = max(participant.MaxBombPower, participant.BombPower)
	participant.MaxSpeedRate = max(participant.MaxSpeedRate, participant.SpeedRate)
	if participant.MaxSpeedRate > MaxNativeSpeedRate {
		return fmt.Errorf("added actor %d has invalid speed rate %d", participant.PlayerID, participant.MaxSpeedRate)
	}
	participant.SpeedPixelsPerSecond = engine.rules.SpeedPixelsPerSecondByRate[participant.SpeedRate]
	if participant.SpeedPixelsPerSecond == 0 {
		return fmt.Errorf("added actor %d speed rate %d has no projection", participant.PlayerID, participant.SpeedRate)
	}
	if tile, inside := engine.grid.Cell(cell); !inside || tile.Kind != CellOpen {
		return fmt.Errorf("added actor %d cell %d,%d is not open", participant.PlayerID, cell.Row, cell.Col)
	}
	participant.Spawn = cell
	position := PositionAtCellCenter(cell)
	engine.actors = append(engine.actors, Actor{
		Participant: participant, Position: position, nativePreviousPosition: position, State: ActorActive, Facing: DirectionDown,
		OxygenValue: NativeOxygenValueInitial, initial: participant, DurabilityLeft: engine.rules.NativeDurabilityLayers,
		nativeActionInventory: engine.rules.NativeActionInventory,
	})
	sort.SliceStable(engine.actors, func(i, j int) bool { return engine.actors[i].PlayerID < engine.actors[j].PlayerID })
	return nil
}

// TeleportActor moves a living actor to a cell's centre (0x005AD322, used
// by actions 2, 5, 6 and the routine step 0x1A).
func (engine *Engine) TeleportActor(playerID uint16, cell Cell) error {
	if engine == nil {
		return fmt.Errorf("battle engine is nil")
	}
	index := engine.actorIndex(playerID)
	if index < 0 {
		return fmt.Errorf("teleport player %d is not a participant", playerID)
	}
	if _, inside := engine.grid.Cell(cell); !inside {
		return fmt.Errorf("teleport cell %d,%d is outside the map", cell.Row, cell.Col)
	}
	actor := &engine.actors[index]
	if actor.State == ActorEliminated {
		return nil
	}
	actor.nativePreviousPosition = actor.Position
	actor.Position = PositionAtCellCenter(cell)
	return nil
}

// TrapActor wraps a living actor in a bubble as a hit would (0x005ACAAB)
// and lengthens its trap by extraMS (actions 15 and 16 add 240000 ms).
func (engine *Engine) TrapActor(playerID uint16, extraMS uint32) (Event, error) {
	if engine == nil {
		return Event{}, fmt.Errorf("battle engine is nil")
	}
	event, _, err := engine.applyVerifiedActorHit(playerID, 0, Position{}, false, false)
	if err != nil {
		return Event{}, err
	}
	actor := &engine.actors[engine.actorIndex(playerID)]
	if actor.State == ActorTrapped && actor.TrapExpiresAt != 0 {
		actor.TrapExpiresAt = saturatingAdd(actor.TrapExpiresAt, extraMS)
	}
	return event, nil
}

// SetFlameImmune keeps flames from trapping an actor. The tutorial's local
// player is never hit on 1501..1503: 0x00607899 skips its flame test there.
func (engine *Engine) SetFlameImmune(playerID uint16, immune bool) error {
	if engine == nil {
		return fmt.Errorf("battle engine is nil")
	}
	index := engine.actorIndex(playerID)
	if index < 0 {
		return fmt.Errorf("flame-immune player %d is not a participant", playerID)
	}
	engine.actors[index].flameImmune = immune
	return nil
}
