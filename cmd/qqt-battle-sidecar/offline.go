package main

// Offline mode: a whole match without a server, the local player against
// (and with) AI seats, under every rule the online mode plays.
//
// Every participant gets its own client, the same onlineMatch an online
// match uses: the local player's is the arbitrator, exactly as in a room
// filled with server AIs, and every AI seat is an ordinary non-arbitrator
// client whose local player is driven by a policy (the autoplay bot, or the
// rule-1 ONNX model) instead of a keyboard. Nothing has to be synthesized for
// the AI seats: each one detects its own hits, asks the arbitrator for its
// pickups, bun takes and puts, kicks and pushes, and mirrors its reports
// like a remote human.
//
// The hub stands in for the network and the server:
//   - every client's UDP datagrams are handed to their multicast targets;
//   - the clients share one scene clock, advanced by the step commands
//     (20 ms ticks, as practice mode), not by the wall clock;
//   - the reliable TCP copies (REQUEST_PLAY) go to a local referee that keeps
//     the server's own ledger (internal/game/match.CompetitiveBattle) and
//     answers the requests the server answers (item uses, Boss creation, NPC
//     registration);
//   - the referee concludes the match and the hub returns a GAME_OVER body
//     shaped like the server's, which the client shows as online.

import (
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"math/rand"
	"os"
	"sort"
	"strconv"
	"strings"

	"qqtang/internal/game/battleengine"
	"qqtang/internal/game/mapdata"
	"qqtang/internal/protocol/game"
)

const (
	offlineGameID  = 1
	offlineRoomID  = 1
	offlineUINBase = 10_000 // AI seats look like ordinary humans to every client
	// The server pushes CREATE_NPC_BOSS 50 ms after GAME_BEGIN.
	offlineBossCreateMS = 50
)

// offlineInit is the client's request for an offline match.
type offlineInit struct {
	MapID uint32 `json:"map_id"`
	// Every seat; AI marks the computer's. Teams are the room's 1..8.
	Players []player `json:"players"`
	// The keyboard's seat; 0 lets AIs play every seat (headless tests).
	LocalID   uint16 `json:"local_id"`
	SpawnSeed uint32 `json:"spawn_seed"`
	ItemSeed  uint32 `json:"item_seed"`
	FreeRule  bool   `json:"free_rule"`
	// Boss: activate the map's Boss overlay (its first candidate), if any.
	// Maps 11 and 124 always have theirs.
	Boss bool `json:"boss"`
	// Policy for the AI seats: "" or "auto" (the ONNX model on rules 1 and
	// 4 when it is built in and found, the bot otherwise), "bot", "onnx".
	Policy string `json:"policy"`
	// ONNX files; empty selects configs/models and the bundled runtime.
	Model   string `json:"model"`
	Library string `json:"library"`
	// Log every seat's sidecar messages (the local seat's always go to
	// stderr).
	Verbose bool `json:"verbose"`
	// Quiet silences the local seat too (headless measurements).
	Quiet bool `json:"quiet"`
}

// localPolicy plays one client's local player.
type localPolicy interface {
	act(engine *battleengine.Engine) battleengine.Action
}

// offlineLink is what every client of one offline match shares: the hub's
// scene clock and its datagram relay.
type offlineLink struct {
	clock  uint32 // ms since GAME_BEGIN, a multiple of the tick
	jitter uint32 // sub-tick part of the client tag (native frames are not on ticks)
	hub    *offlineHub
}

func (link *offlineLink) deliver(sender uint16, data []byte) {
	link.hub.route(sender, data)
}

func (link *offlineLink) log(playerID uint16) io.Writer {
	return link.hub.seatLog(playerID)
}

type offlineHub struct {
	link    *offlineLink
	begin   game.GameBeginData
	view    beginView
	entry   mapdata.CompetitiveMap
	seats   map[uint16]*onlineMatch
	order   []uint16 // the arbitrator first
	local   uint16   // whose view the client gets
	ai      map[uint16]bool
	referee *offlineReferee
	// TCP notifications waiting for each client's next step.
	tcp     map[uint16][]tcpPacket
	random  *rand.Rand
	options offlineInit
	// Boss overlay to announce; nil without one.
	boss       *game.CreateNPCBoss
	bossSent   bool
	notifySeq  uint32
	over       *game.GameOverData
	overBody   []byte
	logs       map[uint16]io.Writer
	policyName map[uint16]string
	clientRoot string
	policies   *offlinePolicies
	// Rule 12: the route's stages; restage moves the survivors on.
	stage       *offlineStage
	stageNumber int
	shownStage  int
	nextStage   *offlinePendingStage
}

type offlinePendingStage struct {
	players    []player
	arbitrator uint16
}

func runOffline(init command, lines interface {
	Scan() bool
	Bytes() []byte
	Err() error
}, out interface{ Encode(any) error }) error {
	hub, err := newOfflineHub(init.ClientRoot, *init.Offline)
	if err != nil {
		return err
	}
	if err := out.Encode(hub.state(nil, true)); err != nil {
		return err
	}
	for lines.Scan() {
		var step command
		if err := json.Unmarshal(lines.Bytes(), &step); err != nil {
			return fmt.Errorf("step: %w", err)
		}
		if step.Cmd != "step" {
			return fmt.Errorf("unknown command %q", step.Cmd)
		}
		events, err := hub.step(step)
		if err != nil {
			return err
		}
		if err := out.Encode(hub.state(events, false)); err != nil {
			return err
		}
	}
	return lines.Err()
}

func newOfflineHub(clientRoot string, options offlineInit) (*offlineHub, error) {
	if len(options.Players) == 0 {
		return nil, fmt.Errorf("offline: no players")
	}
	if options.SpawnSeed == 0 {
		options.SpawnSeed = 1
	}
	if options.ItemSeed == 0 {
		options.ItemSeed = 1
	}
	catalog, err := mapdata.LoadCatalog(clientRoot)
	if err != nil {
		return nil, err
	}
	hub := &offlineHub{
		seats: map[uint16]*onlineMatch{}, ai: map[uint16]bool{}, tcp: map[uint16][]tcpPacket{}, logs: map[uint16]io.Writer{},
		random: rand.New(rand.NewSource(int64(options.SpawnSeed)<<32 | int64(options.ItemSeed))), options: options,
		policyName: map[uint16]string{}, clientRoot: clientRoot,
	}
	hub.link = &offlineLink{hub: hub}
	hub.local = options.LocalID
	arbitrator := options.LocalID
	for _, entry := range options.Players {
		if entry.ID == 0 {
			return nil, fmt.Errorf("offline: player ID 0")
		}
		if entry.ID == options.LocalID && entry.AI {
			return nil, fmt.Errorf("offline: the local player %d is marked as AI", entry.ID)
		}
		if entry.ID != options.LocalID {
			hub.ai[entry.ID] = true
		}
	}
	if arbitrator == 0 {
		arbitrator = options.Players[0].ID
		hub.local = arbitrator
	} else if hub.ai[arbitrator] || !hasPlayer(options.Players, arbitrator) {
		return nil, fmt.Errorf("offline: local player %d is not a seat", arbitrator)
	}
	var stage *offlineStage
	entry, competitive := catalog.CompetitiveMap(options.MapID)
	if competitive {
		hub.entry = entry
		hub.begin, hub.boss, err = offlineCompetitiveBegin(entry, options, arbitrator)
	} else if stageMap, isStage := catalog.Map(options.MapID); isStage {
		stage, err = newOfflineStage(clientRoot, catalog, stageMap)
		if err == nil {
			hub.stage = stage
			hub.begin, err = stage.begin(options, arbitrator, options.Players)
		}
	} else {
		err = fmt.Errorf("map %d is neither a competitive map nor an adventure stage", options.MapID)
	}
	if err != nil {
		return nil, err
	}
	if hub.policies, err = newOfflinePolicies(options, hub.begin, entry, competitive, hub.boss != nil); err != nil {
		return nil, err
	}
	if err := hub.startSeats(); err != nil {
		hub.close()
		return nil, err
	}
	hub.referee, err = newOfflineReferee(hub, entry, competitive, stage)
	if err != nil {
		hub.close()
		return nil, err
	}
	names := make([]string, 0, len(hub.order))
	for _, playerID := range hub.order {
		kind := "human"
		if name := hub.policyName[playerID]; name != "" {
			kind = name
		}
		names = append(names, fmt.Sprintf("%d:%s", playerID, kind))
	}
	hub.logf("offline map %d (%s): arbitrator %d, seats %s", hub.begin.MapID, hub.view.Rule, arbitrator, strings.Join(names, " "))
	return hub, nil
}

// startSeats creates every participant's client for hub.begin.
func (hub *offlineHub) startSeats() error {
	payload, err := hub.begin.MarshalLengthPrefixedNetworkBinary()
	if err != nil {
		return err
	}
	uins := map[string]uint32{}
	for _, info := range hub.begin.Players {
		uins[strconv.Itoa(int(info.PlayerID))] = offlineUINBase + uint32(info.PlayerID)
	}
	arbitrator := hub.begin.ArbitratorPlayerID
	hub.order = []uint16{arbitrator}
	for _, info := range hub.begin.Players {
		if info.PlayerID != arbitrator {
			hub.order = append(hub.order, info.PlayerID)
		}
	}
	hub.seats = map[uint16]*onlineMatch{}
	hub.tcp = map[uint16][]tcpPacket{}
	for _, playerID := range hub.order {
		online := &onlineInit{
			Begin: hex.EncodeToString(payload), LocalID: playerID, LocalUIN: offlineUINBase + uint32(playerID),
			UINs: uins, FreeRule: hub.options.FreeRule, offline: hub.link,
		}
		if hub.ai[playerID] || hub.options.LocalID == 0 {
			online.policy = hub.policies.forSeat(playerID)
			if online.policy == nil {
				online.Autoplay = true
			}
			hub.policyName[playerID] = hub.policies.name(playerID)
		}
		match, view, err := newOnlineMatch(command{Cmd: "init", ClientRoot: hub.clientRoot, Online: online})
		if err != nil {
			return fmt.Errorf("seat %d: %w", playerID, err)
		}
		if match.bot != nil {
			// Offline the bot is an opponent and a teammate, not a test
			// driver: no special-element tours.
			match.bot.special = nil
			match.bot.offline = true
		}
		hub.seats[playerID] = match
		if playerID == hub.local {
			hub.view = view
		}
	}
	return nil
}

// restage moves the survivors of a cleared adventure stage to the next one:
// a new GAME_BEGIN, every client rebuilt, the scene clock restarted.
func (hub *offlineHub) restage() {
	pending := hub.nextStage
	hub.nextStage = nil
	if hub.options.LocalID != 0 && !hasPlayer(pending.players, hub.local) {
		// The keyboard's player died on the way: the route ends for it.
		over := game.GameOverData{Time: hub.link.clock, GameMode: game.SettlementGameModeAdventure}
		for _, info := range hub.begin.Players {
			over.Results = append(over.Results, hub.stage.result(info.PlayerID, game.GameResultLoss))
		}
		hub.referee.result, hub.referee.dueAt = &over, hub.link.clock
		return
	}
	if hub.options.LocalID == 0 {
		hub.local = pending.arbitrator
	}
	begin, err := hub.stage.begin(hub.options, pending.arbitrator, pending.players)
	if err == nil {
		err = hub.stage.newBattle(begin)
	}
	if err != nil {
		hub.logf("next stage: %v", err)
		over := game.GameOverData{Time: hub.link.clock, GameMode: game.SettlementGameModeAdventure}
		for _, info := range hub.begin.Players {
			over.Results = append(over.Results, hub.stage.result(info.PlayerID, game.GameResultWin))
		}
		hub.referee.result, hub.referee.dueAt = &over, hub.link.clock
		return
	}
	hub.close()
	hub.begin = begin
	hub.link.clock, hub.link.jitter = 0, 0
	if err := hub.startSeats(); err != nil {
		hub.logf("next stage seats: %v", err)
		return
	}
	hub.stageNumber++
}

func hasPlayer(players []player, playerID uint16) bool {
	for _, entry := range players {
		if entry.ID == playerID {
			return true
		}
	}
	return false
}

// offlineCompetitiveBegin is the server's GAME_BEGIN for a competitive map
// (probe newCompetitiveGameData): the map's wall pool plus the rule's
// objective items, and the delayed supply the arbitrator dispatches.
func offlineCompetitiveBegin(entry mapdata.CompetitiveMap, options offlineInit, arbitrator uint16) (game.GameBeginData, *game.CreateNPCBoss, error) {
	if entry.Rule == mapdata.CompetitiveRuleUnknown {
		return game.GameBeginData{}, nil, fmt.Errorf("map %d has an unsupported native rule %d", entry.ID, entry.NativeRule)
	}
	var candidate *mapdata.CompetitiveBossCandidate
	if len(entry.BossCandidates) > 0 && (options.Boss || entry.ID == 11 || entry.ID == 124) {
		chosen := entry.BossCandidates[0]
		candidate = &chosen
	}
	players := make([]game.PlayerGameInfo, 0, len(options.Players))
	teamSizes := map[byte]int{}
	largest := 0
	roles := make([]uint16, 0, len(options.Players))
	for _, entry := range options.Players {
		team := entry.Team
		if candidate != nil && candidate.Overlay.TeamProjection == mapdata.CompetitiveTeamsUnified && candidate.Overlay.UnifiedTeamID != 0 {
			team = candidate.Overlay.UnifiedTeamID
		}
		if team == 0 {
			return game.GameBeginData{}, nil, fmt.Errorf("player %d has no team", entry.ID)
		}
		players = append(players, game.PlayerGameInfo{PlayerID: entry.ID, RoleID: byte(entry.Role), TeamID: team})
		teamSizes[team]++
		largest = max(largest, teamSizes[team])
		roles = append(roles, entry.Role)
	}
	if len(teamSizes) < 2 && candidate == nil {
		return game.GameBeginData{}, nil, fmt.Errorf("map %d needs at least two teams", entry.ID)
	}
	supply := entry.PlanCompetitiveSupply(options.ItemSeed, roles, candidate != nil)
	newItems := mergeSceneItems(supply.WallItems, entry.CompetitiveObjectiveSceneItems(largest, options.ItemSeed))
	begin, err := game.NewGameBeginData(game.GameBeginOptions{
		GameID: offlineGameID, MapID: entry.ID, SpawnSeed: options.SpawnSeed, ItemSeed: options.ItemSeed,
		ArbitratorPlayerID: arbitrator, ContinueID: game.NoRemoteContinueFileID, MapHash: entry.MapHash,
		GameTimeMS: game.DefaultCompetitiveGameTimeMS, Players: players,
		NewItems: newItems, Items: mergeSceneItems(supply.DelayedItems),
	})
	if err != nil || candidate == nil {
		return begin, nil, err
	}
	aiType, err := candidate.Entity.NativeAIType()
	if err != nil {
		return game.GameBeginData{}, nil, err
	}
	// probe competitiveBossStartData; the reward inventories are server
	// configuration, so offline Bosses drop nothing.
	boss := &game.CreateNPCBoss{Bosses: []game.CompetitiveBossInfo{{
		BossID: offlineBossFirstID, RoleID: byte(candidate.Entity.RoleID), TeamID: offlineBossTeamID, AIType: aiType,
		HP: candidate.Entity.HP, Rate: candidate.Entity.Rate, Bubble: candidate.Entity.Bubble, Power: candidate.Entity.Power,
		Skills: append([]uint32(nil), candidate.Entity.Skills...),
	}}}
	return begin, boss, nil
}

const (
	offlineBossTeamID  = byte(7)       // probe competitiveBossNeutralTeamID
	offlineBossFirstID = uint16(30001) // probe competitiveBossFirstID
)

// mergeSceneItems is probe mergeCompetitiveSceneItems.
func mergeSceneItems(groups ...[]mapdata.CompetitiveWallItem) []game.GameItemType {
	quantities := map[uint32]int{}
	var order []uint32
	for _, group := range groups {
		for _, item := range group {
			if item.SceneID == 0 || item.Quantity <= 0 {
				continue
			}
			if _, seen := quantities[item.SceneID]; !seen {
				order = append(order, item.SceneID)
			}
			quantities[item.SceneID] += int(item.Quantity)
		}
	}
	items := make([]game.GameItemType, 0, len(order))
	for _, scene := range order {
		if quantity := quantities[scene]; quantity > 0 && quantity <= 0x7fff {
			items = append(items, game.GameItemType{ItemID: scene, Quantity: int16(quantity)})
		}
	}
	return items
}

func (hub *offlineHub) close() {
	for _, seat := range hub.seats {
		seat.close()
	}
}

func (hub *offlineHub) seatLog(playerID uint16) io.Writer {
	if writer, ok := hub.logs[playerID]; ok {
		return writer
	}
	var writer io.Writer = io.Discard
	if playerID == hub.local && !hub.options.Quiet || hub.options.Verbose {
		writer = os.Stderr
		if playerID != hub.local {
			writer = prefixWriter{out: os.Stderr, prefix: fmt.Sprintf("[seat %d] ", playerID)}
		}
	}
	hub.logs[playerID] = writer
	return writer
}

type prefixWriter struct {
	out    io.Writer
	prefix string
}

func (writer prefixWriter) Write(data []byte) (int, error) {
	_, err := writer.out.Write(append([]byte(writer.prefix), data...))
	return len(data), err
}

func (hub *offlineHub) logf(format string, args ...any) {
	if !hub.options.Quiet {
		fmt.Fprintf(os.Stderr, "[offline] "+format+"\n", args...)
	}
}

// route hands a client's datagram to its multicast targets, as the server's
// QQTPPP relay forwards the same bytes.
func (hub *offlineHub) route(sender uint16, data []byte) {
	packet, err := game.DecodeLegacyUDPControlPacket(data)
	if err != nil || packet.Multicast == nil {
		return
	}
	for _, target := range packet.Multicast.Targets {
		if seat := hub.seats[target.PlayerID]; seat != nil && target.PlayerID != sender {
			seat.offlineInbox = append(seat.offlineInbox, append([]byte(nil), data...))
		}
	}
}

// notify queues a server notification (NOTIFY_GAME_EVENT) for one client.
func (hub *offlineHub) notify(playerID uint16, schema uint16, body []byte) {
	hub.notifySeq++
	payload, err := (game.NotifyGameEvent{RoomID: offlineRoomID, GameDataSequence: hub.notifySeq, Schema: schema, Body: body}).MarshalNetworkBinary()
	if err != nil {
		hub.logf("notify 0x%04X: %v", schema, err)
		return
	}
	hub.tcp[playerID] = append(hub.tcp[playerID], tcpPacket{Command: gameEventNotifyCommand, Payload: hex.EncodeToString(payload)})
}

func (hub *offlineHub) notifyAll(schema uint16, body []byte) {
	for _, playerID := range hub.order {
		hub.notify(playerID, schema, body)
	}
}

// step advances every client by the command's ticks. The local player's
// keys apply to the first tick (pulses) and are held for the rest (moves).
func (hub *offlineHub) step(step command) ([]battleengine.Event, error) {
	if hub.over != nil {
		return nil, nil
	}
	if step.Ticks <= 0 {
		// Between ticks the client still latches its keys.
		return hub.stepSeat(hub.local, step.Actions)
	}
	var events []battleengine.Event
	for tick := 0; tick < step.Ticks && hub.over == nil; tick++ {
		hub.link.clock += tickMS
		hub.link.jitter = uint32(hub.random.Intn(tickMS))
		hub.serverTick()
		actions := step.Actions
		if tick > 0 {
			actions = heldOnly(step.Actions)
		}
		if hub.nextStage != nil {
			hub.restage()
			hub.serverTick()
		}
		for _, playerID := range hub.order {
			var seatActions []action
			if playerID == hub.local {
				seatActions = actions
			}
			seatEvents, err := hub.stepSeat(playerID, seatActions)
			if err != nil {
				return events, err
			}
			if playerID == hub.local {
				events = append(events, seatEvents...)
			}
		}
	}
	return events, nil
}

func heldOnly(actions []action) []action {
	held := make([]action, len(actions))
	for index, entry := range actions {
		held[index] = action{ID: entry.ID, Move: entry.Move}
	}
	return held
}

func (hub *offlineHub) stepSeat(playerID uint16, actions []action) ([]battleengine.Event, error) {
	seat := hub.seats[playerID]
	tcp := hub.tcp[playerID]
	hub.tcp[playerID] = nil
	events, err := seat.step(command{Cmd: "step", Actions: actions, TCP: tcp})
	if err != nil {
		return events, fmt.Errorf("seat %d: %w", playerID, err)
	}
	for _, packet := range seat.takeTCP() {
		hub.serverTCP(playerID, packet)
	}
	hub.measure(seat, events)
	return events, nil
}

// measure counts a seat's own player caught by its own or a teammate's
// bubble, as that seat's engine (which detects its player's hits) saw it.
func (hub *offlineHub) measure(seat *onlineMatch, events []battleengine.Event) {
	for _, event := range events {
		if event.Kind != battleengine.EventActorTrapped || event.TargetID != seat.local || event.PlayerID == 0 {
			continue
		}
		stats := hub.referee.stat(seat.local)
		if event.PlayerID == seat.local {
			stats.SelfTraps++
			seat.lastSelfTrap = event.TimeMS
		} else if owner, ok := actorByID(seat.engine.Actors(), event.PlayerID); ok {
			if victim, found := actorByID(seat.engine.Actors(), seat.local); found && owner.TeamID == victim.TeamID {
				stats.TeamTraps++
			}
		}
	}
}

// serverTick is the server's own clock: the Boss announcement, the round
// timeout and the delayed GAME_OVER.
func (hub *offlineHub) serverTick() {
	now := hub.link.clock
	if hub.boss != nil && !hub.bossSent && now >= offlineBossCreateMS {
		hub.bossSent = true
		if body, err := hub.boss.MarshalNetworkBinary(); err == nil {
			hub.referee.registerBoss(*hub.boss)
			hub.notifyAll(game.CreateNPCBossEvent, body)
		} else {
			hub.logf("CREATE_NPC_BOSS: %v", err)
		}
	}
	if over, ok := hub.referee.due(now); ok {
		body, err := over.MarshalNetworkBinary()
		if err != nil {
			hub.logf("GAME_OVER: %v", err)
			body = nil
		}
		hub.over, hub.overBody = &over, body
		results := make([]string, 0, len(over.Results))
		for _, result := range over.Results {
			results = append(results, fmt.Sprintf("%d=%d", result.PlayerID, result.Result))
		}
		hub.logf("game over at %d ms: %s", now, strings.Join(results, " "))
	}
}

// serverTCP is the server's handling of one client's reliable copy.
func (hub *offlineHub) serverTCP(sender uint16, packet tcpPacket) {
	if packet.Command != requestPlayCommand {
		return
	}
	payload, err := hex.DecodeString(packet.Payload)
	if err != nil {
		return
	}
	event, err := game.ParseGameEventPayload(payload)
	if err != nil {
		return
	}
	hub.referee.observe(sender, event, hub.link.clock)
	switch event.Schema {
	case game.RequestUseItem:
		// The server validates the use and broadcasts it (0x0FB0).
		hub.notifyAll(game.NotifyPlayerUseItem, event.Body)
	case game.RequestPreparedUseProp:
		hub.notifyAll(game.NotifyPreparedUseProp, event.Body)
	case game.CreateNPCBossEvent:
		// A rule-8 NPC: the server registers it and echoes the request.
		hub.tcp[sender] = append(hub.tcp[sender], packet)
	case game.RequestGameNextMap:
		hub.referee.nextMap(sender, event)
	}
}

// state is the local client's view plus the offline result.
func (hub *offlineHub) state(events []battleengine.Event, first bool) offlineStateView {
	seat := hub.seats[hub.local]
	state := offlineStateView{stateView: seat.stateView(events)}
	_ = seat.takeTCP()
	if first || hub.shownStage != hub.stageNumber {
		hub.shownStage = hub.stageNumber
		begin := hub.view
		state.Begin = &begin
	}
	state.Offline = &offlineView{Clock: hub.link.clock, Stage: hub.stageNumber, AI: map[string]string{}}
	for playerID, name := range hub.policyName {
		state.Offline.AI[strconv.Itoa(int(playerID))] = name
	}
	if hub.over != nil {
		state.Offline.GameOver = hex.EncodeToString(hub.overBody)
	}
	state.Offline.Score = hub.referee.score()
	return state
}

type offlineStateView struct {
	stateView
	Offline *offlineView `json:"offline,omitempty"`
}

type offlineView struct {
	Clock uint32 `json:"clock"`
	// Adventure stages passed (a new begin comes with each).
	Stage int `json:"stage"`
	// The GAME_OVER body (hex) once the match has ended.
	GameOver string `json:"game_over,omitempty"`
	// Policy of each AI seat by player ID.
	AI    map[string]string `json:"ai,omitempty"`
	Score *offlineScore     `json:"score,omitempty"`
}

// sortedSeats lists the participants in GAME_BEGIN order.
func (hub *offlineHub) sortedSeats() []uint16 {
	ids := make([]uint16, 0, len(hub.seats))
	for playerID := range hub.seats {
		ids = append(ids, playerID)
	}
	sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
	return ids
}
