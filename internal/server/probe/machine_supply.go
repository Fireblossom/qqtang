package probe

import (
	"fmt"
	"slices"

	"qqtang/internal/game/mapdata"
	"qqtang/internal/game/match"
	"qqtang/internal/protocol/game"
)

func (server *Server) requireCompetitiveProduceItem(session *connectionSession, item game.ProduceItem) (*match.CompetitiveBattle, mapdata.CompetitiveRuleKind, error) {
	if server.mapCatalog == nil || session == nil {
		return nil, "", fmt.Errorf("produce-item requires a session and map catalog")
	}
	selected, ok := server.mapCatalog.CompetitiveMap(session.CurrentMapID)
	if !ok || (selected.Rule != mapdata.CompetitiveRuleKickBomb && selected.Rule != mapdata.CompetitiveRuleMachine) {
		return nil, "", fmt.Errorf("produce-item is invalid on map %d", session.CurrentMapID)
	}
	if selected.Rule == mapdata.CompetitiveRuleMachine {
		cell := mapdata.CompetitiveCell{Row: item.RowAndCol >> 4, Col: item.RowAndCol & 15}
		if item.ItemID < 1 || item.ItemID > 3 || !slices.Contains(selected.MachineSupplyCells, cell) {
			return nil, "", fmt.Errorf("invalid machine supply %d at %v", item.ItemID, cell)
		}
	}
	battle, err := server.requireCompetitiveRule(session, selected.Rule, 0)
	return battle, selected.Rule, err
}
