package battleenv

import (
	"fmt"
	"math"

	"qqtang/internal/game/battleengine"
)

// RewardWeights holds every coefficient of the training reward. The design is
// the one of the original vectorized environment: a terminal outcome reward
// (normalized against the public initial team strength, scaled up for early
// decisions) plus small shaping terms. Combat shaping (trap, elimination,
// debuff, transformation) is paid only against other teams; a trap credit is
// reversible and is rolled back when a teammate rescues the victim.
//
// DefaultRewardWeights returns the historical constants. JSON field names are
// the worker configuration keys (see cmd/qqt-train-worker/config.go).
type RewardWeights struct {
	// TerminalScale multiplies the terminal outcome reward; ShapingScale
	// multiplies the sum of every non-terminal term of one decision.
	TerminalScale float32 `json:"terminal_scale"`
	ShapingScale  float32 `json:"shaping_scale"`

	// Placing a bomb is an action, not progress by itself; useful placements
	// already receive delayed credit through wall, trap, elimination and
	// terminal events.
	BombPlaced    float32 `json:"bomb_placed"`
	WallDestroyed float32 `json:"wall_destroyed"`
	Pickup        float32 `json:"pickup"`
	// Opening development bonuses are finite because walls and pickups are
	// consumed; they are paid before DevelopmentEndMS of round time.
	DevelopmentWallBonus   float32 `json:"development_wall_bonus"`
	DevelopmentPickupBonus float32 `json:"development_pickup_bonus"`
	// One bounded bonus for completing the development curriculum lesson.
	DevelopmentCourseCompletion float32 `json:"development_course_completion"`
	MovementDebuff              float32 `json:"movement_debuff"`
	// Trap credit for the bubble owner (and minus for the victim). The assist
	// pool is shared between owners of bubbles/flames that closed an exit.
	Trap            float32 `json:"trap"`
	TrapAssistTotal float32 `json:"trap_assist_total"`
	Elimination     float32 `json:"elimination"`
	// Rescues decay geometrically against RescueBudget per target and episode.
	Rescue               float32 `json:"rescue"`
	RescueBudget         float32 `json:"rescue_budget"`
	TransformationBroken float32 `json:"transformation_broken"`
	DevelopmentEndMS     uint32  `json:"development_end_ms"`
	// Potential-based approach shaping toward the nearest live enemy (after
	// DevelopmentEndMS); approaching earns what moving away gives back.
	EnemyApproachPerCell float32 `json:"enemy_approach_per_cell"`
	// A win at round time t pays base*(1+EarlyWinScale*remaining(t)); a loss
	// likewise with EarlyLossScale.
	EarlyWinScale  float32 `json:"early_win_scale"`
	EarlyLossScale float32 `json:"early_loss_scale"`
	// Draw outcome except in one-versus-coalition rooms (survival scoring).
	Draw float32 `json:"draw"`
	// Late-round anti-stall cost for a team without material progress.
	LateStallStartMS         uint32  `json:"late_stall_start_ms"`
	LateStallProgressGraceMS uint32  `json:"late_stall_progress_grace_ms"`
	LateStallBudget          float32 `json:"late_stall_budget"`
	LateStallMaxMultiplier   float32 `json:"late_stall_max_multiplier"`
	// Static blocked-cell occupancy (wall-invulnerability technique) shaping.
	FreeBlockedEntries          uint32  `json:"free_blocked_entries"`
	RepeatedBlockedEntryPenalty float32 `json:"repeated_blocked_entry_penalty"`
	BlockedEntryPenaltyCap      float32 `json:"blocked_entry_penalty_cap"`
	BlockedReentryWindowMS      uint32  `json:"blocked_reentry_window_ms"`
	RapidBlockedReentryPenalty  float32 `json:"rapid_blocked_reentry_penalty"`
	BlockedCellGraceMS          uint32  `json:"blocked_cell_grace_ms"`
	BlockedCellPenaltyPerSecond float32 `json:"blocked_cell_penalty_per_second"`
	// Friendly-fire penalty factor applied to the attacker's combat amount.
	FriendlyFireFactor float32 `json:"friendly_fire_factor"`
}

// DefaultRewardWeights returns the constants of the original environment.
func DefaultRewardWeights() RewardWeights {
	return RewardWeights{
		TerminalScale: 1, ShapingScale: 1,
		BombPlaced: 0, WallDestroyed: 0.02, Pickup: 0.01,
		DevelopmentWallBonus: 0.01, DevelopmentPickupBonus: 0.03,
		DevelopmentCourseCompletion: 0.25,
		MovementDebuff:              0.05,
		Trap:                        0.6, TrapAssistTotal: 0.6, Elimination: 1.0,
		Rescue: 0.2, RescueBudget: 0.4,
		TransformationBroken: 0.1,
		DevelopmentEndMS:     30_000,
		EnemyApproachPerCell: 0.005,
		EarlyWinScale:        1.0, EarlyLossScale: 1.0,
		Draw:                        -0.5,
		LateStallStartMS:            90_000,
		LateStallProgressGraceMS:    15_000,
		LateStallBudget:             0.2,
		LateStallMaxMultiplier:      3.0,
		FreeBlockedEntries:          2,
		RepeatedBlockedEntryPenalty: 0.02,
		BlockedEntryPenaltyCap:      0.1,
		BlockedReentryWindowMS:      5_000,
		RapidBlockedReentryPenalty:  0.05,
		BlockedCellGraceMS:          1_200,
		BlockedCellPenaltyPerSecond: 0.025,
		FriendlyFireFactor:          0.1,
	}
}

// defaultRewards is used by tests and by callers that leave Config.Rewards nil.
var defaultRewards = func() *RewardWeights { weights := DefaultRewardWeights(); return &weights }()

// Validate rejects non-finite coefficients and an inverted stall ramp.
func (weights RewardWeights) Validate() error {
	values := map[string]float32{
		"terminal_scale": weights.TerminalScale, "shaping_scale": weights.ShapingScale,
		"bomb_placed": weights.BombPlaced, "wall_destroyed": weights.WallDestroyed, "pickup": weights.Pickup,
		"development_wall_bonus": weights.DevelopmentWallBonus, "development_pickup_bonus": weights.DevelopmentPickupBonus,
		"development_course_completion": weights.DevelopmentCourseCompletion, "movement_debuff": weights.MovementDebuff,
		"trap": weights.Trap, "trap_assist_total": weights.TrapAssistTotal, "elimination": weights.Elimination,
		"rescue": weights.Rescue, "rescue_budget": weights.RescueBudget, "transformation_broken": weights.TransformationBroken,
		"enemy_approach_per_cell": weights.EnemyApproachPerCell, "early_win_scale": weights.EarlyWinScale,
		"early_loss_scale": weights.EarlyLossScale, "draw": weights.Draw, "late_stall_budget": weights.LateStallBudget,
		"late_stall_max_multiplier": weights.LateStallMaxMultiplier, "repeated_blocked_entry_penalty": weights.RepeatedBlockedEntryPenalty,
		"blocked_entry_penalty_cap": weights.BlockedEntryPenaltyCap, "rapid_blocked_reentry_penalty": weights.RapidBlockedReentryPenalty,
		"blocked_cell_penalty_per_second": weights.BlockedCellPenaltyPerSecond, "friendly_fire_factor": weights.FriendlyFireFactor,
	}
	for name, value := range values {
		if math.IsNaN(float64(value)) || math.IsInf(float64(value), 0) {
			return fmt.Errorf("reward weight %s is not finite", name)
		}
	}
	if weights.LateStallMaxMultiplier < 1 {
		return fmt.Errorf("reward weight late_stall_max_multiplier %g must be at least 1", weights.LateStallMaxMultiplier)
	}
	return nil
}

// rewardRoundDurationMS is the controllable round length of rule 1; it is an
// engine fact, not a tunable weight.
const rewardRoundDurationMS uint32 = battleengine.StandardRoundTimeMS - battleengine.NativeRoundStartClockMS

type teamMaterial struct {
	initial int
	live    int
}

func materialByTeam(actors []battleengine.Actor) map[byte]teamMaterial {
	result := make(map[byte]teamMaterial)
	for _, actor := range actors {
		material := result[actor.TeamID]
		material.initial++
		if actor.State != battleengine.ActorEliminated {
			material.live++
		}
		result[actor.TeamID] = material
	}
	return result
}

func (weights *RewardWeights) timeoutDrawReward(teamID byte, material map[byte]teamMaterial) float32 {
	team := material[teamID]
	if team.initial <= 0 || len(material) < 2 {
		return weights.Draw
	}
	totalRetention := float32(0)
	for _, candidate := range material {
		if candidate.initial > 0 {
			totalRetention += float32(candidate.live) / float32(candidate.initial)
		}
	}
	if totalRetention <= 0 {
		return initialOutcomeReward(teamID, 1/float32(len(material)), material)
	}
	teamRetention := float32(team.live) / float32(team.initial)
	return initialOutcomeReward(teamID, teamRetention/totalRetention, material)
}

// oneVersusCoalition reports only the native room layout described by 1Vn:
// exactly two teams, one with one player and the other with several same-team
// players. FFA, equal teams and other uneven layouts do not receive the draw
// survival exception.
func oneVersusCoalition(material map[byte]teamMaterial) bool {
	if len(material) != 2 {
		return false
	}
	var solo, coalition bool
	for _, team := range material {
		switch {
		case team.initial == 1:
			solo = true
		case team.initial > 1:
			coalition = true
		default:
			return false
		}
	}
	return solo && coalition
}

// initialOutcomeReward normalizes an outcome score against the squared
// initial team sizes: a coalition's strength is not linear in its seat count.
func initialOutcomeReward(teamID byte, score float32, material map[byte]teamMaterial) float32 {
	team := material[teamID]
	totalInitialStrength := 0
	for _, candidate := range material {
		totalInitialStrength += candidate.initial * candidate.initial
	}
	teamCount := len(material)
	if team.initial <= 0 || totalInitialStrength <= 0 || teamCount < 2 {
		return 2*score - 1
	}
	expected := float32(team.initial*team.initial) / float32(totalInitialStrength)
	scale := float32(teamCount) / float32(teamCount-1)
	return scale * (score - expected)
}

// terminalOutcomeReward is unscaled; Batch applies TerminalScale.
func (weights *RewardWeights) terminalOutcomeReward(teamID byte, outcome battleengine.Outcome, material map[byte]teamMaterial) float32 {
	if !outcome.Ended {
		return 0
	}
	if outcome.Draw {
		if oneVersusCoalition(material) {
			if outcome.TimedOut {
				return weights.timeoutDrawReward(teamID, material)
			}
			return initialOutcomeReward(teamID, 1/float32(len(material)), material)
		}
		return weights.Draw
	}
	elapsed := outcome.EndedAtMS
	if elapsed > rewardRoundDurationMS {
		elapsed = rewardRoundDurationMS
	}
	remaining := float32(rewardRoundDurationMS-elapsed) / float32(rewardRoundDurationMS)
	if teamID == outcome.WinnerTeamID {
		base := initialOutcomeReward(teamID, 1, material)
		return base * (1 + weights.EarlyWinScale*remaining)
	}
	base := initialOutcomeReward(teamID, 0, material)
	return base * (1 + weights.EarlyLossScale*remaining)
}

func (weights *RewardWeights) enemyApproachReward(elapsedMS uint32, previousDistance, currentDistance int) float32 {
	if elapsedMS < weights.DevelopmentEndMS {
		return 0
	}
	return float32(previousDistance-currentDistance) * weights.EnemyApproachPerCell
}

func (weights *RewardWeights) developmentProgressReward(elapsedMS uint32, base, openingBonus float32) float32 {
	if elapsedMS < weights.DevelopmentEndMS {
		return base + openingBonus
	}
	return base
}

// pickupProgressReward values only consequences proven by the authoritative
// pickup event. Attribute items scale with the amount that actually changed
// the actor; held-action pickups scale with the granted count. A fork is also
// one finite self-rescue. Every source item is finite, so none can be farmed.
func (weights *RewardWeights) pickupProgressReward(event battleengine.Event, elapsedMS uint32) float32 {
	if event.Attribute != battleengine.AttributeNone {
		if event.ValueAfter <= event.ValueBefore {
			return 0
		}
		units := float32(event.ValueAfter - event.ValueBefore)
		return (weights.Pickup + weights.DevelopmentPickupBonus) * units
	}
	units := float32(1)
	if event.Effect == battleengine.PickupEffectBattleAction && event.ActionCount > 0 {
		units = float32(event.ActionCount)
	}
	reward := weights.developmentProgressReward(
		elapsedMS,
		weights.Pickup*units,
		weights.DevelopmentPickupBonus*units,
	)
	if event.Effect == battleengine.PickupEffectBattleAction && event.ActionID == 63 {
		reward += weights.Rescue * units
	}
	return reward
}

// lateStallPenalty integrates an increasing density over one tick. Its
// full-round integral is LateStallBudget; progress can only remove charge.
func (weights *RewardWeights) lateStallPenalty(elapsedMS, lastProgressMS, tickMS uint32) float32 {
	if elapsedMS < weights.LateStallStartMS || elapsedMS >= rewardRoundDurationMS || elapsedMS-lastProgressMS <= weights.LateStallProgressGraceMS {
		return 0
	}
	if tickMS > rewardRoundDurationMS-elapsedMS {
		tickMS = rewardRoundDurationMS - elapsedMS
	}
	window := float64(rewardRoundDurationMS - weights.LateStallStartMS)
	start := float64(elapsedMS-weights.LateStallStartMS) / window
	end := float64(elapsedMS-weights.LateStallStartMS+tickMS) / window
	ramp := float64(weights.LateStallMaxMultiplier - 1)
	mass := ((end - start) + ramp*(end*end-start*start)/2) / (1 + ramp/2)
	return -weights.LateStallBudget * float32(mass)
}
