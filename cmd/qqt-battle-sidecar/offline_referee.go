package main

// The offline referee is the server's half of a competitive match: it reads
// the reliable copies (REQUEST_PLAY) every client sends, keeps the server's
// own ledger (internal/game/match.CompetitiveBattle, fed as the probe's
// game_event_dispatch.go and competitive_death_dispatch.go feed it) and
// produces the GAME_OVER the server would send: on a decisive report after
// the native 750 ms grace (10 s after a Boss victory), or at the round's end.

import (
	"fmt"

	"qqtang/internal/clientdata/sceneelement"
	"qqtang/internal/game/mapdata"
	"qqtang/internal/game/match"
	"qqtang/internal/protocol/game"
)

const (
	offlineConclusionDelayMS  = 750    // probe competitiveNativeConclusionDelay
	offlineBossVictoryDelayMS = 10_000 // probe competitiveBossVictoryConclusionDelay
	// probe settlement_projection.go: points shown on the result screen.
	offlineWinPoints, offlineDrawPoints, offlineLossPoints = 50, 20, 10
	offlineActionPoints                                    = 10
)

type offlineReferee struct {
	hub     *offlineHub
	battle  *match.CompetitiveBattle
	stage   *offlineStage // rule 12 instead of battle
	roundMS uint32
	result  *game.GameOverData
	dueAt   uint32
	// Per-player measurements for headless runs.
	stats map[uint16]*offlinePlayerStats
}

type offlinePlayerStats struct {
	Kills     int `json:"kills"`
	Deaths    int `json:"deaths"`
	SelfTraps int `json:"self_traps"` // trapped (or harmed) by an own bubble
	TeamTraps int `json:"team_traps"` // by a teammate's bubble
	Objective int `json:"objective"`  // buns home, gems carried, fragments placed
}

type offlineScore struct {
	Players map[uint16]*offlinePlayerStats `json:"players"`
}

func newOfflineReferee(hub *offlineHub, entry mapdata.CompetitiveMap, competitive bool, stage *offlineStage) (*offlineReferee, error) {
	referee := &offlineReferee{hub: hub, stats: map[uint16]*offlinePlayerStats{}, roundMS: hub.view.RoundMS}
	for _, info := range hub.begin.Players {
		referee.stats[info.PlayerID] = &offlinePlayerStats{}
	}
	if !competitive {
		referee.stage = stage
		return referee, stage.attach(referee)
	}
	spec, ok := mapdata.LookupCompetitiveRule(entry.Rule)
	if !ok {
		return nil, fmt.Errorf("map %d has no rule contract for %s", entry.ID, entry.Rule)
	}
	conclusion := match.CompetitiveConclusionClientRule
	if entry.UsesServerElimination() {
		conclusion = match.CompetitiveConclusionOrdinaryElimination
	}
	lifecycle, err := offlineLifecycle(spec.PlayerLifecycle)
	if err != nil {
		return nil, err
	}
	config := match.CompetitiveRuleConfig{
		ConclusionPolicy: conclusion, PlayerLifecycle: lifecycle, Objective: offlineObjective(spec.Objective),
		NativeHitLimit: spec.NativePlayerHitLimit, InitialSceneItems: map[uint32]uint32{},
	}
	for _, item := range hub.begin.NewItems {
		config.InitialSceneItems[item.ItemID] += uint32(item.Quantity)
	}
	if entry.Rule == mapdata.CompetitiveRuleKickBomb {
		config.RuleSceneItems = map[uint32]uint32{uint32(sceneelement.SugarCoin50): 50}
	}
	if config.Objective == match.CompetitiveObjectiveSculpture {
		for _, cell := range entry.ObjectiveCells {
			config.SculptureCells = append(config.SculptureCells, match.CompetitiveSculptureCell{Row: cell.Row, Col: cell.Col})
		}
	}
	if hub.boss != nil {
		candidate := entry.BossCandidates[0]
		config.Objective = match.CompetitiveObjectiveBoss
		config.ConclusionPolicy = match.CompetitiveConclusionClientRule
		if config.PlayerLifecycle, err = offlineLifecycle(candidate.Overlay.PlayerLifecycle); err != nil {
			return nil, err
		}
		config.BossID = candidate.ID
		config.BossSkills = map[uint16][]uint16{}
		for _, boss := range hub.boss.Bosses {
			config.BossEntityIDs = append(config.BossEntityIDs, boss.BossID)
			skills := make([]uint16, 0, len(boss.Skills))
			for _, skill := range boss.Skills {
				skills = append(skills, uint16(skill))
			}
			config.BossSkills[boss.BossID] = skills
		}
		if candidate.Overlay.TeamProjection == mapdata.CompetitiveTeamsUnified {
			config.TeamTopology = match.CompetitiveTeamsCooperative
		}
	}
	participants := make([]match.CompetitiveParticipant, 0, len(hub.begin.Players))
	for _, info := range hub.begin.Players {
		participants = append(participants, match.CompetitiveParticipant{PlayerID: info.PlayerID, RoleID: info.RoleID, TeamID: info.TeamID})
	}
	if referee.battle, err = match.NewCompetitiveBattleWithRuleConfig(hub.begin.GameID, hub.begin.MapID, hub.begin.ArbitratorPlayerID, participants, config); err != nil {
		return nil, err
	}
	return referee, nil
}

// offlineLifecycle and offlineObjective are probe competitivePlayerLifecycle
// and competitiveObjective.
func offlineLifecycle(lifecycle mapdata.CompetitivePlayerLifecycle) (match.CompetitivePlayerLifecycle, error) {
	switch lifecycle {
	case mapdata.CompetitiveLifecyclePermanentElimination:
		return match.CompetitivePlayerPermanentElimination, nil
	case mapdata.CompetitiveLifecycleTimedRespawn:
		return match.CompetitivePlayerTimedRespawn, nil
	case mapdata.CompetitiveLifecycleNativeDurability:
		return match.CompetitivePlayerNativeDurability, nil
	}
	return 0, fmt.Errorf("unknown competitive player lifecycle %q", lifecycle)
}

func offlineObjective(objective mapdata.CompetitiveObjective) match.CompetitiveObjectiveKind {
	switch objective {
	case mapdata.CompetitiveObjectiveBun:
		return match.CompetitiveObjectiveBun
	case mapdata.CompetitiveObjectiveWrestle:
		return match.CompetitiveObjectiveWrestle
	case mapdata.CompetitiveObjectiveSculpture:
		return match.CompetitiveObjectiveSculpture
	case mapdata.CompetitiveObjectiveTreasure:
		return match.CompetitiveObjectiveTreasure
	case mapdata.CompetitiveObjectiveTankBase:
		return match.CompetitiveObjectiveTankBase
	}
	return match.CompetitiveObjectiveNone
}

func (referee *offlineReferee) registerBoss(data game.CreateNPCBoss) {
	// CREATE_NPC_BOSS's entities are the BossEntityIDs the ledger was built
	// with; nothing more to record.
	_ = data
}

func (referee *offlineReferee) stat(playerID uint16) *offlinePlayerStats {
	if stats := referee.stats[playerID]; stats != nil {
		return stats
	}
	return &offlinePlayerStats{}
}

// observe is the server's dispatch of one reliable copy from sender.
func (referee *offlineReferee) observe(sender uint16, event game.GameEvent, now uint32) {
	if referee.result != nil {
		return
	}
	if referee.stage != nil {
		referee.stage.observe(sender, event, now)
		return
	}
	battle := referee.battle
	arbitrator := battle.IsArbitrator(sender)
	switch event.Schema {
	case game.PlayerBeExploded:
		hit, err := game.ParsePlayerExplodedEvent(event)
		if err == nil && battle.HasParticipant(hit.PlayerID) && !hit.IsAvatar {
			_ = battle.RecordTrapped(hit.PlayerID)
		}
	case game.RequestKillPlayer:
		interaction, err := game.ParsePlayerInteractionEvent(event)
		if err != nil || !battle.HasParticipant(interaction.DestinationPlayerID) || battle.HasNativeNPCEntity(interaction.PlayerID) || battle.HasNativeNPCEntity(interaction.DestinationPlayerID) {
			return
		}
		var resolution match.CompetitiveResolution
		if battle.HasBossEntity(interaction.PlayerID) {
			resolution, err = battle.RecordBossKill(interaction.PlayerID, interaction.DestinationPlayerID)
		} else {
			resolution, err = battle.RecordKill(interaction.PlayerID, interaction.DestinationPlayerID)
		}
		if err == nil {
			referee.conclude(resolution, interaction.ClientTime, now, offlineConclusionDelayMS)
		}
	case game.RequestSavePlayer, game.NotifyPlayerSaved:
		if interaction, err := game.ParsePlayerInteractionEvent(event); err == nil {
			_ = battle.RecordRescue(interaction.PlayerID, interaction.DestinationPlayerID)
		}
	case game.NotifyPlayerKilled:
		killed, err := game.ParsePlayerKilledEvent(event)
		if err != nil || !arbitrator {
			return
		}
		battle.RecordNativeRelay(event.Schema, event.Body)
		if battle.HasNativeNPCEntity(killed.DestinationPlayerID) {
			_, _ = battle.RecordNativeNPCDeath(killed.DestinationPlayerID)
			return
		}
		if battle.HasNativeNPCEntity(killed.PlayerID) || !battle.HasParticipant(killed.DestinationPlayerID) {
			return
		}
		itemIDs := make([]uint32, 0, len(killed.Items))
		for _, item := range killed.Items {
			itemIDs = append(itemIDs, item.ItemID)
		}
		_ = battle.RecordTreasureScatter(killed.DestinationPlayerID, itemIDs)
		var resolution match.CompetitiveResolution
		if battle.HasBossEntity(killed.PlayerID) {
			resolution, err = battle.RecordBossKill(killed.PlayerID, killed.DestinationPlayerID)
		} else {
			resolution, err = battle.RecordKill(killed.PlayerID, killed.DestinationPlayerID)
			if err == nil && killed.PlayerID != killed.DestinationPlayerID {
				referee.stat(killed.PlayerID).Kills++
			}
		}
		referee.stat(killed.DestinationPlayerID).Deaths++
		if err == nil {
			referee.conclude(resolution, killed.ClientTime, now, offlineConclusionDelayMS)
		}
	case game.NotifyPlayerDieEvent:
		referee.death(sender, event, now)
	case game.NotifyPlayerGetItem:
		item, err := game.ParsePlayerItemEvent(event)
		if err != nil || !arbitrator || !battle.RecordNativeRelay(event.Schema, event.Body) {
			return
		}
		if match.IsTreasureGem(item.ItemID) && battle.HasParticipant(item.PlayerID) {
			resolution, recorded, err := battle.RecordTreasurePickup(item.PlayerID, item.ClientTime, item.ItemID, item.PosX, item.PosY)
			if err == nil && recorded {
				referee.stat(item.PlayerID).Objective++
			}
			if err == nil {
				referee.conclude(resolution, item.ClientTime, now, offlineConclusionDelayMS)
			}
		}
	case game.NotifyPlayerGetBun, game.NotifyPlayerPutBun:
		action, err := game.ParseBunActionEvent(event)
		if err != nil || !arbitrator {
			return
		}
		var resolution match.CompetitiveResolution
		var fresh bool
		get := event.Schema == game.NotifyPlayerGetBun
		switch {
		case battle.UsesBunObjective():
			resolution, fresh, err = battle.RecordBunAction(get, action.PlayerID, action.ClientTime, action.PosX, action.PosY, action.BunID, action.BunTeamID)
		case battle.UsesSculptureObjective():
			resolution, fresh, err = battle.RecordSculptureAction(get, action.PlayerID, action.ClientTime, action.PosX, action.PosY, action.BunID, action.BunTeamID)
		default:
			return
		}
		if err != nil {
			referee.hub.logf("bun action of %d: %v", action.PlayerID, err)
			return
		}
		if fresh && !get {
			referee.stat(action.PlayerID).Objective++
		}
		referee.conclude(resolution, action.ClientTime, now, offlineConclusionDelayMS)
	case game.NotifyBombExplode:
		if !battle.UsesSculptureObjective() || !arbitrator {
			return
		}
		explosion, err := game.ParseBombExplodeEvent(event)
		if err != nil {
			return
		}
		bombs := make([]match.CompetitiveSculptureBomb, 0, len(explosion.Bombs))
		for _, bomb := range explosion.Bombs {
			bombs = append(bombs, match.CompetitiveSculptureBomb{PlayerID: bomb.PlayerID, ClientTime: bomb.ClientTime, BombID: bomb.BombID, Row: bomb.Row, Col: bomb.Column})
		}
		_, _ = battle.RecordSculptureExplosions(bombs)
	case game.RequestTankBaseHP:
		change, err := game.ParseTankBaseHPChangeEvent(event)
		if err != nil || change.TeamID > 0xff {
			return
		}
		result, err := battle.RecordTankBaseHPChange(change.PlayerID, byte(change.TeamID), change.ChangeHP)
		if err == nil {
			referee.conclude(result.Resolution, change.Time, now, offlineConclusionDelayMS)
		}
	case game.PlayerBeHarmed:
		harmed, err := game.ParseEntityHarmedEvent(event)
		if err == nil && battle.HasParticipant(harmed.ObjectID) {
			_, _, _ = battle.RecordNativeHarm(harmed.ObjectID, harmed.Time, harmed.PosX, harmed.PosY, harmed.IsAvatar, harmed.LossHP)
		}
	case game.NotifyPlayerRelive:
		if relive, err := game.ParsePlayerReliveEvent(event); err == nil && arbitrator {
			_ = battle.RecordRespawn(relive.PlayerID)
		}
	case game.CreateNPCBossEvent:
		// A rule-8 NPC registered by the arbitrator.
		if data, err := game.ParseCreateNPCBossNetwork(event.Body); err == nil && arbitrator {
			ids := make([]uint16, 0, len(data.Bosses))
			for _, boss := range data.Bosses {
				ids = append(ids, boss.BossID)
			}
			if err := battle.RegisterNativeNPCEntities(ids); err != nil {
				referee.hub.logf("NPC registration: %v", err)
			}
		}
	}
}

// death is probe handleCompetitiveDeath.
func (referee *offlineReferee) death(sender uint16, event game.GameEvent, now uint32) {
	battle := referee.battle
	death, err := game.ParsePlayerDeathEvent(event)
	if err != nil || death.PlayerID != sender && !battle.IsArbitrator(sender) {
		return
	}
	battle.RecordNativeRelay(event.Schema, event.Body)
	var resolution match.CompetitiveResolution
	delay := uint32(offlineConclusionDelayMS)
	switch {
	case battle.HasParticipant(death.PlayerID):
		if battle.UsesNativeDurability() {
			var authoritative bool
			resolution, authoritative, err = battle.RecordNativeDurabilityDeath(death.PlayerID, death.ClientTime, death.PosX, death.PosY)
			if err != nil || !authoritative {
				return
			}
		} else {
			itemIDs := make([]uint32, 0, len(death.Items))
			for _, item := range death.Items {
				itemIDs = append(itemIDs, item.ItemID)
			}
			_ = battle.RecordTreasureScatter(death.PlayerID, itemIDs)
			if grace, allDead, graceErr := battle.RecordBossVictoryGraceDeath(death.PlayerID); graceErr == nil && grace {
				if allDead && referee.result != nil && referee.dueAt > now {
					referee.dueAt = now // the server ends the grace once everyone is dead
				}
				return
			}
			if resolution, err = battle.RecordDeath(death.PlayerID); err != nil {
				return
			}
		}
		referee.stat(death.PlayerID).Deaths++
	case battle.HasBossEntity(death.PlayerID):
		if resolution, err = battle.RecordBossDeath(death.PlayerID); err != nil {
			return
		}
		delay = offlineBossVictoryDelayMS
	case battle.HasNativeNPCEntity(death.PlayerID):
		_, _ = battle.RecordNativeNPCDeath(death.PlayerID)
		return
	default:
		return
	}
	referee.conclude(resolution, death.ClientTime, now, delay)
}

func (referee *offlineReferee) conclude(resolution match.CompetitiveResolution, clientTime, now, delay uint32) {
	if !resolution.NewlyConcluded || referee.result != nil {
		return
	}
	over := offlineGameOver(clientTime, resolution)
	referee.result, referee.dueAt = &over, now+delay
	referee.hub.logf("concluded at %d ms (winner team %d); GAME_OVER in %d ms", now, resolution.WinnerTeamID, delay)
}

// due returns the GAME_OVER once its time has come, concluding the round
// when its time is up.
func (referee *offlineReferee) due(now uint32) (game.GameOverData, bool) {
	if referee.stage != nil {
		if referee.result == nil {
			referee.stage.tick(now)
		}
	} else if referee.result == nil && referee.roundMS > 0 && now >= referee.roundMS {
		resolution, err := referee.battle.ConcludeTimeout()
		if err != nil {
			referee.hub.logf("timeout: %v", err)
		} else if resolution.NewlyConcluded {
			over := offlineGameOver(referee.roundMS, resolution)
			referee.result, referee.dueAt = &over, now
			referee.hub.logf("time is up at %d ms (winner team %d)", now, resolution.WinnerTeamID)
		}
	}
	if referee.result != nil && referee.roundMS > 0 && now >= referee.roundMS && referee.dueAt > now {
		referee.dueAt = now // a pending boss victory ends with the round time
	}
	if referee.result == nil || now < referee.dueAt {
		return game.GameOverData{}, false
	}
	return *referee.result, true
}

// offlineGameOver is probe competitiveGameOverData with its statistics and
// outcome points (settlement_projection.go).
func offlineGameOver(clientTime uint32, resolution match.CompetitiveResolution) game.GameOverData {
	data := game.GameOverData{Time: clientTime, GameMode: game.SettlementGameModeCompetitive}
	for _, player := range resolution.Results {
		outcome, base := game.GameResultLoss, uint32(offlineLossPoints)
		if player.Draw {
			outcome, base = game.GameResultDraw, offlineDrawPoints
		} else if player.Won {
			outcome, base = game.GameResultWin, offlineWinPoints
		}
		kills, rescues := player.KillCount*offlineActionPoints, player.RescueCount*offlineActionPoints
		data.Results = append(data.Results, game.GameResultData{
			PlayerID: player.Participant.PlayerID, Result: outcome, Point: kills + rescues + base,
			Fields: []game.GameResultField{
				{Count: player.KillCount, Score: kills}, {Count: player.RescueCount, Score: rescues}, {Score: base},
			},
		})
	}
	return data
}

func (referee *offlineReferee) nextMap(sender uint16, event game.GameEvent) {
	if referee.stage != nil {
		referee.stage.nextMap(sender, event)
	}
}

func (referee *offlineReferee) score() *offlineScore {
	return &offlineScore{Players: referee.stats}
}
