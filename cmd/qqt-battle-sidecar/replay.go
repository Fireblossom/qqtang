package main

import (
	"encoding/binary"
	"encoding/hex"
	"fmt"

	"qqtang/internal/game/battleengine"
	"qqtang/internal/game/qbv"
	"qqtang/internal/protocol/game"
)

// replayFeed plays a native .qbv recording into a spectator match. The file
// holds GAME_BEGIN, GAME_OVER, one PLAYER_INFO_IN_ROOM_OLD per player and every
// battle message the recording client sent or received, each at its scene
// time (Client.exe 0x005A36CE loads it; the player object steps it). The match
// has no local player and no socket: it is a non-arbitrator client whose
// remote traffic all comes from the file, so arbitrator outcomes apply
// exactly as they did live.
type replayFeed struct {
	events []qbv.Event
	next   int
	clock  uint32 // scene clock the client asked for (replay_ms)
}

func loadReplay(path string) (*replayFeed, game.GameBeginData, error) {
	recording, err := qbv.DecodeFile(path)
	if err != nil {
		return nil, game.GameBeginData{}, err
	}
	begin, err := qbv.ParseBootstrapGameBegin(recording)
	if err != nil {
		return nil, game.GameBeginData{}, err
	}
	if begin.GameTimeMS == 0 {
		// Archived recordings keep a zero GameTime; the rule's round length
		// is used instead (view.RoundMS).
		begin.GameTimeMS = 1
	}
	return &replayFeed{events: recording.Events, clock: battleengine.NativeRoundStartClockMS}, begin, nil
}

// replayDue applies every recorded message whose time has been reached.
func (match *onlineMatch) replayDue(clock uint32) []battleengine.Event {
	feed := match.replay
	var events []battleengine.Event
	for feed.next < len(feed.events) && feed.events[feed.next].TimeMS <= clock {
		event := feed.events[feed.next]
		feed.next++
		events = append(events, match.replayApply(event)...)
	}
	return events
}

func (match *onlineMatch) replayApply(event qbv.Event) []battleengine.Event {
	switch event.Schema {
	case game.NotifyPreparedUseProp:
		match.applyPreparedUse(event.Data)
		return nil
	case game.NotifyChangeArbitrator:
		if len(event.Data) >= 2 {
			match.changeArbitrator(binary.BigEndian.Uint16(event.Data))
		}
		return nil
	}
	message := game.BattleMessageData{Time: event.TimeMS, DataID: uint32(event.Schema), Data: event.Data}
	// The file does not say who sent a message. Player messages start with
	// their author's PlayerID and are checked against it; outcome
	// notifications are the arbitrator's. Try the author first, then the
	// arbitrator, never both for the arbitrator's own messages.
	author := match.replayAuthor(event.Data)
	applied := match.applyRemote(author, message)
	if len(applied) == 0 && author != match.arbitrator {
		applied = match.applyRemote(match.arbitrator, message)
	}
	match.forgetPickups(applied)
	return applied
}

func (match *onlineMatch) replayAuthor(body []byte) uint16 {
	if len(body) >= 2 {
		if id := binary.BigEndian.Uint16(body); match.isPlayer(id) {
			return id
		}
	}
	if len(body) >= 4 {
		if id := binary.BigEndian.Uint32(body); id <= 0xffff && match.isPlayer(uint16(id)) {
			return uint16(id)
		}
	}
	return match.arbitrator
}

func (match *onlineMatch) isPlayer(id uint16) bool {
	for _, info := range match.begin.Players {
		if info.PlayerID == id && id != 0 {
			return true
		}
	}
	return false
}

func (match *onlineMatch) replayView() *replayView {
	if match.replay == nil {
		return nil
	}
	view := &replayView{Clock: match.replay.clock, Applied: match.replay.next, Total: len(match.replay.events)}
	if n := len(match.replay.events); n > 0 {
		view.EndMS = match.replay.events[n-1].TimeMS
	}
	return view
}

type replayView struct {
	Clock   uint32 `json:"clock"`
	Applied int    `json:"applied"`
	Total   int    `json:"total"`
	EndMS   uint32 `json:"end_ms"`
}

// replayRulePickups keeps recorded wall items that the current engine does
// not know (Tutorial1/2.qbv from 2007 hide scene 24040) as server-counted
// rule pickups, so the ItemSeed shuffle still places every other item where
// the recording had it.
func replayRulePickups(rules *battleengine.Rules, begin game.GameBeginData) {
	for _, list := range [][]game.GameItemType{begin.NewItems, begin.Items} {
		for _, item := range list {
			scene := item.ItemID
			if scene == 0 || battleengine.PickupSceneSupported(scene) {
				continue
			}
			for index, id := range rules.RulePickupSceneIDs {
				if id == scene {
					break
				}
				if id == 0 {
					rules.RulePickupSceneIDs[index] = scene
					break
				}
			}
		}
	}
}

// record keeps a battle message the live match sent or received, at the
// scene clock it was handled (Client.exe records every match and writes the
// file only if 保存录像 is on at the end, 0x005A35FB).
func (match *onlineMatch) record(schema uint16, body []byte) {
	if match.replay != nil || len(match.recorded) >= qbv.MaxEvents {
		return
	}
	clock := max(match.engine.ElapsedMS(), battleengine.NativeRoundStartClockMS)
	if n := len(match.recorded); n > 0 {
		clock = max(clock, match.recorded[n-1].TimeMS)
	}
	match.recorded = append(match.recorded, qbv.Event{TimeMS: clock, Schema: schema, Data: append([]byte(nil), body...),
		Opaque: [3]uint32{uint32(len(match.recorded) + 1), match.clientTag(), 0}})
}

type recordedView struct {
	Time   uint32    `json:"t"`
	Schema uint16    `json:"s"`
	Data   string    `json:"d"` // hex
	Opaque [3]uint32 `json:"o"`
}

func (match *onlineMatch) recordedView() []recordedView {
	view := make([]recordedView, len(match.recorded))
	for index, event := range match.recorded {
		view[index] = recordedView{Time: event.TimeMS, Schema: event.Schema, Data: hex.EncodeToString(event.Data), Opaque: event.Opaque}
	}
	return view
}

func replayError(err error) error {
	return fmt.Errorf("replay: %w", err)
}
