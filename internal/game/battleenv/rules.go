package battleenv

import (
	"fmt"
	"sort"

	"qqtang/internal/game/battleengine"
	"qqtang/internal/game/mapdata"
)

// RuleModule is the extension point for battle rules. The training
// environment currently ships only the ordinary rule 1 (OrdinaryRule). The
// special rules (bun 3, sculpture 6, wrestle 4, treasure 5, kick-bomb 2,
// machine 7, box 8, tank 13, adventure 12) keep their rule logic in
// cmd/qqt-battle-sidecar today; porting one means implementing this
// interface with that logic (see training/README.md, "Worker").
type RuleModule interface {
	// NativeRule is the client rule number reported in episode info.
	NativeRule() uint8
	// Eligible reports whether a catalog map can host this rule with
	// participants actors.
	Eligible(entry mapdata.CompetitiveMap, participants int) error
	// ConfigureEpisode may adjust the engine construction options before
	// battleengine.ConfigFromCompetitiveMap runs: rule-specific engine
	// switches (Rules.ObjectiveSceneIDs, DeathDropRecycle, ...),
	// AllowSpecialRules and the GAME_BEGIN item lists that the sidecar
	// otherwise records from the original client.
	ConfigureEpisode(entry mapdata.CompetitiveMap, seed uint64, options *battleengine.CompetitiveMapConfigOptions) error
	// NewEpisode returns per-episode rule state, or nil when the engine alone
	// decides the battle (rule 1).
	NewEpisode(engine *battleengine.Engine, entry mapdata.CompetitiveMap) (RuleEpisode, error)
}

// RuleEpisode runs rule logic that is not part of the engine after every
// engine tick, and may decide the outcome itself (score rules).
type RuleEpisode interface {
	// AfterTick sees the tick's engine events and may apply further engine
	// calls (respawns, objective scoring, NPC moves). Returned events are
	// appended to the tick's events for reward accounting.
	AfterTick(engine *battleengine.Engine, events []battleengine.Event) ([]battleengine.Event, error)
	// Outcome overrides the engine outcome when the rule concludes the battle
	// (Ended=false leaves the engine's outcome in force).
	Outcome(engine *battleengine.Engine) battleengine.Outcome
}

// OrdinaryRule is rule 1: ordinary elimination on a no-item-field map.
type OrdinaryRule struct{}

func (OrdinaryRule) NativeRule() uint8 { return 1 }

func (OrdinaryRule) Eligible(entry mapdata.CompetitiveMap, participants int) error {
	if entry.RequiredItemField != 0 || entry.NativeRule != 1 || !entry.UsesOrdinaryElimination() {
		return fmt.Errorf("map %d is outside restricted no-item rule-1 scope", entry.ID)
	}
	if participants > 0 && int(entry.PlayerLimit) < participants {
		return fmt.Errorf("map %d holds %d players, want %d", entry.ID, entry.PlayerLimit, participants)
	}
	if err := battleengine.ValidateNativeRuntimeMap(entry); err != nil {
		return err
	}
	if participants > 0 {
		freeTeams := make([]byte, participants)
		for index := range freeTeams {
			freeTeams[index] = byte(index + 1)
		}
		if err := battleengine.ValidateNativeSpawnTopology(entry, battleengine.NativeSpawnFree, freeTeams); err != nil {
			return err
		}
	}
	return nil
}

func (OrdinaryRule) ConfigureEpisode(mapdata.CompetitiveMap, uint64, *battleengine.CompetitiveMapConfigOptions) error {
	return nil
}

func (OrdinaryRule) NewEpisode(*battleengine.Engine, mapdata.CompetitiveMap) (RuleEpisode, error) {
	return nil, nil
}

// LoadRuleMaps returns the selectable catalog maps that host rule with at
// least minParticipants actors and fit a height x width tensor (zero skips
// the size check), sorted by map ID.
func LoadRuleMaps(clientRoot string, rule RuleModule, minParticipants, height, width int) ([]mapdata.CompetitiveMap, error) {
	if rule == nil {
		rule = OrdinaryRule{}
	}
	catalog, err := mapdata.LoadCatalog(clientRoot)
	if err != nil {
		return nil, err
	}
	var result []mapdata.CompetitiveMap
	for _, mapID := range catalog.CompetitiveIDs() {
		entry, ok := catalog.CompetitiveMap(mapID)
		if !ok || rule.Eligible(entry, minParticipants) != nil {
			continue
		}
		if (height > 0 && int(entry.Battlefield.Height) > height) || (width > 0 && int(entry.Battlefield.Width) > width) {
			continue
		}
		result = append(result, entry)
	}
	if len(result) == 0 {
		return nil, fmt.Errorf("no eligible rule-%d maps for %d participants", rule.NativeRule(), minParticipants)
	}
	sort.Slice(result, func(i, j int) bool { return result[i].ID < result[j].ID })
	return result, nil
}
