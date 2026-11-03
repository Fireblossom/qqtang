package battleengine

import (
	"fmt"

	"qqtang/internal/game/mapdata"
)

// adventureBombResume is the PVE player's bubble counter (0x005B9F1C,
// 0x005BA3CB): a placement uses one and restarts the refill timer, an
// explosion returns one, and BombResumeMS without a placement returns one.
func (engine *Engine) adventureBombResume(events []Event) {
	if !engine.rules.NativeAdventure {
		return
	}
	for index := range engine.actors {
		actor := &engine.actors[index]
		if actor.BombResumeMS == 0 {
			continue
		}
		live := map[uint32]struct{}{}
		for _, bomb := range engine.bombs {
			if bomb.OwnerID == actor.PlayerID && !bomb.RuleOwned {
				live[bomb.ID] = struct{}{}
			}
		}
		for id := range live {
			if _, known := actor.adventureBubbles[id]; !known {
				actor.adventureUsed++
				actor.adventureResume = 0
			}
		}
		for id := range actor.adventureBubbles {
			if _, still := live[id]; !still && actor.adventureUsed > 0 {
				actor.adventureUsed-- // 0x00606F28 returns one on the explosion
			}
		}
		actor.adventureBubbles = live
		if actor.adventureResume > actor.BombResumeMS {
			actor.adventureResume = 0
			if actor.adventureUsed >= 1 {
				actor.adventureUsed--
			}
		}
		actor.adventureResume += engine.rules.TickMS
	}
}

// AdventureConfigOptions are GAME_BEGIN's facts for one rule-12 stage.
type AdventureConfigOptions struct {
	SimulationSeed uint64
	ItemSeed       uint32
	// GameTimeSeconds is GAME_BEGIN's game time; rule 12's slot 1
	// (0x00608F83) multiplies it by 1000 for the round length.
	GameTimeSeconds uint32
	Rules           Rules
	// Participants in GAME_BEGIN order; 0x005B6E07 seats them on the
	// interleaved spawn table (every stage has only group A).
	Participants []Participant
	// WallItems are GAME_BEGIN's NewItems (the stage's materials), hidden in
	// the breakable walls by ItemSeed like competitive wall items.
	WallItems []mapdata.CompetitiveWallItem
}

// ConfigFromAdventureStage builds an engine for a rule-12 stage of any size.
func ConfigFromAdventureStage(field mapdata.AdventureBattlefield, options AdventureConfigOptions) (Config, error) {
	if field.NativeRule != 12 {
		return Config{}, fmt.Errorf("adventure stage %d uses native rule %d, want 12", field.ID, field.NativeRule)
	}
	entry := mapdata.CompetitiveMap{ID: field.ID, NativeRule: field.NativeRule, Battlefield: field.Battlefield,
		HiddenItemCells: field.HiddenItemCells, SpawnGroupA: field.SpawnGroupA, SpawnGroupB: field.SpawnGroupB}
	grid, err := gridFromCompetitiveMap(entry, true)
	if err != nil {
		return Config{}, err
	}
	rules := options.Rules
	rules.NativeAdventure = true
	rules.ExternalConclusion = true
	rules.StartClockMS = NativeRoundStartClockMS
	rules.RoundDurationMS = options.GameTimeSeconds * 1000
	rules.BombFuseMS = NativeBombFuseMS
	rules.FlameDurationMS = NativeFlameDurationMS
	rules.ActorHalfSizePixels = NativeActorHalfSizePixels
	rules.SpeedPixelsPerSecondByRate = nativeSpeedPixelsPerSecondByRate
	participants, err := placeNativeSpawns(0, NativeSpawnFree, competitiveCells(field.SpawnGroupA), competitiveCells(field.SpawnGroupB), options.Participants, false)
	if err != nil {
		return Config{}, fmt.Errorf("adventure stage %d: %w", field.ID, err)
	}
	pickups, err := hiddenPickupsFromWallItems(entry, options.ItemSeed, options.WallItems, rules, true)
	if err != nil {
		return Config{}, err
	}
	return Config{Seed: options.SimulationSeed, Grid: grid, Rules: rules, Participants: participants, Pickups: pickups}, nil
}

// SetActorFieldLock locks (or with h == 0 releases) the actor inside a
// battlefield rectangle; only rule 12 uses it (0x0061667E, 0x005B7C16).
func (engine *Engine) SetActorFieldLock(playerID uint16, row, col, h, w int16) error {
	if engine == nil || !engine.rules.NativeAdventure {
		return fmt.Errorf("battlefield lock requires the adventure rule")
	}
	index := engine.actorIndex(playerID)
	if index < 0 {
		return fmt.Errorf("battlefield lock player %d is not a participant", playerID)
	}
	actor := &engine.actors[index]
	actor.fieldLock, actor.fieldLockRect = h > 0 && w > 0, [4]int16{row, col, h, w}
	return nil
}

func (actor *Actor) fieldContains(cell Cell) bool {
	r := actor.fieldLockRect
	return cell.Row >= r[0] && cell.Row < r[0]+r[2] && cell.Col >= r[1] && cell.Col < r[1]+r[3]
}

// fieldAllows is 0x005B7C16: inside the locked field only cells of the
// field are reachable; outside it the lock does not hold the actor back.
func (actor *Actor) fieldAllows(cell Cell) bool {
	return !actor.fieldLock || !actor.fieldContains(actor.Position.Cell()) || actor.fieldContains(cell)
}

// AdjustAdventureAttributes adds NPC skill effects to a rule-12 actor
// (0x005FA37A): SpeedDel, BombPowerDel and BombCountDel change the speed
// rate, bubble power and bubble count by the given amounts until the same
// call with the opposite values (RestorePlayerAttr) takes them back. The
// effective values never go below 0 (speed, count) or 1 (power).
func (engine *Engine) AdjustAdventureAttributes(playerID uint16, speed, power, count int) error {
	if engine == nil || !engine.rules.NativeAdventure {
		return fmt.Errorf("skill attributes require the adventure rule")
	}
	index := engine.actorIndex(playerID)
	if index < 0 {
		return fmt.Errorf("skill attributes player %d is not a participant", playerID)
	}
	actor := &engine.actors[index]
	actor.adventureAttr[0] += speed
	actor.adventureAttr[1] += power
	actor.adventureAttr[2] += count
	return nil
}

func adventureAdjusted(base byte, delta, low, high int) byte {
	return byte(min(max(int(base)+delta, low), high))
}

func (engine *Engine) adventureBombPower(actor *Actor) byte {
	if !engine.rules.NativeAdventure {
		return actor.BombPower
	}
	return adventureAdjusted(actor.BombPower, actor.adventureAttr[1], 1, 255)
}

// IgniteBombAt is a rule-12 projectile prop's IsIgniteBomb: every resting
// bubble in cell takes 10,000,000 ms off its fuse and so explodes at the
// next step. It reports whether there was one.
func (engine *Engine) IgniteBombAt(cell Cell) bool {
	if engine == nil || !engine.rules.NativeAdventure {
		return false
	}
	lit := false
	for index := range engine.bombs {
		bomb := &engine.bombs[index]
		if bomb.Cell == cell && bomb.FlightUntilMS <= engine.elapsedMS {
			bomb.ExplodeAtMS, lit = engine.elapsedMS, true
		}
	}
	return lit
}
