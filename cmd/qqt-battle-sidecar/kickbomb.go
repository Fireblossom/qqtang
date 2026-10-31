package main

// Rule 2, 踢炸弹 (kick bomb). Client.exe's rule object (vtable 0x007A44FC)
// differs from rule 1 in a few slots: players cannot place bubbles (slot 3,
// 0x0060C0E0), the corner slide tolerance is 2 pixels (slot 11), the opening
// skips the wall items (slot 1 does not call 0x005B6441), and its component
// list (0x0060BD76) drops the bubble and push producers and adds the airdrop
// (0x0061988B). The round is decided by elimination, as in rule 1.
//
// Everyone kicks the bubble they walk into. The kick lands eight cells ahead,
// modulo the field, or the first cell back from there without a map element
// (0x005B438D); all 216 kick messages of Tutorial3.qbv agree. The
// arbitrator answers a request (0x0FB4) whose cell holds exactly one bubble
// by echoing it as 0x139C (0x006040CF); every client then flies the bubble
// there at 400 px/s (0x005FD24C, 0x005D3C1D).
//
// Only the arbitrator drops bubbles. Its airdrop component checks every frame
// (0x005E17C5): from 5000 ms on, a batch is due once the scene clock is at
// least max(4000, 9000 - clock/8) past the last batch, which starts at -10000
// (0x005E0B38). A batch (0x0061988B) holds min(15, clock/16000 + 5) bubbles of
// scene object 1 with wire power 2, min(10, clock/16000 + 1) of object 2 with
// power 5, and one item while the item pool lasts (0x005E19FB), all on
// distinct random free cells (0x005B8753). The item is drawn by weight from
// the pool 0x005E1AAD sets up, the last entry (81) only from 10 s on
// (0x005E18E3). From 120 s on, one batch in 200 takes one of eight fixed
// formations instead (0x005E1A2B), but the producer then leaves both counts at
// zero, so the batch arrives empty; that is kept. Also from 120 s on, a few
// batches would add 2x2 bubbles (object 11, 0x005E1856); those are not made.
// Each client applies a batch (0x006018A7) by flying every bubble in from a
// random end of its row and dropping every item from the top of its column.

import (
	"math/rand"
	"strconv"

	"qqtang/internal/game/battleengine"
	"qqtang/internal/game/mapdata"
	"qqtang/internal/protocol/game"
)

const (
	kickBatchFirstMS            = 5000
	kickBatchLastStart          = -10_000
	kickFormationsFromMS        = 120_000
	kickFormations              = 8
	kickLateItemFromMS          = 10_000
	kickNormalScene             = 1
	kickNormalWirePower         = 2
	kickPowerScene              = 2
	kickPowerWirePower          = 5
	kickFieldRows               = 13
	kickFieldCols               = 15
	kickMaxNormal, kickMaxPower = 15, 10
)

// kickItem is one entry of the airdrop's item pool (0x005E173E).
type kickItem struct {
	scene  uint32
	weight int
	left   int
}

// kickFlight is a bubble in the air, for drawing: it left From at Start and
// moves Cells cells in Direction (wire order right, up, left, down), across
// the field's edge if need be.
type kickFlight struct {
	From      cell   `json:"from"`
	Direction byte   `json:"dir"`
	Cells     int16  `json:"cells"`
	Start     uint32 `json:"start"`
	Until     uint32 `json:"until"`
}

type kickFall struct {
	Scene uint32 `json:"scene"`
	Row   int16  `json:"row"`
	Col   int16  `json:"col"`
	Start uint32 `json:"start"`
	Until uint32 `json:"until"`
}

type kickRule struct {
	rng       *rand.Rand
	pool      []kickItem
	lastBatch int64
	scenes    map[uint32]uint32 // engine bubble ID: scene object (1 or 2)
	flights   map[uint32]kickFlight
	falls     []kickFall
}

// newKickRule sets the item pool up as 0x005E1AAD does; maps 703 and 704 also
// drop darts (27).
func newKickRule(entry mapdata.CompetitiveMap, seed int64) *kickRule {
	rule := &kickRule{rng: rand.New(rand.NewSource(seed)), lastBatch: kickBatchLastStart, scenes: map[uint32]uint32{}, flights: map[uint32]kickFlight{}}
	rule.pool = []kickItem{{3, 5, 50}, {23, 2, 50}, {25, 2, 50}, {24, 1, 4}}
	if entry.ID == 703 || entry.ID == 704 {
		rule.pool = append(rule.pool, kickItem{27, 1, 4})
	}
	rule.pool = append(rule.pool, kickItem{81, 2, 50})
	return rule
}

// applied records the arbitrator's own batch. The receiver (0x006018A7) times
// the last batch when it runs; the arbitrator's own copy goes through the
// scene's message object (+0x4EA8, VM-protected) and runs within a frame.
// Timing it in the sending tick keeps Tutorial3.qbv's batches within 50 ms.
func (rule *kickRule) applied(now uint32) {
	rule.lastBatch = int64(now)
}

// due is 0x005E17C5.
func (rule *kickRule) due(now uint32) bool {
	if now < kickBatchFirstMS {
		return false
	}
	interval := max(int64(4000), 9000-int64(now>>3))
	return int64(now)-rule.lastBatch >= interval
}

func (rule *kickRule) poolLeft() bool {
	total := 0
	for _, item := range rule.pool {
		total += item.left
	}
	return total > 0
}

// drawItem is 0x005E18E3.
func (rule *kickRule) drawItem(now uint32) uint32 {
	if len(rule.pool) == 0 {
		return 3
	}
	candidates := rule.pool
	if now < kickLateItemFromMS {
		candidates = candidates[:len(candidates)-1]
	}
	total := 0
	for _, item := range candidates {
		if item.weight > 0 && item.left > 0 {
			total += item.weight
		}
	}
	if total > 0 {
		pick := rule.rng.Intn(total)
		for index := range candidates {
			item := &rule.pool[index]
			if item.weight <= 0 || item.left <= 0 {
				continue
			}
			if pick < item.weight {
				item.left--
				return item.scene
			}
			pick -= item.weight
		}
	}
	return rule.pool[rule.rng.Intn(len(rule.pool))].scene
}

// batch builds the arbitrator's next 0x10E1 body (without player and time).
func (rule *kickRule) batch(engine *battleengine.Engine, now uint32) game.DispatchBombData {
	var data game.DispatchBombData
	if now >= kickFormationsFromMS && rule.rng.Intn(kickFormations*200) < kickFormations {
		return data
	}
	normal := min(kickMaxNormal, int(now/16000)+5)
	power := min(kickMaxPower, int(now/16000)+1)
	items := 0
	if rule.poolLeft() {
		items = 1
	}
	cells := rule.freeCells(engine, normal+power+items)
	for index, free := range cells {
		row, col := byte(min(free.Row, kickFieldRows-1)), byte(min(free.Col, kickFieldCols-1))
		switch {
		case index < normal:
			data.Bombs = append(data.Bombs, game.DispatchBomb{SceneID: kickNormalScene, Prop: kickNormalWirePower, Row: row, Col: col})
		case index < normal+power:
			data.Bombs = append(data.Bombs, game.DispatchBomb{SceneID: kickPowerScene, Prop: kickPowerWirePower, Row: row, Col: col})
		default:
			data.Items = append(data.Items, game.DispatchBombItem{ItemID: rule.drawItem(now), Row: row, Col: col})
		}
	}
	return data
}

// freeCells picks count distinct random cells with no map element, bubble,
// item or placed field object (0x005B8753 checks the two map-element layers,
// the bubble layer and two object layers). Players do not block a cell.
func (rule *kickRule) freeCells(engine *battleengine.Engine, count int) []battleengine.Cell {
	taken := map[battleengine.Cell]bool{}
	for _, bomb := range engine.Bombs() {
		taken[bomb.Cell] = true
	}
	for _, pickup := range engine.Pickups() {
		if pickup.State == battleengine.PickupAvailable {
			taken[pickup.Cell] = true
		}
	}
	for _, object := range engine.FieldObjects() {
		taken[object.Cell] = true
	}
	for _, fall := range rule.falls {
		taken[battleengine.Cell{Row: fall.Row, Col: fall.Col}] = true
	}
	grid := engine.Grid()
	var free []battleengine.Cell
	for row := int16(0); row < int16(grid.Height); row++ {
		for col := int16(0); col < int16(grid.Width); col++ {
			candidate := battleengine.Cell{Row: row, Col: col}
			tile, _ := grid.Cell(candidate)
			if tile.Kind == battleengine.CellOpen && !tile.MapElementOccupied && !taken[candidate] {
				free = append(free, candidate)
			}
		}
	}
	chosen := make([]battleengine.Cell, 0, min(count, len(free)))
	for len(chosen) < count && len(free) > 0 {
		index := rule.rng.Intn(len(free))
		chosen = append(chosen, free[index])
		free[index] = free[len(free)-1]
		free = free[:len(free)-1]
	}
	return chosen
}

// apply drops a batch into the engine as 0x006018A7 does; each bubble flies in
// from a random end of its row, chosen by this client.
func (rule *kickRule) apply(engine *battleengine.Engine, data game.DispatchBombData) ([]battleengine.Event, error) {
	bombs := make([]battleengine.RuleBomb, 0, len(data.Bombs))
	for _, bomb := range data.Bombs {
		from := int16(0)
		if rule.rng.Intn(2) == 0 {
			from = kickFieldCols - 1
		}
		bombs = append(bombs, battleengine.RuleBomb{Cell: battleengine.Cell{Row: int16(bomb.Row), Col: int16(bomb.Col)}, Power: max(bomb.Prop, 2) - 1, FromCol: from})
	}
	items := make([]battleengine.Pickup, 0, len(data.Items))
	for _, item := range data.Items {
		items = append(items, battleengine.Pickup{SceneID: item.ItemID, Cell: battleengine.Cell{Row: int16(item.Row), Col: int16(item.Col)}})
	}
	events, err := engine.DropRuleBatch(data.PlayerID, data.Time, bombs, items)
	if err != nil {
		return nil, err
	}
	index := 0
	for _, event := range events {
		switch event.Kind {
		case battleengine.EventBombPlaced:
			drop, wire := bombs[index], data.Bombs[index]
			index++
			rule.scenes[event.BombID] = wire.SceneID
			if distance := drop.Cell.Col - drop.FromCol; distance != 0 {
				direction := byte(0)
				if distance < 0 {
					direction, distance = 2, -distance
				}
				rule.flights[event.BombID] = kickFlight{From: cell{Row: drop.Cell.Row, Col: drop.FromCol}, Direction: direction, Cells: distance,
					Start: data.Time, Until: data.Time + uint32(distance)*battleengine.NativeBombFlightMSPerCell}
			}
		case battleengine.EventPickupDispatched:
			rule.falls = append(rule.falls, kickFall{Scene: event.SceneID, Row: event.Cell.Row, Col: event.Cell.Col,
				Start: data.Time, Until: data.Time + uint32(max(event.Cell.Row, 0))*battleengine.NativeBombFlightMSPerCell})
		}
	}
	return events, nil
}

// kicked records a kick's flight for drawing.
func (rule *kickRule) kicked(engine *battleengine.Engine, event battleengine.Event, direction byte) {
	bomb, found := bombByID(engine.Bombs(), event.BombID)
	if !found || bomb.FlightUntilMS <= event.TimeMS {
		return
	}
	rule.flights[event.BombID] = kickFlight{From: cell{Row: event.FromCell.Row, Col: event.FromCell.Col}, Direction: direction,
		Cells: int16((bomb.FlightUntilMS - event.TimeMS) / battleengine.NativeBombFlightMSPerCell), Start: event.TimeMS, Until: bomb.FlightUntilMS}
}

// forget drops what has landed or gone.
func (rule *kickRule) forget(engine *battleengine.Engine) {
	now := engine.ElapsedMS()
	live := map[uint32]bool{}
	for _, bomb := range engine.Bombs() {
		live[bomb.ID] = true
	}
	for id, flight := range rule.flights {
		if flight.Until <= now || !live[id] {
			delete(rule.flights, id)
		}
	}
	for id := range rule.scenes {
		if !live[id] {
			delete(rule.scenes, id)
		}
	}
	kept := rule.falls[:0]
	for _, fall := range rule.falls {
		if fall.Until > now {
			kept = append(kept, fall)
		}
	}
	rule.falls = kept
}

// kickView is the rule state the client draws.
type kickView struct {
	Scenes  map[string]uint32     `json:"scenes"` // by bubble ID
	Flights map[string]kickFlight `json:"flights"`
	Falls   []kickFall            `json:"falls"`
}

func (rule *kickRule) view() *kickView {
	view := &kickView{Scenes: map[string]uint32{}, Flights: map[string]kickFlight{}, Falls: append([]kickFall{}, rule.falls...)}
	for id, scene := range rule.scenes {
		view.Scenes[strconv.FormatUint(uint64(id), 10)] = scene
	}
	for id, flight := range rule.flights {
		view.Flights[strconv.FormatUint(uint64(id), 10)] = flight
	}
	return view
}
