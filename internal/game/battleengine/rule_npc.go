package battleengine

import "fmt"

// RegisterRuleNPCOwner reserves a native rule-8 entity identity, or a Boss
// overlay object, without adding it to human participants, team outcomes or
// policy observations. Identities remain known after death so already-created
// bubbles can finish; the rule controller owns the active/dead lifecycle.
func (engine *Engine) RegisterRuleNPCOwner(id uint16) error {
	boxNPC := engine != nil && engine.rules.NativeBoxPhysics && id >= 20001 && id < 23000
	boss := engine != nil && (engine.rules.NativeBossNPC && id >= 30001 && id <= 30008 || engine.rules.NativeAdventure && id >= 30300 && id < 30400)
	if !boxNPC && !boss {
		return fmt.Errorf("rule NPC owner %d is not permitted", id)
	}
	if engine.ruleNPCOwners == nil {
		engine.ruleNPCOwners = map[uint16]struct{}{}
	}
	engine.ruleNPCOwners[id] = struct{}{}
	return nil
}

func (engine *Engine) HasRuleNPCOwner(id uint16) bool {
	if engine == nil {
		return false
	}
	_, ok := engine.ruleNPCOwners[id]
	return ok
}

// MoveRuleNPC resolves one default NPC displacement through the same native
// directional/corner geometry as participants, without inserting a participant
// or applying player interactions. Transformations and active wall passage are
// owned separately by the scene controller and are not implied by this API.
func (engine *Engine) MoveRuleNPC(owner uint16, position Position, direction Direction, distance int) (Position, error) {
	if !engine.HasRuleNPCOwner(owner) {
		return position, fmt.Errorf("unregistered NPC movement owner %d", owner)
	}
	actor := Actor{Participant: Participant{PlayerID: owner, TeamID: 7, Source: ParticipantVirtualAI},
		Position: position, Facing: direction, State: ActorActive, nativePreviousPosition: position}
	engine.resolveNativeMovementDisplacement(&actor, direction, min(distance, int(NativeMaxMovementPixelsPerUpdate)), true)
	return actor.Position, nil
}

// RuleNPCPositionPassable is the default-capability 005B7999 predicate used
// by the scene's path follower. It does not commit movement or traversal state.
func (engine *Engine) RuleNPCPositionPassable(owner uint16, position Position, direction Direction) bool {
	if !engine.HasRuleNPCOwner(owner) {
		return false
	}
	actor := Actor{Participant: Participant{PlayerID: owner, TeamID: 7, Source: ParticipantVirtualAI},
		Position: position, Facing: direction, State: ActorActive}
	return engine.positionWalkable(&actor, position, direction)
}

func (engine *Engine) ruleNPCCornerBlocked(actor *Actor, cell Cell) bool {
	if actor == nil || !engine.rules.NativeBoxPhysics || !engine.HasRuleNPCOwner(actor.PlayerID) {
		return false
	}
	return ((cell.Row == 0 || cell.Row == 12) && (cell.Col == 0 || cell.Col == 1 || cell.Col == 13 || cell.Col == 14)) ||
		((cell.Row == 1 || cell.Row == 11) && (cell.Col == 0 || cell.Col == 14))
}

// PlaceRuleNPCBomb is an explicit scene-controller operation. Personal
// capacity and native AI safety checks belong to that controller; these
// bubbles never consume a human's placement allowance.
func (engine *Engine) PlaceRuleNPCBomb(owner uint16, cell Cell, wirePower uint16, property byte, at uint32) (Event, error) {
	return engine.placeRuleNPCBomb(owner, cell, wirePower, property, 1, at)
}

// NativeStyleBigBubble is the 2x2 bubble (type 11): it occupies and blocks
// only its anchor, the bottom-right cell, explodes as an ordinary cross and
// is drawn over the whole block; type 12 is its three companions in rule 2's
// airdrop (docs/bubble-2x2-re.md).
const (
	NativeStyleBigBubble     byte = 11
	NativeStyleBigBubblePart byte = 12
)

// PlaceRuleNPCBigBomb places a Boss's type-11 bubble (skill 12, 0x005FDF84).
func (engine *Engine) PlaceRuleNPCBigBomb(owner uint16, cell Cell, wirePower uint16, at uint32) (Event, error) {
	return engine.placeRuleNPCBomb(owner, cell, wirePower, 0, NativeStyleBigBubble, at)
}

func (engine *Engine) placeRuleNPCBomb(owner uint16, cell Cell, wirePower uint16, property, style byte, at uint32) (Event, error) {
	if !engine.HasRuleNPCOwner(owner) {
		return Event{}, fmt.Errorf("unregistered rule NPC owner %d", owner)
	}
	if _, inside := engine.grid.Cell(cell); !inside || wirePower < 1 || wirePower > 16 {
		return Event{}, fmt.Errorf("invalid rule NPC bomb at %v with power %d", cell, wirePower)
	}
	bomb := Bomb{ID: engine.nextBombID, OwnerID: owner, Cell: cell, Power: byte(wirePower - 1),
		RuleOwned: true, NativeStyle: style, ExplodeAtMS: saturatingAdd(at, NativeBombFuseMS+1), SceneFourEffect: property != 0}
	return engine.commitBomb(bomb), nil
}

// CollectRuleNPCPickup removes a concrete visible object for the independent
// rule controller. It does not create a human actor or apply human inventory
// effects; the authenticated rule controller owns those NPC attributes.
func (engine *Engine) CollectRuleNPCPickup(owner uint16, scene uint32, cell Cell) (bool, error) {
	if !engine.HasRuleNPCOwner(owner) {
		return false, fmt.Errorf("unregistered NPC pickup owner %d", owner)
	}
	for i := range engine.pickups {
		p := &engine.pickups[i]
		if p.Cell == cell && p.SceneID == scene && p.State == PickupAvailable {
			p.State = PickupCollected
			return true, nil
		}
	}
	return false, nil
}

// InstallRuleNPCDrops consumes an already selected native death-drop table.
// Only registered scene entities can use this explicit non-participant path.
func (engine *Engine) InstallRuleNPCDrops(owner uint16, drops []Pickup) ([]Event, error) {
	if !engine.HasRuleNPCOwner(owner) {
		return nil, fmt.Errorf("unregistered NPC drop owner %d", owner)
	}
	if err := engine.validateVerifiedDeathDrops(drops); err != nil {
		return nil, err
	}
	return engine.installDeathDrops(owner, drops), nil
}

// AddBossRewardScene admits one BOSS_INFO reward item as a rule pickup, so
// that the Boss's hit and death drops can be installed and collected.
func (engine *Engine) AddBossRewardScene(sceneID uint32) error {
	if engine == nil || !engine.rules.NativeBossNPC || sceneID == 0 {
		return fmt.Errorf("Boss reward scene %d is not permitted", sceneID)
	}
	if engine.rules.pickupSupported(sceneID) {
		return nil
	}
	for i, id := range engine.rules.BossRewardSceneIDs {
		if id == 0 {
			engine.rules.BossRewardSceneIDs[i] = sceneID
			return nil
		}
	}
	return fmt.Errorf("too many Boss reward scenes")
}

// InstallRuleNPCFieldObjects places the scene objects of a Boss skill
// (NOTIFY_NPCUSE_SKILL items: 43 slow glue, 41 trap) once they have landed.
// They behave like the native field actions of the same number; Boss IDs are
// not actors, so they are never caught by them (0x005CDBF8 excludes them).
func (engine *Engine) InstallRuleNPCFieldObjects(owner uint16, actionID uint8, cells []Cell) error {
	if !engine.HasRuleNPCOwner(owner) {
		return fmt.Errorf("unregistered rule NPC field owner %d", owner)
	}
	if actionID != 41 && actionID != 42 && actionID != 43 {
		return fmt.Errorf("rule NPC field action %d is not a native field object", actionID)
	}
	for _, cell := range cells {
		if _, inside := engine.grid.Cell(cell); !inside {
			return fmt.Errorf("rule NPC field cell %d,%d is outside the grid", cell.Row, cell.Col)
		}
	}
	for _, cell := range cells {
		engine.installAuthoritativeFieldObject(actionID, cell)
	}
	return nil
}
