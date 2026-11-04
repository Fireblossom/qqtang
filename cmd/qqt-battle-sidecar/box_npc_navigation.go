package main

import "qqtang/internal/game/battleengine"

func (match *onlineMatch) chooseBoxNPCDirection(n *boxNPCActor, now, dt uint32) byte {
	position := npcFollowPosition{float32(n.pos.X), float32(n.pos.Y)}
	var mask uint32
	for direction := byte(0); direction < 4; direction++ {
		distance := max(1, int((uint32(bossPixelsPerSecond(n.speed))*dt+999)/1000))
		projected, err := match.engine.MoveRuleNPC(n.id, n.pos, battleDirection(direction), distance)
		if err == nil && projected != n.pos {
			mask |= 1 << (3 - direction)
		}
	}
	_, danger := n.danger.ImpactAt(n.pos.Cell())
	canPass := func(x, y int32, facing byte) bool {
		return match.engine.RuleNPCPositionPassable(n.id, battleengine.Position{X: x, Y: y}, battleDirection(facing))
	}
	direction := n.navigator.choose(npcNavigationFrame{
		position: &position, facing: n.facing, standing: !n.navigator.walking,
		blocked: n.moveBlocked, danger: danger, mask: mask, now: now, dt: dt, speed: int(n.speed),
		random: match.box.rng.Uint32, canPass: canPass,
		flee: func() byte {
			radius := 3
			if n.role == 27 {
				radius = 10
			}
			path, ok := match.bossWorld(now).safePlan(n.pos.Cell(), radius, match.box.rng.Intn)
			n.navigator.path.nodes = path
			if ok && len(path) > 0 {
				return byte(path[0].dir)
			}
			return npcFleeFallback(position, canPass)
		},
	})
	n.pos = battleengine.Position{X: int32(position.x), Y: int32(position.y)}
	return direction
}
