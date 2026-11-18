package main

import (
	"encoding/json"

	"qqtang/internal/game/battleenv"
)

// episodeInfo is one entry of the response's {"episodes": [...]} JSON.
type episodeInfo struct {
	Env        int    `json:"env"`
	Map        uint32 `json:"map"`
	Rule       uint8  `json:"rule"`
	Curriculum string `json:"curriculum"`
	// Length is the number of decisions (STEPs) of the episode; Ticks the
	// engine ticks (20 ms) and EndedAtMS the engine outcome time.
	Length    uint32 `json:"length"`
	Ticks     uint32 `json:"ticks"`
	EndedAtMS uint32 `json:"ended_at_ms"`
	// Outcome is "win" (WinnerTeam won) or "draw".
	Outcome    string      `json:"outcome"`
	WinnerTeam uint8       `json:"winner_team"`
	TimedOut   bool        `json:"timed_out"`
	Teams      []teamInfo  `json:"teams"`
	Agents     []agentInfo `json:"agents"`
}

type teamInfo struct {
	Team   uint8  `json:"team"`
	Result string `json:"result"` // "win", "loss" or "draw"
	Size   int    `json:"size"`
	Alive  int    `json:"alive"`
}

type agentInfo struct {
	Slot     int     `json:"slot"`
	Team     uint8   `json:"team"`
	Return   float32 `json:"return"`
	Survived bool    `json:"survived"`
	battleenv.AgentMetrics
}

func newEpisodeInfo(completed battleenv.CompletedEpisode) episodeInfo {
	metrics := completed.Metrics
	info := episodeInfo{
		Env: completed.EnvIndex, Map: metrics.MapID, Rule: completed.Rule,
		Curriculum: battleenv.CurriculumName(battleenv.EpisodeCurriculumID(&metrics)),
		Length:     metrics.Decisions, Ticks: metrics.Ticks, EndedAtMS: completed.Outcome.EndedAtMS,
		Outcome: "draw", WinnerTeam: completed.Outcome.WinnerTeamID, TimedOut: completed.Outcome.TimedOut,
	}
	if !completed.Outcome.Draw && completed.Outcome.WinnerTeamID != 0 {
		info.Outcome = "win"
	}
	for slot, teamID := range completed.TeamIDs {
		index := -1
		for candidate := range info.Teams {
			if info.Teams[candidate].Team == teamID {
				index = candidate
				break
			}
		}
		if index < 0 {
			result := "draw"
			if info.Outcome == "win" {
				result = "loss"
				if teamID == info.WinnerTeam {
					result = "win"
				}
			}
			info.Teams = append(info.Teams, teamInfo{Team: teamID, Result: result})
			index = len(info.Teams) - 1
		}
		info.Teams[index].Size++
		survived := slot < len(completed.Survived) && completed.Survived[slot]
		if survived {
			info.Teams[index].Alive++
		}
		agent := agentInfo{Slot: slot, Team: teamID, Survived: survived}
		if slot < len(completed.Returns) {
			agent.Return = completed.Returns[slot]
		}
		if slot < len(metrics.Agents) {
			agent.AgentMetrics = metrics.Agents[slot]
		}
		info.Agents = append(info.Agents, agent)
	}
	return info
}

// appendEpisodeInfo appends the UTF-8 JSON document for the finished
// episodes to buffer. With the server cadence, "ticks" lists the engine
// ticks (20 ms) each environment advanced during the step.
func appendEpisodeInfo(buffer []byte, completed []battleenv.CompletedEpisode, ticks []uint8) ([]byte, error) {
	if len(completed) == 0 && ticks == nil {
		return append(buffer, `{"episodes":[]}`...), nil
	}
	episodes := make([]episodeInfo, len(completed))
	for index, episode := range completed {
		episodes[index] = newEpisodeInfo(episode)
	}
	var tickList []int
	if ticks != nil {
		tickList = make([]int, len(ticks))
		for index, count := range ticks {
			tickList[index] = int(count)
		}
	}
	data, err := json.Marshal(struct {
		Episodes []episodeInfo `json:"episodes"`
		Ticks    []int         `json:"ticks,omitempty"`
	}{episodes, tickList})
	if err != nil {
		return buffer, err
	}
	return append(buffer, data...), nil
}
