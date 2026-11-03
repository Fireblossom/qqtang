package probe

import (
	"fmt"
	"qqtang/internal/protocol/game"
)

// The local catalog issues permanent goods and RESTORPRICE=0. Decode and
// answer the original operation, but never charge for a fictitious repair or
// turn it into a second purchase. Timed-item restoration is not enabled.
func (server *Server) handleShopRestore(session *connectionSession, id string, data []byte, request game.ShopRestoreRequest) ([]byte, string, bool) {
	response := game.ShopRestoreResponse{ResultID: 1, ItemID: request.ItemID, ResultString: "无法修理该道具"}
	var err error
	if session.UIN == 0 {
		err = server.bindAuxiliaryShopConnection(session, request.UIN)
	}
	status := "qqt_shop_restore_unavailable"
	if err == nil && session.UIN == request.UIN {
		item, present := inventoryItemByID(session.Profile.Inventory, uint16(request.ItemID))
		if !present || item.NumOfItem == 0 {
			response.ResultString = "背包中没有该道具"
			status = "qqt_shop_restore_missing_item"
		} else if item.AvailPeriod == 0 || item.AvailPeriod == game.LocalPermanentAvailablePeriod {
			response.ResultString = "该道具无需修理"
			status = "qqt_shop_restore_unnecessary"
		} else {
			response.ResultString = "本地版本尚未启用限时道具修理"
		}
	} else if err == nil {
		err = fmt.Errorf("restore UIN does not match session")
	}
	if err != nil {
		server.log(logEvent{Level: "error", Event: "shop_restore_failed", ConnectionID: id, ErrorContext: err.Error()})
	}
	packet, err := game.BuildLocalShopRestoreResponse(data, response)
	if err != nil {
		return nil, "qqt_shop_restore_response_failed", true
	}
	return packet, status, true
}
