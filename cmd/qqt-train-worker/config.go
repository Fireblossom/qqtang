package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"slices"

	"qqtang/internal/game/battleenv"
	"qqtang/internal/game/mapdata"
)

// Config is the worker configuration file (-config). Every field is
// optional; omitted fields take the defaults shown. Unknown keys are an
// error so that typos do not silently fall back to defaults.
//
//	{
//	  "envs": 64,                 // E: environments in this process
//	  "agents": 4,                // P: tensor slots per environment (fixed
//	                              //    capacity, >= every player count);
//	                              //    0 = max(player_counts)
//	  "rule": 1,                  // native rule; only 1 (ordinary) for now
//	  "maps": [],                 // map IDs; empty = every eligible map
//	  "exclude_maps": [],         // map IDs removed from the pool
//	  "player_counts": [2, 4],    // real players per episode, drawn
//	                              //    uniformly per reset (repeat a value to
//	                              //    weight it, e.g. [2, 2, 4, 8])
//	  "team_layouts": [],         // explicit TeamID layouts, e.g.
//	                              //    [[1,2],[1,1,2,2],[1,2,3,4]]; one
//	                              //    layout with the drawn player count is
//	                              //    chosen per reset. Empty = team_count
//	                              //    equal teams for every player count
//	  "team_count": 2,            // used only when team_layouts is empty
//	  "curriculum": {             // shares of resets (0..1, sum <= 1); the
//	                              //    courses apply to 2-player episodes
//	    "duel": 0,                // cleared map, developed attributes
//	    "blockade": 0,            // duel in low-degree corridors
//	    "chain_finisher": 0,      // preset rolling bubble chain conversion
//	    "bomb_escape": 0,         // starts right after placing under self
//	    "development": 0,         // opening wall/pickup lesson, then battle
//	    "late_duel_replay": 0,    // restart from a captured real late 1v1
//	    "stratify": false,        // shares = fixed env-slot occupancy
//	    "duel_bomb_capacity": 0,  // fixed capacity for duel/blockade (0=role)
//	    "chain_min_preset_roots": 0, "chain_max_preset_roots": 0,
//	    "chain_trigger_only": false, "chain_pressure_only": false,
//	    "bomb_escape_capacity": 0, "bomb_escape_detonations": 0,
//	    "development_productive_detonations": 0
//	  },
//	  "rewards": { ... },         // any battleenv.RewardWeights JSON key;
//	                              //    omitted keys keep their defaults
//	  "seed": 1,                  // base seed; same seed + actions = same
//	                              //    observations (any worker count)
//	  "workers": 0,               // goroutines; 0 = GOMAXPROCS
//	  "spatial_dtype": "f16",     // "f16" or "f32"
//	  "decision_cadence": "fixed",// "fixed": every live agent decides every
//	                              //    STEP, which advances 100 ms (spec).
//	                              //    "server": the live server's cadence;
//	                              //    an agent re-decides early when its
//	                              //    held move turns illegal or the engine
//	                              //    asks for an immediate decision
//	                              //    (native pass, blast within 400 ms).
//	                              //    A STEP then advances each env only to
//	                              //    its next due agent (1..5 ticks, listed
//	                              //    in the info JSON "ticks") and only due
//	                              //    agents are active.
//	  "random_phase": false,      // server cadence only: delay each agent's
//	                              //    first decision by 0..4 ticks like the
//	                              //    server's per-player phase
//	  "trap_duration_ms": 6000,   // server virtual-AI trap timeout
//	  "danger_horizon_ms": 3500   // must match the server (3500)
//	}
//
// The decision interval (100 ms), engine tick (20 ms) and tensor shape
// (117x13x15, 187 scalars, 45 actions) are fixed by the deployment contract
// and are not configurable.
type Config struct {
	Envs            int                     `json:"envs"`
	Agents          int                     `json:"agents"`
	Rule            int                     `json:"rule"`
	Maps            []uint32                `json:"maps"`
	ExcludeMaps     []uint32                `json:"exclude_maps"`
	PlayerCounts    []int                   `json:"player_counts"`
	TeamLayouts     [][]uint8               `json:"team_layouts"`
	TeamCount       int                     `json:"team_count"`
	Curriculum      CurriculumConfig        `json:"curriculum"`
	Rewards         battleenv.RewardWeights `json:"rewards"`
	Seed            uint64                  `json:"seed"`
	Workers         int                     `json:"workers"`
	SpatialDtype    string                  `json:"spatial_dtype"`
	DecisionCadence string                  `json:"decision_cadence"`
	RandomPhase     bool                    `json:"random_phase"`
	TrapDurationMS  uint32                  `json:"trap_duration_ms"`
	DangerHorizonMS uint32                  `json:"danger_horizon_ms"`
}

// CurriculumConfig holds the curriculum shares (fractions of resets) and the
// course parameters of battleenv.Config.
type CurriculumConfig struct {
	Duel                             float64 `json:"duel"`
	Blockade                         float64 `json:"blockade"`
	ChainFinisher                    float64 `json:"chain_finisher"`
	BombEscape                       float64 `json:"bomb_escape"`
	Development                      float64 `json:"development"`
	LateDuelReplay                   float64 `json:"late_duel_replay"`
	Stratify                         bool    `json:"stratify"`
	DuelBombCapacity                 byte    `json:"duel_bomb_capacity"`
	ChainMinPresetRoots              byte    `json:"chain_min_preset_roots"`
	ChainMaxPresetRoots              byte    `json:"chain_max_preset_roots"`
	ChainTriggerOnly                 bool    `json:"chain_trigger_only"`
	ChainPressureOnly                bool    `json:"chain_pressure_only"`
	BombEscapeCapacity               byte    `json:"bomb_escape_capacity"`
	BombEscapeDetonations            byte    `json:"bomb_escape_detonations"`
	DevelopmentProductiveDetonations byte    `json:"development_productive_detonations"`
}

const (
	tickMS         = 20
	decisionMS     = 100
	tensorChannels = battleenv.SpatialChannels
	tensorHeight   = 13
	tensorWidth    = 15
)

// DefaultConfig returns the configuration used for omitted keys.
func DefaultConfig() Config {
	return Config{
		Envs: 64, Rule: 1, PlayerCounts: []int{2, 4}, TeamCount: 2,
		Rewards: battleenv.DefaultRewardWeights(), Seed: 1, SpatialDtype: "f16", DecisionCadence: "fixed",
		TrapDurationMS: 6_000, DangerHorizonMS: 3_500,
	}
}

// LoadConfig reads a configuration file over the defaults. An empty path
// returns the defaults.
func LoadConfig(path string) (Config, error) {
	config := DefaultConfig()
	if path == "" {
		return config, config.normalize()
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return Config{}, err
	}
	return ParseConfig(data)
}

// ParseConfig decodes JSON over the defaults and validates the result.
func ParseConfig(data []byte) (Config, error) {
	config := DefaultConfig()
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&config); err != nil {
		return Config{}, fmt.Errorf("worker config: %w", err)
	}
	return config, config.normalize()
}

func (config *Config) normalize() error {
	if config.Rule != 1 {
		return fmt.Errorf("worker config: rule %d is not supported yet (only rule 1)", config.Rule)
	}
	if config.Envs <= 0 {
		return fmt.Errorf("worker config: envs must be positive")
	}
	if len(config.PlayerCounts) == 0 {
		return fmt.Errorf("worker config: player_counts is empty")
	}
	maximum := 0
	for _, count := range config.PlayerCounts {
		if count < 2 || count > 8 {
			return fmt.Errorf("worker config: player count %d is outside 2..8", count)
		}
		maximum = max(maximum, count)
	}
	if config.Agents == 0 {
		config.Agents = maximum
	}
	if config.Agents < maximum || config.Agents > 8 {
		return fmt.Errorf("worker config: agents %d must cover player counts (max %d) and stay <= 8", config.Agents, maximum)
	}
	switch config.SpatialDtype {
	case "f16", "f32":
	default:
		return fmt.Errorf("worker config: spatial_dtype %q must be f16 or f32", config.SpatialDtype)
	}
	switch config.DecisionCadence {
	case "fixed", "server":
	default:
		return fmt.Errorf("worker config: decision_cadence %q must be fixed or server", config.DecisionCadence)
	}
	if config.RandomPhase && config.DecisionCadence != "server" {
		return fmt.Errorf("worker config: random_phase needs decision_cadence server")
	}
	if config.Workers < 0 {
		return fmt.Errorf("worker config: workers must not be negative")
	}
	shares := []float64{
		config.Curriculum.Duel, config.Curriculum.Blockade, config.Curriculum.ChainFinisher,
		config.Curriculum.BombEscape, config.Curriculum.Development, config.Curriculum.LateDuelReplay,
	}
	total := 0.0
	for _, share := range shares {
		if share < 0 || share > 1 {
			return fmt.Errorf("worker config: curriculum share %g is outside 0..1", share)
		}
		total += share
	}
	if total-config.Curriculum.LateDuelReplay > 1+1e-9 {
		return fmt.Errorf("worker config: curriculum shares sum to more than 1")
	}
	return config.Rewards.Validate()
}

func permille(share float64) uint16 {
	return uint16(share*1000 + 0.5)
}

// BatchConfig converts the file configuration into the environment config.
func (config Config) BatchConfig(maps []mapdata.CompetitiveMap) battleenv.Config {
	rewards := config.Rewards
	curriculum := config.Curriculum
	return battleenv.Config{
		Maps: maps, EnvCount: config.Envs,
		ParticipantCount: config.Agents, ParticipantCounts: slices.Clone(config.PlayerCounts),
		TeamCount: config.TeamCount, TeamLayouts: cloneLayouts(config.TeamLayouts),
		DuelCurriculumPermille:                   permille(curriculum.Duel),
		BlockadeCurriculumPermille:               permille(curriculum.Blockade),
		ChainFinisherCurriculumPermille:          permille(curriculum.ChainFinisher),
		BombEscapeCurriculumPermille:             permille(curriculum.BombEscape),
		DevelopmentCurriculumPermille:            permille(curriculum.Development),
		LateDuelReplayPermille:                   permille(curriculum.LateDuelReplay),
		StratifyCurriculumSlots:                  curriculum.Stratify,
		DuelCurriculumBombCapacity:               curriculum.DuelBombCapacity,
		ChainFinisherMinimumPresetRoots:          curriculum.ChainMinPresetRoots,
		ChainFinisherMaximumPresetRoots:          curriculum.ChainMaxPresetRoots,
		ChainFinisherTriggerOnly:                 curriculum.ChainTriggerOnly,
		ChainFinisherPressureOnly:                curriculum.ChainPressureOnly,
		BombEscapeCapacity:                       curriculum.BombEscapeCapacity,
		BombEscapeRequiredDetonations:            curriculum.BombEscapeDetonations,
		DevelopmentRequiredProductiveDetonations: curriculum.DevelopmentProductiveDetonations,
		WorkerCount:                              config.Workers,
		TickMS:                                   tickMS, DecisionMS: decisionMS,
		TrapDurationMS: config.TrapDurationMS, DangerHorizonMS: config.DangerHorizonMS,
		BaseSeed: config.Seed, ReuseTensorBuffers: true, AutoReset: true, DiscardEvents: true,
		Rewards: &rewards, TensorHeight: tensorHeight, TensorWidth: tensorWidth,
		Rule: battleenv.OrdinaryRule{}, HalfSpatial: config.SpatialDtype == "f16",
		ServerCadence: config.DecisionCadence == "server", RandomDecisionPhase: config.RandomPhase,
	}
}

func cloneLayouts(layouts [][]uint8) [][]uint8 {
	result := make([][]uint8, len(layouts))
	for index, layout := range layouts {
		result[index] = slices.Clone(layout)
	}
	return result
}

// SelectMaps loads the eligible maps of the configured rule and applies the
// maps / exclude_maps filters.
func (config Config) SelectMaps(clientRoot string) ([]mapdata.CompetitiveMap, error) {
	minimum := config.PlayerCounts[0]
	for _, count := range config.PlayerCounts {
		minimum = min(minimum, count)
	}
	eligible, err := battleenv.LoadRuleMaps(clientRoot, battleenv.OrdinaryRule{}, minimum, tensorHeight, tensorWidth)
	if err != nil {
		return nil, err
	}
	return config.filterMaps(eligible)
}

func (config Config) filterMaps(eligible []mapdata.CompetitiveMap) ([]mapdata.CompetitiveMap, error) {
	var result []mapdata.CompetitiveMap
	for _, entry := range eligible {
		if len(config.Maps) != 0 && !slices.Contains(config.Maps, entry.ID) {
			continue
		}
		if slices.Contains(config.ExcludeMaps, entry.ID) {
			continue
		}
		result = append(result, entry)
	}
	for _, mapID := range config.Maps {
		if !slices.ContainsFunc(result, func(entry mapdata.CompetitiveMap) bool { return entry.ID == mapID }) {
			return nil, fmt.Errorf("worker config: map %d is not an eligible rule-%d map", mapID, config.Rule)
		}
	}
	if len(result) == 0 {
		return nil, fmt.Errorf("worker config: the map selection is empty")
	}
	return result, nil
}
