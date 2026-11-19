package main

// Tutorial mode: the newbie tutorial (新手任务) as Client.exe plays it, a
// local game on map 1501 whose Task.txt controller (vtable 0x007A2348,
// docs/tutorial-re.md) tells the story, locks and frees the keyboard, adds
// and moves the scripted actors and switches the map: 1501, 1502, 1503,
// 1504, then map 311.
//
// The controller runs here, next to one engine (no seats, no referee: the
// original is a local game too). Each step answers with the engine state and
// a "tutorial" part that the client draws: the hint (index into the text
// table 0x007F49C4) with its speaker's picture, the target arrow, Kubi, the
// story scroll, the task card and the result once a 0xFBB would end it.
//
// Fixed original bugs: the local PlayerID is added once to the actor field
// (Client.exe adds it twice, 0x005C64A1/0x005C71A5); NotifyAddHint gets its
// text for texts over 200 bytes (the client draws it). Host decisions are
// marked "[host]".

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"sort"

	"qqtang/internal/game/battleengine"
	"qqtang/internal/game/mapdata"
)

const (
	tutorialFirstMap = 1501 // QQTSection 0x1001C5A7: map 0x5DD
	tutorialItemMap  = 1503 // 0x005C751A, 0x00604DB0: the item stage
	tutorialDefend   = 1504 // 0x0060D6E5: 70 s, then the task list again
	tutorialRole     = 8    // QQTSection 0x1001C5A7 writes role 8
	tutorialTeam     = 1
	tutorialGameID   = 1

	tutorialHintMS     = 5000   // actions 3, 8, 12, 29, 30
	tutorialScrollMS   = 10_000 // action 27 (Task.txt's 12000 is ignored)
	tutorialCardMS     = 5000   // action 28
	tutorialStageMS    = 240_000
	tutorialDefendMS   = 70_000
	tutorialReviveMS   = 10_000 // death handler 0x00606606: step 0x17, 10000 ms
	tutorialTrapMS     = defaultTrapMS
	tutorialTrapLongMS = 240_000 // actions 15 and 16: +0x3C4 += 240000
	tutorialItemKinds  = 9

	tutorialInputLocked = 2 // controller +0xAC
	tutorialInputFree   = 4

	tutorialResultLoss = 0 // 0xFBB's result byte (GAME_OVER results)
	tutorialResultWin  = 2

	// Talking pictures: misc(360 + role) at (214, 239) (0x005B035E); the
	// second picture used for actions 29 and 30 (actor +0x46C) is never
	// written by plain code; every text of those actions is Kubi's, so it
	// is taken as misc359, Kubi's head [inferred].
	tutorialAvatarBase = 360
	tutorialKubiAvatar = 359
	// The task cards: misc(0x172 + n), n = map - 1501 with 0 -> 1 and
	// negative (map 311) -> 4 (0x005B5F68).
	tutorialCardBase = 0x172
)

// tutorialItems are the hidden items of 1503 [host]: the local GAME_BEGIN
// of QQTSection 0x1001C5A7 carries none and the map has no wall-item table,
// yet the stage asks for the nine kinds that 0x00605386 knows.
var tutorialItems = []mapdata.CompetitiveWallItem{
	{SceneID: 1, Quantity: 4}, {SceneID: 2, Quantity: 4}, {SceneID: 3, Quantity: 4},
	{SceneID: 4, Quantity: 2}, {SceneID: 23, Quantity: 2}, {SceneID: 24, Quantity: 2},
	{SceneID: 25, Quantity: 2}, {SceneID: 61, Quantity: 2}, {SceneID: 81, Quantity: 2},
}

// tutorialItemHint is 0x00605386: the hint of a newly eaten item kind (1
// means none).
func tutorialItemHint(scene uint32) int32 {
	switch {
	case scene == 1:
		return 16
	case scene == 2:
		return 17
	case scene == 3:
		return 18
	case scene == 4:
		return 21
	case scene == 23:
		return 22
	case scene == 24:
		return 19
	case scene == 25:
		return 20
	case scene == 61:
		return 23
	case scene > 80 && scene < 85:
		return 24
	}
	return 1
}

// tutorialPanelFrame is 0x005EE60D: the frame of misc602 (拾得道具说明).
func tutorialPanelFrame(scene uint32) int {
	switch {
	case scene == 23:
		return 8
	case scene == 1:
		return 5
	case scene == 2:
		return 7
	case scene == 3:
		return 9
	case scene == 4:
		return 10
	case scene == 21:
		return 3
	case scene == 24:
		return 1
	case scene == 25:
		return 4
	case scene == 27:
		return 2
	case scene == 61:
		return 6
	case scene > 80 && scene < 85:
		return 11
	}
	return 0
}

func tutorialStage(mapID uint32) bool { return mapID >= 1501 && mapID <= 1504 }

type tutorialInit struct {
	MapID   uint32 `json:"map_id"`   // 0: 1501
	LocalID uint16 `json:"local_id"` // 0: 1
	Role    uint16 `json:"role"`     // 0: 8
	Seed    uint32 `json:"seed"`
	Quiet   bool   `json:"quiet"`
	// Tasks replaces Task.txt (tests).
	Tasks string `json:"tasks,omitempty"`
	// Autoplay lets the bot play the local player in free phases (tests).
	Autoplay bool `json:"autoplay,omitempty"`
}

type tutorialRoutine struct {
	steps   []taskRecord
	index   int
	started uint32
}

type tutorialHintView struct {
	Text   int32 `json:"text"`   // index into 0x007F49C4
	Avatar int   `json:"avatar"` // misc picture of the speaker
}

type tutorialView struct {
	Stage  int               `json:"stage"`
	MapID  uint32            `json:"map_id"`
	Action int16             `json:"action"`
	Hint   *tutorialHintView `json:"hint,omitempty"`
	Arrow  *cell             `json:"arrow,omitempty"`
	Kubi   *cell             `json:"kubi,omitempty"`
	// ScrollMS: the story scroll has run this long.
	ScrollMS *uint32 `json:"scroll_ms,omitempty"`
	Card     int     `json:"card,omitempty"`
	// HideUI: gm+0x56EFD is 0 (scroll and card).
	HideUI bool `json:"hide_ui,omitempty"`
	Free   bool `json:"free"`
	// The side panel (0x005EE383): the last item's frame and, on 1503, the
	// kinds eaten.
	PanelItem int  `json:"panel_item"`
	Eaten     int  `json:"eaten"`
	Routine   bool `json:"routine,omitempty"`
	// Result: 0xFBB's result byte (0 loss, 2 win) once the game is over.
	Result *int `json:"result,omitempty"`
}

type tutorialStateView struct {
	stateView
	Tutorial *tutorialView `json:"tutorial"`
}

type tutorialMatch struct {
	options    tutorialInit
	clientRoot string
	catalog    *mapdata.Catalog
	local      uint16
	role       uint16
	engine     *battleengine.Engine
	begin      beginView
	stage      int
	shownStage int

	tasks   []taskRecord // tasks[0] is the current record
	state   int          // +0x88: 1 new, 2 running, 3 done
	started uint32       // +0x94
	input   byte         // +0xAC

	routineMode bool // +0xAD
	routines    map[uint16]*tutorialRoutine
	deaths      map[uint16]uint32
	bots        map[uint16]*autoplayBot
	walks       map[uint16]battleengine.Cell
	bombs       map[uint16]bool
	forced      *battleengine.Cell
	driver      *tutorialDriver

	hint      *tutorialHintView
	arrow     *battleengine.Cell
	kubi      *battleengine.Cell
	scrollAt  *uint32
	card      int
	hideUI    bool
	panelItem int
	eaten     []uint32
	itemCount int // +0xB0
	timerDone bool
	result    *int
	nextMap   uint32

	bubbleKey, anyKey bool
	log               io.Writer
}

func runTutorial(init command, lines interface {
	Scan() bool
	Bytes() []byte
	Err() error
}, out interface{ Encode(any) error }) error {
	match, err := newTutorialMatch(init.ClientRoot, *init.Tutorial)
	if err != nil {
		return err
	}
	if err := out.Encode(match.view(nil)); err != nil {
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
		events, err := match.step(step)
		if err != nil {
			return err
		}
		if err := out.Encode(match.view(events)); err != nil {
			return err
		}
	}
	return lines.Err()
}

func newTutorialMatch(clientRoot string, options tutorialInit) (*tutorialMatch, error) {
	if options.MapID == 0 {
		options.MapID = tutorialFirstMap
	}
	if options.LocalID == 0 {
		options.LocalID = 1
	}
	if options.Role == 0 {
		options.Role = tutorialRole
	}
	if options.Seed == 0 {
		options.Seed = 1
	}
	catalog, err := mapdata.LoadCatalog(clientRoot)
	if err != nil {
		return nil, err
	}
	var tasks []taskRecord
	if options.Tasks != "" {
		tasks, err = parseTasks([]byte(options.Tasks))
	} else {
		tasks, err = loadTasks(clientRoot)
	}
	if err != nil {
		return nil, err
	}
	if len(tasks) == 0 {
		return nil, fmt.Errorf("Task.txt has no records")
	}
	match := &tutorialMatch{
		options: options, clientRoot: clientRoot, catalog: catalog, local: options.LocalID, role: options.Role,
		tasks: tasks, state: 1, input: tutorialInputLocked, shownStage: -1, log: os.Stderr,
	}
	if options.Quiet {
		match.log = io.Discard
	}
	if err := match.startStage(options.MapID); err != nil {
		return nil, err
	}
	return match, nil
}

func (match *tutorialMatch) logf(format string, args ...any) {
	fmt.Fprintf(match.log, "[tutorial] "+format+"\n", args...)
}

// startStage is the local GAME_BEGIN (QQTSection 0x1001C5A7, and
// 0x005A40B3 for action 10): the same begin on another map, only the local
// player. The controller keeps its task list (0x005C5F8A).
func (match *tutorialMatch) startStage(mapID uint32) error {
	entry, ok := match.catalog.TutorialMap(mapID)
	if !ok {
		entry, ok = match.catalog.CompetitiveMapMetadata(mapID)
	}
	if !ok {
		return fmt.Errorf("tutorial map %d is not installed", mapID)
	}
	participant, err := battleengine.ParticipantFromNativeRole(match.local, match.role, tutorialTeam, battleengine.ParticipantHuman)
	if err != nil {
		return err
	}
	var items []mapdata.CompetitiveWallItem
	switch {
	case mapID == tutorialItemMap:
		items = tutorialItems
	case entry.NativeRule == 1:
		// Map 311 [host]: the map's own wall items, as the server would roll.
		items = entry.PlanCompetitiveSupply(match.options.Seed, []uint16{match.role}, false).WallItems
	}
	seed := match.options.Seed + uint32(match.stage)
	config, err := battleengine.ConfigFromTutorialMap(entry, battleengine.TutorialMapConfigOptions{
		SpawnSeed: seed, ItemSeed: seed ^ 0x5A5A,
		Rules:        battleengine.Rules{TickMS: tickMS, TrapDurationMS: tutorialTrapMS, NativeActionInventory: true},
		Participants: []battleengine.Participant{participant}, WallItems: items,
	})
	if err != nil {
		return err
	}
	engine, err := battleengine.New(config)
	if err != nil {
		return fmt.Errorf("tutorial map %d: %w", mapID, err)
	}
	if mapID >= 1501 && mapID <= 1503 {
		// 0x00607899: no flame test for the local player on 1501..1503.
		if err := engine.SetFlameImmune(match.local, true); err != nil {
			return err
		}
	}
	rule := "tutorial"
	if !tutorialStage(mapID) {
		rule = string(entry.Rule)
	}
	if match.engine != nil {
		match.stage++
	}
	match.engine = engine
	match.begin = beginView{GameID: tutorialGameID, MapID: mapID, Rule: rule, RoundMS: tutorialStageMS, Arbitrator: match.local,
		Players: []player{{ID: match.local, Role: match.role, Team: tutorialTeam}}}
	// A new scene: its routines, pictures, panel and rule object are new;
	// 0x005C5F8A leaves routine mode.
	match.routineMode = false
	match.routines, match.deaths = map[uint16]*tutorialRoutine{}, map[uint16]uint32{}
	match.bots, match.walks, match.bombs = map[uint16]*autoplayBot{}, map[uint16]battleengine.Cell{}, map[uint16]bool{}
	match.forced, match.arrow, match.kubi, match.scrollAt = nil, nil, nil, nil
	match.card, match.hideUI, match.panelItem, match.eaten, match.timerDone = 0, false, 0, nil, false
	if match.options.Autoplay {
		match.driver = newTutorialDriver(match.local)

	}
	match.logf("stage %d: map %d (%s)", match.stage, mapID, rule)
	return nil
}

// step advances the game by the command's ticks; the local player's keys
// count on the first tick (pulses) and are held for the rest (moves).
func (match *tutorialMatch) step(step command) ([]battleengine.Event, error) {
	if match.result != nil {
		return nil, nil
	}
	ticks := max(step.Ticks, 1)
	var events []battleengine.Event
	for tick := 0; tick < ticks && match.result == nil; tick++ {
		var input action
		for _, entry := range step.Actions {
			if entry.ID == match.local {
				input = entry
			}
		}
		if tick > 0 {
			input = action{ID: input.ID, Move: input.Move}
		}
		match.bubbleKey = input.Bomb // 0x005A4BF7: the bubble key
		match.anyKey = step.AnyKey && tick == 0
		match.control()
		if match.nextMap != 0 {
			next := match.nextMap
			match.nextMap = 0
			if err := match.startStage(next); err != nil {
				return events, err
			}
			events = nil
			continue
		}
		stepEvents, err := match.engine.Step(match.actions(input))
		if err != nil {
			return events, err
		}
		events = append(events, stepEvents...)
		match.observe(stepEvents)
		match.timers()
	}
	return events, nil
}

// actions are every actor's input for one tick.
func (match *tutorialMatch) actions(input action) []battleengine.Action {
	actors := match.engine.Actors()
	result := make([]battleengine.Action, 0, len(actors))
	for _, actor := range actors {
		next := battleengine.Action{PlayerID: actor.PlayerID}
		switch {
		case actor.State != battleengine.ActorActive:
		case actor.PlayerID == match.local:
			switch {
			case match.forced != nil:
				next.Move = match.walk(actor, *match.forced)
			case match.input != tutorialInputFree:
				// 0x00610BA0, 0x006112AF: no bubble, no move.
			case match.driver != nil:
				next = match.driver.act(match)
			default:
				next.Move = battleengine.Direction(input.Move)
				next.PlaceBomb, next.UseActionID = input.Bomb, input.Use
			}
		case match.bots[actor.PlayerID] != nil:
			next = match.bots[actor.PlayerID].act(match.engine)
		default:
			if target, walking := match.walks[actor.PlayerID]; walking {
				next.Move = match.walk(actor, target)
			}
			if match.bombs[actor.PlayerID] {
				next.PlaceBomb = true
			}
		}
		delete(match.bombs, actor.PlayerID)
		result = append(result, next)
	}
	return result
}

// walk is the way to a cell over open cells (the role's vtable +0x74 finds
// a path; its search is not ported [inferred: shortest path]).
func (match *tutorialMatch) walk(actor battleengine.Actor, target battleengine.Cell) battleengine.Direction {
	here := actor.Position.Cell()
	if here == target {
		if onCentre(actor.Position) {
			return battleengine.DirectionNone
		}
		return steer(actor.Position, target)
	}
	world := newAutoplayWorld(match.engine)
	path := world.nearest(here, world.open, func(cell battleengine.Cell) bool { return cell == target })
	if len(path) < 2 {
		return battleengine.DirectionNone
	}
	return steer(actor.Position, path[1])
}

func (match *tutorialMatch) actor(playerID uint16) (battleengine.Actor, bool) {
	return actorByID(match.engine.Actors(), playerID)
}

func (match *tutorialMatch) at(playerID uint16, cell battleengine.Cell) bool {
	actor, ok := match.actor(playerID)
	return ok && actor.State != battleengine.ActorEliminated && actor.Position.Cell() == cell
}

// roleID is the actor of a record's f0 (local PlayerID + f0).
func (match *tutorialMatch) roleID(record taskRecord) uint16 {
	return match.local + uint16(record.Role)
}

// control is one frame of the controller (vtable +4, 0x005C5DF2).
func (match *tutorialMatch) control() {
	now := match.engine.ElapsedMS()
	if match.routineMode {
		match.runRoutines(now)
		return
	}
	switch {
	case len(match.tasks) == 0:
		// 0x005C5F3A: nothing left ends the tutorial (0x005A4181).
		match.finish(tutorialResultLoss, "task list empty")
	case match.state == 3:
		if len(match.tasks) > 1 { // 0x005C6187
			match.tasks = match.tasks[1:]
			match.state, match.started = 2, now
		}
	case match.state == 1: // 0x005C61DE
		match.state, match.started = 2, now
	case match.state == 2:
		match.execute(now)
	}
}

func (match *tutorialMatch) done() { match.state = 3 }

// free: the keyboard moves the local player (controller +0xAC == 4).
func (match *tutorialMatch) free() bool {
	return match.input == tutorialInputFree && match.forced == nil
}

// execute is 0x005C6221 for the current record.
func (match *tutorialMatch) execute(now uint32) {
	record := match.tasks[0]
	elapsed := now - match.started
	match.input = tutorialInputLocked
	switch record.Action {
	case taskScroll:
		if elapsed < tutorialScrollMS && !match.anyKey {
			started := match.started
			match.hideUI, match.scrollAt = true, &started
			return
		}
		match.hideUI, match.scrollAt = false, nil
	case taskCard:
		if elapsed < tutorialCardMS && !match.anyKey {
			match.hideUI, match.card = true, match.cardImage()
			return
		}
		match.hideUI, match.card = false, 0
	case taskWait:
		if elapsed < record.MS {
			return
		}
	case taskForcedWalk:
		target := record.to()
		if !match.at(match.local, target) {
			match.forced = &target
			return
		}
		match.forced = nil
		match.teleport(match.local, target)
	case taskSay, taskKubiSay:
		if elapsed < tutorialHintMS && !match.bubbleKey {
			avatar := tutorialAvatarBase + int(match.role)
			if record.Action == taskKubiSay {
				avatar = tutorialKubiAvatar
			}
			match.hint = &tutorialHintView{Text: record.Param, Avatar: avatar}
			return
		}
		match.hint = nil
	case taskKubiSayMark:
		if elapsed < tutorialHintMS {
			match.hint = &tutorialHintView{Text: record.Param, Avatar: tutorialKubiAvatar}
			return
		}
		match.hint = nil
		target := record.to()
		match.arrow = &target
	case taskActorSay, taskActorSayMark:
		skip := record.Action == taskActorSay && match.bubbleKey
		if elapsed < tutorialHintMS && !skip {
			avatar := 0
			if actor, ok := match.actor(match.roleID(record)); ok {
				avatar = tutorialAvatarBase + int(actor.RoleID)
			}
			match.hint = &tutorialHintView{Text: record.Param, Avatar: avatar}
			return
		}
		match.hint = nil
		if record.Action == taskActorSayMark {
			target := record.to()
			match.arrow = &target
		}
	case taskTeleport:
		match.teleport(match.local, record.to())
	case taskSpawn:
		match.spawn(record)
	case taskWalk:
		id := match.roleID(record)
		if !match.at(id, record.to()) {
			match.walks[id] = record.to()
			return
		}
		delete(match.walks, id)
	case taskSwitchMap:
		match.nextMap = uint32(record.Param)
	case taskBomb:
		match.bombs[match.roleID(record)] = true
	case taskReach:
		match.input = tutorialInputFree
		if !match.at(match.local, record.to()) {
			return
		}
		match.arrow = nil
		match.input = tutorialInputLocked
	case taskTrapped:
		// 0x005C6CAD: a teammate (role 3, speed 2) wrapped in a bubble.
		id := match.roleID(record)
		match.addActor(id, 3, tutorialTeam, 2, record.to())
		match.trap(id)
	case taskTrapAgain:
		match.trap(match.roleID(record))
	case taskRoutines:
		match.loadRoutines(record)
		return
	case taskKubi:
		match.input = tutorialInputFree
		kubi := record.from()
		match.kubi = &kubi
	case taskEatItems:
		match.input = tutorialInputFree
		match.arrow = nil
		if match.itemCount != tutorialItemKinds {
			return
		}
	default:
		// Unknown actions never finish in Client.exe; finish them here.
		match.logf("record %d: unknown action %d skipped", record.Seq, record.Action)
	}
	match.done()
}

func (match *tutorialMatch) cardImage() int {
	index := int(match.begin.MapID) - tutorialFirstMap
	if index == 0 {
		index = 1
	} else if index < 0 {
		index = 4
	}
	return tutorialCardBase + index
}

func (match *tutorialMatch) teleport(playerID uint16, cell battleengine.Cell) {
	if err := match.engine.TeleportActor(playerID, cell); err != nil {
		match.logf("teleport %d: %v", playerID, err)
	}
}

func (match *tutorialMatch) trap(playerID uint16) {
	if _, err := match.engine.TrapActor(playerID, tutorialTrapLongMS); err != nil {
		match.logf("trap %d: %v", playerID, err)
	}
}

// spawn is action 6 (0x005C6425): f9 0 is a teammate of role 3, otherwise
// an opponent of role 0x19 on the next team; 2 bubbles of power 2, speed
// rate (f7 == 0) + 1; AI type 1 on 1501..1504 (only the script moves it),
// 2 elsewhere (0x005C710A: the actor plays by itself).
func (match *tutorialMatch) spawn(record taskRecord) {
	id := match.roleID(record)
	role, team := uint16(3), byte(tutorialTeam)
	if record.Param != 0 {
		role, team = 0x19, tutorialTeam+1
	}
	speed := byte(1)
	if record.MS == 0 {
		speed = 2
	}
	if !match.addActor(id, role, team, speed, record.to()) {
		return
	}
	if !tutorialStage(match.begin.MapID) {
		bot := newAutoplayBot(id)
		bot.offline = true
		match.bots[id] = bot
	}
}

func (match *tutorialMatch) addActor(id, role uint16, team, speed byte, cell battleengine.Cell) bool {
	participant := battleengine.Participant{
		PlayerID: id, RoleID: role, TeamID: team, Source: battleengine.ParticipantHuman,
		BombCapacity: 2, BombPower: 2, SpeedRate: speed,
	}
	if err := match.engine.AddActor(participant, cell); err != nil {
		match.logf("spawn %d: %v", id, err)
		return false
	}
	return true
}

// loadRoutines is action 25 (0x005C6944): the next f9 groups, each a header
// (its f0 the actor, its f9 the step count) and the steps, become routines;
// the records are taken off the task list and routine mode begins.
func (match *tutorialMatch) loadRoutines(record taskRecord) {
	rest := match.tasks[1:]
	used := 0
	for group := int32(0); group < record.Param && used < len(rest); group++ {
		header := rest[used]
		count := int(header.Param)
		used++
		end := min(used+count, len(rest))
		steps := append([]taskRecord(nil), rest[used:end]...)
		used = end
		if len(steps) == 0 {
			continue
		}
		match.routines[match.roleID(header)] = &tutorialRoutine{steps: steps, started: match.engine.ElapsedMS()}
	}
	match.tasks = append([]taskRecord(nil), rest[used:]...)
	match.routineMode = true
	match.state = 3
	match.logf("routine mode: %d routines, %d records left", len(match.routines), len(match.tasks))
}

// runRoutines is routine mode (0x005C5DF2 -> 0x005C7185 per actor).
func (match *tutorialMatch) runRoutines(now uint32) {
	match.input = tutorialInputFree
	ids := make([]uint16, 0, len(match.routines))
	for id := range match.routines {
		ids = append(ids, id)
	}
	sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
	for _, id := range ids {
		routine := match.routines[id]
		actor, ok := match.actor(id)
		if !ok {
			continue
		}
		if actor.State == battleengine.ActorEliminated {
			// Step 0x17: relive after 10 s at the current step's (f3, f4),
			// then the routine starts over (0x005AFB1A).
			died, dead := match.deaths[id]
			if !dead || now-died < tutorialReviveMS {
				continue
			}
			cell := routine.steps[routine.index].from()
			if _, err := match.engine.RespawnActor(id, cell); err != nil {
				match.logf("relive %d: %v", id, err)
				continue
			}
			delete(match.deaths, id)
			routine.index, routine.started = 0, now
			match.logf("actor %d relives at (%d, %d)", id, cell.Col, cell.Row)
			continue
		}
		if actor.State != battleengine.ActorActive {
			continue
		}
		step := routine.steps[routine.index]
		switch step.Action {
		case taskWalk:
			target := step.to()
			if actor.Position.Cell() != target {
				match.walks[id] = target
				continue
			}
			match.advance(id, routine, now)
			// The routine's lose cells: (0, 1) and (0, 13); 13 is outside
			// the 13 rows, so only the top-left one counts (original bug).
			if target.Col == 0 && (target.Row == 1 || target.Row == 13) {
				match.finish(tutorialResultLoss, fmt.Sprintf("actor %d reached (0, %d)", id, target.Row))
				return
			}
		case taskBomb:
			match.bombs[id] = true
			match.advance(id, routine, now)
		case taskPause:
			if now-routine.started >= step.MS {
				match.advance(id, routine, now)
			}
		case taskTeleportStep:
			match.teleport(id, step.to())
			match.advance(id, routine, now)
		case taskLeaveRoutine:
			match.routineMode = false
			return
		default:
			match.advance(id, routine, now)
		}
	}
}

// advance is 0x005AFB86: the next step, or the first after the last.
func (match *tutorialMatch) advance(id uint16, routine *tutorialRoutine, now uint32) {
	delete(match.walks, id)
	routine.index = (routine.index + 1) % len(routine.steps)
	routine.started = now
}

// observe handles the engine's deaths (0x00606606) and pickups (0x00604DB0).
func (match *tutorialMatch) observe(events []battleengine.Event) {
	now := match.engine.ElapsedMS()
	for _, event := range events {
		switch event.Kind {
		case battleengine.EventActorEliminated:
			id := event.TargetID
			if id == 0 {
				id = event.PlayerID
			}
			actor, ok := match.actor(id)
			if !ok {
				continue
			}
			delete(match.walks, id)
			if actor.TeamID == tutorialTeam {
				match.finish(tutorialResultLoss, fmt.Sprintf("teammate %d died", id))
				return
			}
			if !tutorialStage(match.begin.MapID) {
				match.finish(tutorialResultWin, fmt.Sprintf("opponent %d died", id))
				return
			}
			match.deaths[id] = now
			match.logf("actor %d died at %d ms", id, now)
		case battleengine.EventPickupCollected:
			match.pickup(event.SceneID)
		}
	}
}

// pickup is 0x0060528C (1503 only) and 0x005EE60D (the panel's picture).
func (match *tutorialMatch) pickup(scene uint32) {
	match.panelItem = tutorialPanelFrame(scene)
	if match.begin.MapID != tutorialItemMap {
		return
	}
	if len(match.eaten) >= tutorialItemKinds {
		match.done()
		return
	}
	for _, seen := range match.eaten {
		if seen == scene {
			return
		}
	}
	hint := tutorialItemHint(scene)
	if hint == 1 {
		return
	}
	match.eaten = append(match.eaten, scene)
	match.itemCount++
	// Actor local + 1 (1503's teammate) explains it: action 8 in front of
	// the current record, started at once (0x005C7492).
	record := taskRecord{Role: 1, Action: taskActorSay, X1: 8, Y1: 4, X2: 8, Y2: 4, MS: tutorialHintMS, Extra: 3, Param: hint}
	match.tasks = append([]taskRecord{record}, match.tasks...)
	match.state = 1
}

// timers is the rule-9 clock (0x0060D6E5): 1501..1503 are lost after
// 240 s, 1504 goes back to the task list after 70 s, other maps (311) are
// won after 240 s.
func (match *tutorialMatch) timers() {
	if match.timerDone {
		return
	}
	now := match.engine.ElapsedMS()
	switch mapID := match.begin.MapID; {
	case mapID == tutorialDefend:
		if now >= tutorialDefendMS {
			match.timerDone = true
			match.logf("70 s held: back to the task list")
			match.routineMode = false // 0x005C74EC
			match.state, match.started = 1, now
			match.walks = map[uint16]battleengine.Cell{}
		}
	case tutorialStage(mapID):
		if now >= tutorialStageMS {
			match.timerDone = true
			match.finish(tutorialResultLoss, "time is up")
		}
	default:
		if now >= tutorialStageMS {
			match.timerDone = true
			match.finish(tutorialResultWin, "time is up")
		}
	}
}

// finish is a 0xFBB: the game is over with this result byte.
func (match *tutorialMatch) finish(result int, reason string) {
	if match.result != nil {
		return
	}
	match.result = &result
	match.hint = nil
	match.logf("game over on map %d at %d ms: result %d (%s)", match.begin.MapID, match.engine.ElapsedMS(), result, reason)
}

func (match *tutorialMatch) view(events []battleengine.Event) tutorialStateView {
	state := tutorialStateView{stateView: engineView(match.engine, events)}
	if match.shownStage != match.stage {
		match.shownStage = match.stage
		begin := match.begin
		state.Begin = &begin
	}
	tutorial := &tutorialView{
		Stage: match.stage, MapID: match.begin.MapID, Hint: match.hint, Card: match.card, HideUI: match.hideUI,
		Free: match.input == tutorialInputFree && match.forced == nil, PanelItem: match.panelItem, Eaten: match.itemCount % 10,
		Routine: match.routineMode, Result: match.result,
	}
	if len(match.tasks) > 0 {
		tutorial.Action = match.tasks[0].Action
	}
	if match.arrow != nil {
		tutorial.Arrow = &cell{Row: match.arrow.Row, Col: match.arrow.Col}
	}
	if match.kubi != nil {
		tutorial.Kubi = &cell{Row: match.kubi.Row, Col: match.kubi.Col}
	}
	if match.scrollAt != nil {
		elapsed := match.engine.ElapsedMS() - *match.scrollAt
		tutorial.ScrollMS = &elapsed
	}
	state.Tutorial = tutorial
	return state
}
