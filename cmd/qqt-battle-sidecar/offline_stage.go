package main

// Offline adventure (rule 12): the server's half of a stage route, after
// probe newAdventureGameData, handleAdventureDeath, handleAdventureNextMap
// and adventure_victory.go. The AI seats are teammates; the NPCs are the
// arbitrator's own (adventure.go). A stage is cleared when every expected
// NPC has died; the first player through the door then moves the survivors
// to the next stage of the Continue.ini route (a new GAME_BEGIN, every
// client rebuilt) or, on the last stage, wins. When every player is dead
// the route is lost.

import (
	"fmt"
	"os"
	"path/filepath"

	"qqtang/internal/clientdata/sceneelement"
	"qqtang/internal/game/mapdata"
	"qqtang/internal/game/match"
	"qqtang/internal/protocol/game"
)

const (
	offlineAdventureTeam       = byte(1) // probe adventureCooperativeTeamID
	offlineAdventureLootMS     = 10_000  // probe adventureFinalLootGrace
	offlineAdventurePVEBossMS  = 50      // CREATE_PVENPC_BOSS after GAME_BEGIN
	offlineAdventureRescueGain = 10      // configs adventure_rewards.player_rescue_score
)

// Set by build-sidecar.sh when it knows the repository.
var defaultAdventureRules = ""

type offlineStage struct {
	referee  *offlineReferee
	catalog  *mapdata.Catalog
	rules    *mapdata.AdventureRules
	current  mapdata.AdventureMap
	rule     mapdata.AdventureStageRule
	hasRule  bool
	battle   *match.AdventureBattle
	players  map[uint16]player
	pveBoss  []byte // CREATE_PVENPC_BOSS body for this stage
	pveSent  bool
	victory  bool // final stage cleared: GAME_OVER after the loot grace
	victorAt uint32
	gameID   uint32
}

func newOfflineStage(clientRoot string, catalog *mapdata.Catalog, stageMap mapdata.AdventureMap) (*offlineStage, error) {
	stage := &offlineStage{catalog: catalog, current: stageMap, players: map[uint16]player{}, gameID: offlineGameID}
	if path := offlineAdventureRulesPath(); path != "" {
		rules, err := mapdata.LoadAdventureRules(path)
		if err != nil {
			return nil, err
		}
		stage.rules = rules
	} else {
		fmt.Fprintln(os.Stderr, "[offline] configs/adventure-rules.json not found: stages have no wall items or NPC drops")
	}
	return stage, nil
}

func offlineAdventureRulesPath() string {
	candidates := []string{os.Getenv("QQT_ADVENTURE_RULES"), defaultAdventureRules}
	if executable, err := os.Executable(); err == nil {
		candidates = append(candidates, filepath.Join(filepath.Dir(executable), "..", "..", "..", "..", "configs", "adventure-rules.json"))
	}
	if working, err := os.Getwd(); err == nil {
		candidates = append(candidates, filepath.Join(working, "configs", "adventure-rules.json"), filepath.Join(working, "..", "..", "configs", "adventure-rules.json"))
	}
	for _, path := range candidates {
		if path == "" {
			continue
		}
		if info, err := os.Stat(path); err == nil && !info.IsDir() {
			return path
		}
	}
	return ""
}

// begin is probe newAdventureGameData for the current stage: every player
// on the cooperative team with its courage level, the stage's wall items.
func (stage *offlineStage) begin(options offlineInit, arbitrator uint16, players []player) (game.GameBeginData, error) {
	stage.rule, stage.hasRule = stage.rules.Stage(stage.current.ID)
	var wall []game.GameItemType
	if stage.hasRule {
		selected, err := stage.rule.SelectWallItemInventory(options.ItemSeed)
		if err != nil {
			return game.GameBeginData{}, err
		}
		for _, item := range selected {
			if sceneelement.IsClientID(item.ItemID) && item.Quantity > 0 && item.Quantity <= 0x7fff {
				wall = append(wall, game.GameItemType{ItemID: item.ItemID, Quantity: int16(item.Quantity)})
			}
		}
		boss := game.CreatePVENPCBoss{}
		for _, group := range stage.rule.NPCDropGroups {
			info := game.PVEBossInfo{BossID: group.BossID, BossCount: group.BossCount}
			for _, item := range stage.rule.SelectNPCDropInventory(group, options.ItemSeed) {
				info.NormalItems = append(info.NormalItems, game.BossItemInfo{ItemID: item.ItemID, ItemCount: item.Quantity, DropTime: item.DropTime})
			}
			boss.Bosses = append(boss.Bosses, info)
		}
		if len(boss.Bosses) > 0 {
			body, err := boss.MarshalNetworkBinary()
			if err != nil {
				return game.GameBeginData{}, err
			}
			stage.pveBoss = body
		}
	}
	stage.pveSent = false
	infos := make([]game.PlayerGameInfo, 0, len(players))
	for _, entry := range players {
		stage.players[entry.ID] = entry
		infos = append(infos, game.PlayerGameInfo{PlayerID: entry.ID, RoleID: byte(entry.Role), TeamID: offlineAdventureTeam, ExtPoint: entry.ExtPoint})
	}
	return game.NewAdventureGameBeginData(game.GameBeginOptions{
		GameID: stage.gameID, MapID: stage.current.ID, SpawnSeed: options.SpawnSeed, ItemSeed: options.ItemSeed,
		ArbitratorPlayerID: arbitrator, ContinueID: game.NoRemoteContinueFileID, MapHash: stage.current.MapHash,
		GameTimeMS: game.DefaultAdventureGameTimeMS, Players: infos, NewItems: wall,
	})
}

func (stage *offlineStage) attach(referee *offlineReferee) error {
	stage.referee = referee
	return stage.newBattle(referee.hub.begin)
}

func (stage *offlineStage) newBattle(begin game.GameBeginData) error {
	participants := make([]match.AdventureParticipant, 0, len(begin.Players))
	for _, info := range begin.Players {
		participants = append(participants, match.AdventureParticipant{PlayerID: info.PlayerID, RoleID: info.RoleID, TeamID: info.TeamID, ExtPoint: info.ExtPoint})
	}
	if stage.battle != nil {
		return stage.battle.AdvanceStageWithRule(begin.MapID, begin.ItemSeed, stage.rule.ExpectedNPCDeaths, begin.ArbitratorPlayerID, participants)
	}
	battle, err := match.NewAdventureBattleWithStage(begin.GameID, begin.MapID, begin.ItemSeed, stage.rule.ExpectedNPCDeaths, begin.ArbitratorPlayerID, participants)
	stage.battle = battle
	return err
}

func (stage *offlineStage) observe(sender uint16, event game.GameEvent, now uint32) {
	battle := stage.battle
	switch event.Schema {
	case game.NotifyPlayerDieEvent:
		death, err := game.ParsePlayerDeathEvent(event)
		if err != nil {
			return
		}
		if !battle.HasParticipant(death.PlayerID) {
			stage.npcDeath(death, now)
			return
		}
		resolution, err := battle.RecordDeath(death.PlayerID)
		if err != nil {
			return
		}
		stage.referee.stat(death.PlayerID).Deaths++
		if resolution.FinalVictoryPending && resolution.AllPlayersDead {
			stage.finishVictory(death.ClientTime, now, 50)
			return
		}
		if resolution.NewlyConcluded {
			results := make([]game.GameResultData, 0, len(resolution.Participants))
			for _, participant := range resolution.Participants {
				results = append(results, stage.result(participant.PlayerID, game.GameResultLoss))
			}
			over := game.GameOverData{Time: death.ClientTime, Results: results, GameMode: game.SettlementGameModeAdventure}
			stage.referee.result, stage.referee.dueAt = &over, now+offlineConclusionDelayMS
			stage.referee.hub.logf("every player is dead at %d ms", now)
		}
	case game.NotifyPlayerSaved:
		if saved, err := game.ParsePlayerInteractionEvent(event); err == nil {
			_, _ = battle.RecordPlayerSaved(saved.PlayerID, saved.DestinationPlayerID, offlineAdventureRescueGain)
		}
	case game.NotifyPlayerRelive:
		if relive, err := game.ParsePlayerReliveEvent(event); err == nil {
			_, _ = battle.RecordPlayerRelive(relive.PlayerID)
		}
	}
}

// npcDeath is probe handleAdventureNPCDeath.
func (stage *offlineStage) npcDeath(death game.PlayerDeathEvent, now uint32) {
	courage := uint32(0)
	if group, ok := stage.rule.NPCDropGroup(death.PlayerID); ok {
		courage = group.Courage()
	}
	progress, err := stage.battle.RecordNPCDeath(courage)
	if err != nil || !progress.NewlyCompleted {
		return
	}
	if _, hasNext := stage.catalog.Next(stage.current.ID); hasNext {
		stage.referee.hub.logf("stage %d cleared (%d NPCs); waiting for the door", stage.current.ID, progress.DeathOrdinal)
		return
	}
	if pending, err := stage.battle.BeginFinalStageVictory(); err == nil && pending.NewlyConcluded {
		stage.victory, stage.victorAt = true, now
		stage.referee.hub.logf("final stage %d cleared; loot grace", stage.current.ID)
	}
}

func (stage *offlineStage) finishVictory(clientTime, now, delay uint32) {
	resolution, err := stage.battle.FinalizePendingVictory()
	if err != nil || !resolution.NewlyConcluded {
		return
	}
	results := make([]game.GameResultData, 0, len(resolution.Players))
	for _, player := range resolution.Players {
		outcome := game.GameResultLoss
		if player.Won {
			outcome = game.GameResultWin
		}
		results = append(results, stage.result(player.Participant.PlayerID, outcome))
	}
	over := game.GameOverData{Time: clientTime, Results: results, GameMode: game.SettlementGameModeAdventure}
	stage.referee.result, stage.referee.dueAt = &over, now+delay
}

func (stage *offlineStage) result(playerID uint16, outcome game.GameResultCode) game.GameResultData {
	statistics, _ := stage.battle.PlayerStatistics(playerID)
	return game.GameResultData{PlayerID: playerID, Result: outcome, Fields: (game.AdventureResultStatistics{
		KillCount: statistics.KillCount, KillScore: statistics.KillScore,
		RescueCount: statistics.RescueCount, RescueScore: statistics.RescueScore,
		RewardCount: statistics.RewardCount, RewardScore: statistics.RewardScore,
	}).GameResultFields()}
}

// tick is the server's stage clock: CREATE_PVENPC_BOSS, the final loot grace.
func (stage *offlineStage) tick(now uint32) {
	hub := stage.referee.hub
	if !stage.pveSent && stage.pveBoss != nil && now >= offlineAdventurePVEBossMS {
		stage.pveSent = true
		hub.notifyAll(game.CreatePVENPCBossEvent, stage.pveBoss)
	}
	if stage.victory && now >= stage.victorAt+offlineAdventureLootMS {
		stage.victory = false
		stage.finishVictory(now, now, 0)
	}
}

// nextMap is probe handleAdventureNextMap: the survivors go on to the next
// stage, or win on the last one.
func (stage *offlineStage) nextMap(sender uint16, event game.GameEvent) {
	hub := stage.referee.hub
	request, err := game.ParseGameNextMapRequest(event)
	if err != nil || request.PlayerID != sender || hub.nextStage != nil {
		return
	}
	transition, err := stage.battle.PrepareNextStage(request.PlayerID)
	if err != nil {
		hub.logf("next map: %v", err)
		return
	}
	next, hasNext := stage.catalog.Next(stage.current.ID)
	if !hasNext {
		participants := append([]match.AdventureParticipant(nil), transition.Survivors...)
		for _, settled := range transition.SettledOut {
			participants = append(participants, settled.Participant)
		}
		results := make([]game.GameResultData, 0, len(participants))
		for _, participant := range participants {
			results = append(results, stage.result(participant.PlayerID, game.GameResultWin))
		}
		over := game.GameOverData{Time: request.ClientTime, Results: results, GameMode: game.SettlementGameModeAdventure}
		stage.referee.result, stage.referee.dueAt = &over, hub.link.clock
		hub.logf("route won through the last door of stage %d", stage.current.ID)
		return
	}
	survivors := make([]player, 0, len(transition.Survivors))
	for _, participant := range transition.Survivors {
		survivors = append(survivors, stage.players[participant.PlayerID])
	}
	hub.logf("stage %d -> %d with %d players", stage.current.ID, next.ID, len(survivors))
	stage.current = next
	stage.gameID++
	hub.nextStage = &offlinePendingStage{players: survivors, arbitrator: transition.ArbitratorPlayerID}
}
