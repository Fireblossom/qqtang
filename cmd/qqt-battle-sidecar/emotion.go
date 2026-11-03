package main

import (
	"sort"

	"qqtang/internal/protocol/game"
)

// The actor's +0x420 component: Client.exe 0x005D1FFF/0x0060D534.
const emotionDurationMS, emotionCooldownMS = 3000, 2000

type emotionState struct {
	notice game.UseEmotionEvent
	since  uint32
}

type emotionView struct {
	Player uint16 `json:"player"`
	ID     uint32 `json:"id"`
	Since  uint32 `json:"since"`
}

func (match *onlineMatch) applyEmotion(notice game.UseEmotionEvent) {
	id := uint16(notice.PlayerID)
	if _, ok := match.uins[id]; !ok {
		return
	}
	if match.emotions == nil {
		match.emotions = make(map[uint16]emotionState)
	}
	// TCP and UDP carry the same fact. A late mirror must not restart it or
	// replace a newer emotion. The native transport deduplicates before dispatch.
	if previous, ok := match.emotions[id]; ok && notice.Time <= previous.notice.Time {
		return
	}
	match.emotions[id] = emotionState{notice: notice, since: match.engine.ElapsedMS()}
	match.logf("emotion player %d id %d time %d", id, notice.EmotionID, notice.Time)
}

func (match *onlineMatch) requestEmotion(id uint32) {
	if id < 1 || id > 6 || match.engine.Terminal().Ended {
		return
	}
	now := match.engine.ElapsedMS()
	previous := match.emotions[match.local]
	if now < previous.notice.Time || now-previous.notice.Time < emotionCooldownMS {
		return
	}
	notice := game.UseEmotionEvent{PlayerID: uint32(match.local), Time: now, EmotionID: id}
	body, err := notice.MarshalNetworkBinary()
	if err != nil {
		return
	}
	match.queue(game.NotifyUseEmotion, now, now, body)
	match.mirror(game.NotifyUseEmotion, body)
	match.applyEmotion(notice)
}

func (match *onlineMatch) emotionView() []emotionView {
	var out []emotionView
	now := match.engine.ElapsedMS()
	for id, state := range match.emotions {
		if _, present := actorByID(match.engine.Actors(), id); present && now-state.since <= emotionDurationMS {
			out = append(out, emotionView{Player: id, ID: state.notice.EmotionID, Since: state.since})
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Player < out[j].Player })
	return out
}
