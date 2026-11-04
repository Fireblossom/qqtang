package main

// The default model's action and frame-reset state is separate from both
// its movement command and the ten-second ice speed effect. 00424A60 and
// 00424EB0 reset frames only when the action or facing actually changes.
type npcPictureState struct {
	action, facing byte
	clock          uint32
}

func (picture *npcPictureState) set(action, facing byte) {
	if picture.action != action || picture.facing != facing {
		picture.action, picture.facing, picture.clock = action, facing, 0
	}
}

func (picture *npcPictureState) frame(dt uint32) {
	if picture.action == 4 {
		dt *= 2
	} // 00425020 doubles walking time
	picture.clock += dt
}
