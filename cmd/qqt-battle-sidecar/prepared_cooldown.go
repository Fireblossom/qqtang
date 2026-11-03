package main

import "qqtang/internal/game/mapdata"

func (match *onlineMatch) loadPropTable() {
	if match.propTable != nil || match.clientRoot == "" {
		return
	}
	if table, err := mapdata.LoadAdventureProps(match.clientRoot); err == nil {
		match.propTable = table
	} else {
		match.logf("prop table: %v", err)
		match.clientRoot = "" // existing failure policy: log once
	}
}

func (match *onlineMatch) preparedCooldown(item uint32) *preparedCooldownView {
	match.loadPropTable()
	prop, found := match.propTable[item]
	if !found {
		if match.propTable != nil {
			// Native 005DAC82 displays a full stationary mask when the
			// prepared object is absent from the loaded prop registry.
			return &preparedCooldownView{}
		}
		return nil
	}
	adventure := match.adventure != nil
	allowed := prop.GameMode == 0 || prop.GameMode == 1 && !adventure || prop.GameMode == 2 && adventure
	return &preparedCooldownView{Initial: prop.InitialInterval, Interval: prop.FireInterval(), LastUse: match.preparedLastUse[prop.Tier], Allowed: allowed}
}

// 005F7CC5 gates by InitialInterval and the player's last use of this Tier.
// The 400ms in the mask's animation speed is not an extra use restriction.
func (cooldown preparedCooldownView) ready(now uint32) bool {
	return cooldown.Allowed && now >= cooldown.Initial && (cooldown.LastUse == 0 || now-cooldown.LastUse >= cooldown.Interval)
}
