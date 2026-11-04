package main

// bossBirthState is the default 005C06C9/005C0743/005ABE96 path while
// a named Boss is in mode9. Frame counters compare the old value before
// adding dt. The greeting branch runs after the controller update.
type bossBirthState struct {
	mode, walk    byte
	counter       uint32
	greetingTimer int32
}

func newBossBirthState() bossBirthState {
	return bossBirthState{mode: 9, walk: 1, greetingTimer: 1000}
}

func (birth *bossBirthState) frame(dt uint32, role uint16, arbitrator, effectReady bool) (talk bool) {
	if birth.counter < 3000 {
		birth.counter += dt
	} else if effectReady {
		birth.counter = 0
		birth.mode, birth.walk = 1, 1
	}
	if arbitrator && birth.greetingTimer >= 0 {
		if role == 24 || role == 28 || role == 34 {
			talk = true
			birth.mode = 1
			if effectReady {
				birth.counter = 0
				birth.walk = 1
			}
		}
		birth.greetingTimer = -5001
	}
	return talk
}

// advanceBossBirthFrame is the actor-frame phase after gameplay commands.
// It also runs during Ready, when the battle engine is parked at 3000ms.
// Default Bosses have no +3E8 effect object. Its completion gate is covered
// by the native oracle; special effect lifecycles remain separate work.
func (match *onlineMatch) advanceBossBirthFrame(now uint32) {
	if match.boss == nil {
		return
	}
	for _, b := range match.boss.ordered() {
		if b.dead || now < b.birthFrameAt {
			continue
		}
		dt := now - b.birthFrameAt
		b.birthFrameAt = now
		if b.birth.frame(dt, b.role, match.isArbitrator(), true) {
			match.bossGreeting(b, match.engine.ElapsedMS())
		}
	}
	if match.isArbitrator() {
		match.flushNPC(&match.boss.outbox)
	}
}
