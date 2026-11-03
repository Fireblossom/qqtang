// Command qqt-battle-sidecar runs one battleengine match for the native
// client (client/native) over JSON lines on stdin/stdout, so the Python
// client uses the same native-faithful rules as the server.
//
// The first line is {"cmd":"init",...}; it is answered with the spawned
// actors and visible pickups. Every following {"cmd":"step",...} advances
// the match by Ticks engine ticks (at least one) and is answered with the
// events of those ticks and the resulting state. Pulses (bomb placement,
// item use) apply to the first tick only; moves are held for all of them.
//
// With "online" in the init command the sidecar instead plays one live match
// against the server (online.go): the engine follows the wall clock and every
// step also exchanges the match's UDP traffic.
//
// Offline practice supports native rule 1 (ordinary bubble battle) maps;
// online matches also play rules 2, 3, 4, 5, 6 and 13 (see ruleOptions).
package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"math/rand"
	"os"
	"strconv"

	"qqtang/internal/game/battleengine"
	"qqtang/internal/game/mapdata"
)

type player struct {
	ID   uint16 `json:"id"`
	Role uint16 `json:"role"`
	Team byte   `json:"team"`
	// AI marks an offline opponent driven by the sidecar's wander policy.
	AI bool `json:"ai"`
	// Props brought from the room (online, item rooms only).
	Prepared []preparedView `json:"prepared,omitempty"`
	// ExtPoint is GAME_BEGIN's adventure courage (the PVE level).
	ExtPoint uint32 `json:"ext_point,omitempty"`
}

type preparedView struct {
	Item  uint32 `json:"item"`
	Count int16  `json:"count"`
}

type command struct {
	Cmd string `json:"cmd"`

	// init
	ClientRoot string      `json:"client_root"`
	MapID      uint32      `json:"map_id"`
	SpawnSeed  uint32      `json:"spawn_seed"`
	ItemSeed   uint32      `json:"item_seed"`
	TickMS     uint32      `json:"tick_ms"`
	TrapMS     uint32      `json:"trap_ms"`
	FreeRule   bool        `json:"free_rule"`
	Players    []player    `json:"players"`
	Online     *onlineInit `json:"online"`

	// step
	Ticks   int      `json:"ticks"`
	Actions []action `json:"actions"`
	// TCP notifications of the match, forwarded by the client (online).
	TCP []tcpPacket `json:"tcp"`
}

type action struct {
	ID   uint16 `json:"id"`
	Move byte   `json:"move"`
	Bomb bool   `json:"bomb"`
	Use  uint8  `json:"use"`
	// A prop brought from the room (item ID 20001..22999) to use.
	Prop uint32 `json:"prop"`
}

type cell struct {
	Row int16 `json:"row"`
	Col int16 `json:"col"`
}

type actorView struct {
	ID          uint16 `json:"id"`
	Team        byte   `json:"team"`
	Role        uint16 `json:"role"`
	AvatarRole  uint16 `json:"avatar_role"`
	X           int32  `json:"x"`
	Y           int32  `json:"y"`
	State       byte   `json:"state"`
	Facing      byte   `json:"facing"`
	Bombs       byte   `json:"bombs"`
	Power       byte   `json:"power"`
	SpeedRate   byte   `json:"speed_rate"`
	TrapExpires uint32 `json:"trap_expires"`
	Protected   uint32 `json:"protected_until"`
	Durability  byte   `json:"durability"`
	// The seven held-item slots as [action ID, count]; empty slots are zero.
	Held [battleengine.NativeBattleActionSlots][2]uint8 `json:"held"`
	// Carried rule objectives (treasure gems 150, 151, 152) by kind.
	Carried [battleengine.MaxObjectiveKinds]uint16 `json:"carried"`
}

type bombView struct {
	Hidden    bool   `json:"hidden,omitempty"`
	ID        uint32 `json:"id"`
	Owner     uint16 `json:"owner"`
	Row       int16  `json:"row"`
	Col       int16  `json:"col"`
	Power     byte   `json:"power"`
	ExplodeAt uint32 `json:"explode_at"`
	Style     byte   `json:"style,omitempty"` // bubble type: 9 for rule 6's held item 48, 2 for rule 2's power bubbles
}

type flameView struct {
	Style   byte   `json:"style,omitempty"`
	Row     int16  `json:"row"`
	Col     int16  `json:"col"`
	Bomb    uint32 `json:"bomb"`
	Impact  uint32 `json:"impact"`
	Expires uint32 `json:"expires"`
}

type pickupView struct {
	Scene uint32 `json:"scene"`
	Row   int16  `json:"row"`
	Col   int16  `json:"col"`
}

type fieldView struct {
	Action uint8 `json:"action"`
	Row    int16 `json:"row"`
	Col    int16 `json:"col"`
}

type eventView struct {
	Kind   battleengine.EventKind `json:"kind"`
	Time   uint32                 `json:"time"`
	Player uint16                 `json:"player"`
	Target uint16                 `json:"target"`
	Bomb   uint32                 `json:"bomb"`
	Cell   cell                   `json:"cell"`
	From   cell                   `json:"from"` // pushed map elements and kicked bombs
	Team   byte                   `json:"team"`
	Scene  uint32                 `json:"scene"`
	// Inclusive blast bounds, only for bomb explosions.
	Blast [4]int16 `json:"blast"`
}

type outcomeView struct {
	Ended    bool   `json:"ended"`
	Draw     bool   `json:"draw"`
	TimedOut bool   `json:"timed_out"`
	Winner   byte   `json:"winner"`
	EndedAt  uint32 `json:"ended_at"`
}

type stateView struct {
	Clock   uint32       `json:"clock"`
	Events  []eventView  `json:"events"`
	Actors  []actorView  `json:"actors"`
	Bombs   []bombView   `json:"bombs"`
	Flames  []flameView  `json:"flames"`
	Pickups []pickupView `json:"pickups"`
	Fields  []fieldView  `json:"fields"`
	Outcome outcomeView  `json:"outcome"`

	// Online mode only.
	Begin *beginView `json:"begin,omitempty"`
	Net   *netView   `json:"net,omitempty"`
	// The local player's room props left, in GAME_BEGIN order.
	Prepared  []preparedView `json:"prepared,omitempty"`
	Bun       *bunView       `json:"bun,omitempty"` // rule 3
	Tank      *tankView      `json:"tank,omitempty"`
	Sculpture *sculptureView `json:"sculpture,omitempty"` // rule 6
	// Kills and kill streaks by player ID, from the kill notices.
	Kills     map[string]int `json:"kills,omitempty"`
	Streaks   map[string]int `json:"streaks,omitempty"`
	TCPOut    []tcpPacket    `json:"tcp_out,omitempty"`
	Kick      *kickView      `json:"kick,omitempty"` // rule 2
	Box       *boxView       `json:"box,omitempty"`
	Boss      *bossView      `json:"boss,omitempty"`
	Adventure *adventureView `json:"adventure,omitempty"`
	Machine   *machineView   `json:"machine,omitempty"`
}

func main() {
	if err := run(os.Stdin, os.Stdout); err != nil {
		_ = json.NewEncoder(os.Stdout).Encode(map[string]string{"error": err.Error()})
		os.Exit(1)
	}
}

func run(input io.Reader, output io.Writer) error {
	lines := bufio.NewScanner(input)
	lines.Buffer(make([]byte, 1<<20), 1<<20)
	out := json.NewEncoder(output)
	if !lines.Scan() {
		return lines.Err()
	}
	var init command
	if err := json.Unmarshal(lines.Bytes(), &init); err != nil {
		return fmt.Errorf("init: %w", err)
	}
	if init.Cmd != "init" {
		return fmt.Errorf("first command is %q, want init", init.Cmd)
	}
	if init.Online != nil {
		return runOnline(init, lines, out)
	}
	runtime, err := newRuntime(init)
	if err != nil {
		return err
	}
	if err := out.Encode(view(runtime, nil)); err != nil {
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
		events, err := advance(runtime, step)
		if err != nil {
			return err
		}
		if err := out.Encode(view(runtime, events)); err != nil {
			return err
		}
	}
	return lines.Err()
}

func runOnline(init command, lines *bufio.Scanner, out *json.Encoder) error {
	match, begin, err := newOnlineMatch(init)
	if err != nil {
		return err
	}
	defer match.close()
	state := view(match.runtime, nil)
	state.Begin, state.Net, state.Prepared = &begin, match.netView(), match.preparedView()
	state.Kills, state.Streaks = byPlayer(match.kills), byPlayer(match.streaks)
	if match.bun != nil {
		state.Bun = match.bun.view()
	}
	if match.tank != nil {
		state.Tank = match.tank.view()
	}
	if match.sculpture != nil {
		state.Sculpture = match.sculpture.view()
		match.markStyleNine(state.Bombs)
	}
	if match.box != nil {
		state.Box = match.boxView()
	}
	if match.machine != nil {
		state.Machine = match.machineView()
	}
	if match.kick != nil {
		state.Kick = match.kick.view()
		match.kick.markStyles(state.Bombs)
	}
	state.Boss = match.bossView()
	state.Adventure = match.adventureView()
	if err := out.Encode(state); err != nil {
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
		state := view(match.runtime, events)
		state.Net, state.TCPOut, state.Prepared = match.netView(), match.takeTCP(), match.preparedView()
		state.Kills, state.Streaks = byPlayer(match.kills), byPlayer(match.streaks)
		if match.bun != nil {
			state.Bun = match.bun.view()
		}
		if match.tank != nil {
			state.Tank = match.tank.view()
		}
		if match.sculpture != nil {
			state.Sculpture = match.sculpture.view()
			match.markStyleNine(state.Bombs)
		}
		if match.box != nil {
			state.Box = match.boxView()
		}
		if match.machine != nil {
			state.Machine = match.machineView()
		}
		if match.kick != nil {
			state.Kick = match.kick.view()
			match.kick.markStyles(state.Bombs)
		}
		state.Boss = match.bossView()
		state.Adventure = match.adventureView()
		if err := out.Encode(state); err != nil {
			return err
		}
	}
	return lines.Err()
}

func newRuntime(init command) (*battleengine.Runtime, error) {
	catalog, err := mapdata.LoadCatalog(init.ClientRoot)
	if err != nil {
		return nil, err
	}
	entry, ok := catalog.CompetitiveMap(init.MapID)
	if !ok {
		return nil, fmt.Errorf("map %d is not a competitive map", init.MapID)
	}
	participants := make([]battleengine.NativeRuntimeParticipant, len(init.Players))
	policies := map[uint16]battleengine.Policy{}
	random := rand.New(rand.NewSource(int64(init.SpawnSeed)<<32 | int64(init.ItemSeed)))
	for index, entry := range init.Players {
		participants[index] = battleengine.NativeRuntimeParticipant{
			PlayerID: entry.ID, RoleID: entry.Role, TeamID: entry.Team, Source: battleengine.ParticipantHuman,
		}
		if entry.AI {
			participants[index].Source = battleengine.ParticipantVirtualAI
			policies[entry.ID] = wanderPolicy(random)
		}
	}
	tick := init.TickMS
	if tick == 0 {
		tick = 20
	}
	return battleengine.NewRuntimeFromCompetitiveMap(entry, battleengine.CompetitiveRuntimeOptions{
		SpawnSeed: init.SpawnSeed, ItemSeed: init.ItemSeed,
		SpawnMode: battleengine.NativeSpawnModeForMap(entry, init.FreeRule),
		TickMS:    tick, TrapDurationMS: init.TrapMS,
		Participants: participants, Policies: policies,
	})
}

// wanderPolicy keeps walking one way for a while, turns at random and now
// and then drops a bubble. It only gives offline practice something to do.
func wanderPolicy(random *rand.Rand) battleengine.Policy {
	var current battleengine.Action
	remaining := 0
	return battleengine.PolicyFunc(func(_ battleengine.Observation, legal []battleengine.Action) (battleengine.Action, error) {
		if len(legal) == 0 {
			return battleengine.Action{}, nil
		}
		stillLegal := false
		for _, candidate := range legal {
			if candidate.Move == current.Move && !candidate.PlaceBomb && candidate.UseActionID == 0 {
				stillLegal = true
				break
			}
		}
		if remaining <= 0 || !stillLegal {
			moves := make([]battleengine.Action, 0, len(legal))
			for _, candidate := range legal {
				if !candidate.PlaceBomb && candidate.UseActionID == 0 {
					moves = append(moves, candidate)
				}
			}
			if len(moves) == 0 {
				moves = legal
			}
			current = moves[random.Intn(len(moves))]
			remaining = 10 + random.Intn(40)
		}
		remaining--
		if random.Intn(150) == 0 {
			for _, candidate := range legal {
				if candidate.PlaceBomb {
					return candidate, nil
				}
			}
		}
		return current, nil
	})
}

func advance(runtime *battleengine.Runtime, step command) ([]battleengine.Event, error) {
	ticks := step.Ticks
	if ticks < 1 {
		ticks = 1
	}
	var events []battleengine.Event
	for tick := 0; tick < ticks; tick++ {
		actions := make([]battleengine.Action, 0, len(step.Actions))
		for _, entry := range step.Actions {
			next := battleengine.Action{PlayerID: entry.ID, Move: battleengine.Direction(entry.Move)}
			if tick == 0 {
				next.PlaceBomb, next.UseActionID = entry.Bomb, entry.Use
			}
			actions = append(actions, next)
		}
		stepEvents, err := runtime.Step(actions)
		if err != nil {
			return nil, err
		}
		events = append(events, stepEvents...)
		if runtime.EngineSnapshot().Terminal().Ended {
			break
		}
	}
	return events, nil
}

func view(runtime *battleengine.Runtime, events []battleengine.Event) stateView {
	engine := runtime.EngineSnapshot()
	state := stateView{Clock: engine.ElapsedMS(), Events: make([]eventView, 0, len(events))}
	for _, event := range events {
		state.Events = append(state.Events, eventView{
			Kind: event.Kind, Time: event.TimeMS, Player: event.PlayerID, Target: event.TargetID, Bomb: event.BombID,
			Cell: cell{event.Cell.Row, event.Cell.Col}, From: cell{event.FromCell.Row, event.FromCell.Col},
			Team: event.TeamID, Scene: event.SceneID,
			Blast: [4]int16{event.BlastRowMin, event.BlastRowMax, event.BlastColMin, event.BlastColMax},
		})
	}
	for _, actor := range engine.Actors() {
		next := actorView{
			ID: actor.PlayerID, Team: actor.TeamID, Role: actor.RoleID, AvatarRole: actor.AvatarRoleID,
			X: actor.Position.X, Y: actor.Position.Y, State: byte(actor.State), Facing: byte(actor.Facing),
			Bombs: actor.BombCapacity, Power: actor.BombPower, SpeedRate: actor.SpeedRate,
			TrapExpires: actor.TrapExpiresAt, Protected: actor.HarmProtectionExpiresAt, Durability: actor.DurabilityLeft,
			Carried: actor.CarriedObjectives,
		}
		for index, slot := range actor.HeldActions {
			next.Held[index] = [2]uint8{slot.ActionID, slot.Count}
		}
		state.Actors = append(state.Actors, next)
	}
	for _, bomb := range engine.Bombs() {
		state.Bombs = append(state.Bombs, bombView{
			ID: bomb.ID, Owner: bomb.OwnerID, Row: bomb.Cell.Row, Col: bomb.Cell.Col, Power: bomb.Power, ExplodeAt: bomb.ExplodeAtMS, Style: bomb.NativeStyle, Hidden: bomb.Invisible,
		})
	}
	for _, flame := range engine.Flames() {
		state.Flames = append(state.Flames, flameView{
			Row: flame.Cell.Row, Col: flame.Cell.Col, Bomb: flame.BombID, Impact: flame.ImpactAtMS, Expires: flame.ExpiresAtMS, Style: flame.NativeStyle,
		})
	}
	for _, pickup := range engine.Pickups() {
		if pickup.State == battleengine.PickupAvailable {
			state.Pickups = append(state.Pickups, pickupView{Scene: pickup.SceneID, Row: pickup.Cell.Row, Col: pickup.Cell.Col})
		}
	}
	for _, object := range engine.FieldObjects() {
		state.Fields = append(state.Fields, fieldView{Action: object.ActionID, Row: object.Cell.Row, Col: object.Cell.Col})
	}
	outcome := engine.Terminal()
	state.Outcome = outcomeView{outcome.Ended, outcome.Draw, outcome.TimedOut, outcome.WinnerTeamID, outcome.EndedAtMS}
	return state
}

func byPlayer(values map[uint16]int) map[string]int {
	result := make(map[string]int, len(values))
	for playerID, value := range values {
		result[strconv.Itoa(int(playerID))] = value
	}
	return result
}
