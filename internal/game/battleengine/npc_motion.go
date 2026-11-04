package battleengine

// NativeNPCPosition retains the float32 position used by Client.exe. Integer
// Position remains the public collision-query and wire coordinate format.
type NativeNPCPosition struct{ X, Y float32 }

func (p NativeNPCPosition) Pixels() Position { return Position{int32(p.X), int32(p.Y)} }

// nativeNPCDisplacement is the single-axis default-capability path through
// 005CFC10. The point predicate receives truncated pixels, as in the original.
// GFX creates D3D8 without FPU_PRESERVE; explicit conversions retain each
// single-precision product and prevent fused operations changing rounding.
func nativeNPCDisplacement(p NativeNPCPosition, direction Direction, speed float32, dt uint32, width, height int,
	can func(Position, Direction) bool, exit func(Cell, Direction) bool, tolerance int32) NativeNPCPosition {
	if direction == DirectionNone || speed <= 0 || dt == 0 {
		return p
	}
	if !exit(p.Pixels().Cell(), direction) {
		return p
	}
	distance := min(float32(40), float32(float32(float32(dt)*speed)*float32(.04)))
	vertical := direction == DirectionUp || direction == DirectionDown
	negative := direction == DirectionUp || direction == DirectionLeft
	a, b, limit := p.X, p.Y, float32(width*40)
	if vertical {
		a, b, limit = p.Y, p.X, float32(height*40)
	}
	sign := float32(1)
	if negative {
		sign = -1
	}
	cell, crossCell := int32(a)/40, int32(b)/40
	center, crossCenter := float32(cell*40+20), float32(crossCell*40+20)
	point := func(a, b float32) Position {
		if vertical {
			return Position{int32(b), int32(a)}
		}
		return Position{int32(a), int32(b)}
	}
	result := func(a, b float32) NativeNPCPosition {
		if vertical {
			return NativeNPCPosition{b, a}
		}
		return NativeNPCPosition{a, b}
	}
	next := a + sign*distance
	if negative {
		next = max(float32(20), next)
	} else {
		next = min(limit, next)
	}
	if int32(next)/40 == cell && int32(next+sign*19)/40 == cell || can(point(next, b), direction) {
		return result(next, b)
	}
	// The native comparison deliberately uses the truncated current pixel.
	if int32(center) != int32(a) {
		target := center
		if !negative && center <= a {
			target += 40
		}
		if negative && a < center {
			target -= 40
		}
		if can(point(target, b), direction) {
			crosses := !negative && target < next || negative && next < target
			if vertical && negative {
				crosses = crosses && target < a
			}
			if crosses {
				return result(target, b)
			}
			return result(next, b)
		}
	}
	frontCenter := center + sign*40
	target := crossCenter
	if !can(point(frontCenter, crossCenter), direction) {
		remainder := int32(b) % 40
		crossDirection := DirectionNone
		if remainder >= 40-tolerance {
			target += 40
			crossDirection = DirectionDown
			if vertical {
				crossDirection = DirectionRight
			}
		} else if remainder <= tolerance {
			target -= 40
			crossDirection = DirectionUp
			if vertical {
				crossDirection = DirectionLeft
			}
		} else {
			return p
		}
		if !can(point(center, target), crossDirection) {
			return p
		}
		diagonalDirection := direction
		// 005CFEE*'s upper-left diagonal check passes Right, not Left.
		if direction == DirectionLeft && crossDirection == DirectionUp {
			diagonalDirection = DirectionRight
		}
		if !can(point(frontCenter, target), diagonalDirection) {
			return p
		}
	}
	remaining := float32(abs32NPC(int32(target - b)))
	if remaining < distance {
		return result(a, target)
	}
	if target <= b {
		distance = -distance
	}
	return result(a, b+distance)
}

func abs32NPC(v int32) int32 {
	if v < 0 {
		return -v
	}
	return v
}

// MoveRuleNPCFloat runs default NPC geometry without inserting a participant.
func (engine *Engine) MoveRuleNPCFloat(owner uint16, position NativeNPCPosition, direction Direction, speed float32, dt uint32) NativeNPCPosition {
	if !engine.HasRuleNPCOwner(owner) {
		return position
	}
	return nativeNPCDisplacement(position, direction, speed, dt, int(engine.grid.Width), int(engine.grid.Height),
		func(p Position, d Direction) bool { return engine.RuleNPCPositionPassable(owner, p, d) },
		func(cell Cell, d Direction) bool {
			tile, ok := engine.grid.Cell(cell)
			return ok && (!tile.NativeGridAttrSet || tile.PlayerExitPassable(d))
		}, engine.rules.cornerSlideTolerance())
}

// ProjectRuleNPCMovement is 005CF9B8's 25ms disposable-position forecast.
// Integer equality terminates it even if a subpixel update could still move.
func (engine *Engine) ProjectRuleNPCMovement(owner uint16, position NativeNPCPosition, direction Direction, speed float32) NativeMovementProjection {
	return nativeNPCForecast(position, direction, func(p NativeNPCPosition) NativeNPCPosition {
		return engine.MoveRuleNPCFloat(owner, p, direction, speed, 25)
	}, (int(engine.grid.Width)+int(engine.grid.Height))*40+1)
}

func nativeNPCForecast(position NativeNPCPosition, direction Direction, step func(NativeNPCPosition) NativeNPCPosition, limit int) NativeMovementProjection {
	result := NativeMovementProjection{End: position.Pixels()}
	if direction == DirectionNone {
		return result
	}
	for count := 0; count < limit; count++ {
		next := step(position)
		before, after := position.Pixels(), next.Pixels()
		if !result.HasCorner && ((direction == DirectionLeft || direction == DirectionRight) && before.Y != after.Y ||
			(direction == DirectionUp || direction == DirectionDown) && before.X != after.X) {
			result.Corner, result.HasCorner = before, true
		}
		if before == after {
			result.End = after
			break
		}
		position, result.End = next, after
	}
	coordinate := &result.End.Y
	if direction == DirectionRight || direction == DirectionLeft {
		coordinate = &result.End.X
	}
	switch *coordinate % 10 {
	case 9:
		*coordinate++
	case 1:
		*coordinate--
	}
	return result
}
