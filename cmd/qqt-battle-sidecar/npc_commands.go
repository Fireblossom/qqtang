package main

// npcMoveCommand is the scene command's byte at +8, not a per-NPC timer.
type npcMoveCommand struct{ counter int8 }

func (command *npcMoveCommand) tick(rule byte, elapsed uint32, actorMode, movementMode byte, decide func() byte) (byte, bool) {
	if elapsed <= 2999 || actorMode != 1 && actorMode != 11 || movementMode == 7 || movementMode == 8 {
		return 0, false
	}
	threshold := int8(5)
	if rule == 8 {
		threshold = 10
	}
	if command.counter <= threshold {
		command.counter++
		return 0, false
	}
	direction := decide()
	if direction != 9 {
		command.counter = 0
	}
	return direction, true
}

// Rule1 registers movement before the ordinary bubble command. Bubble
// planning has no access to the scene movement command's counter.
func (command *npcMoveCommand) generalFrame(elapsed uint32, actorMode, movementMode byte, decide func() byte, apply func(byte), bubble func()) {
	if direction, emitted := command.tick(1, elapsed, actorMode, movementMode, decide); emitted {
		apply(direction)
	}
	bubble()
}
