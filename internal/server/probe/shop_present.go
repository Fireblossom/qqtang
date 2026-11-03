package probe

import (
	"context"
	"fmt"
	"net"
	"time"

	"golang.org/x/text/encoding/simplifiedchinese"
	"qqtang/internal/protocol/game"
	"qqtang/internal/server/application"
	"qqtang/internal/server/persistence"
)

func (server *Server) handleShopPresent(session *connectionSession, id string, data []byte, request game.ShopPresentRequest) ([]byte, string, bool) {
	response := game.ShopPresentResponse{ResultID: game.ShopBuyResultFailed, ResultString: "赠送失败", DealType: request.DealType, PayType: request.PayType}
	var result application.GiftResult
	var err error
	if session.UIN == 0 {
		err = server.bindAuxiliaryShopConnection(session, request.UIN)
	}
	if err == nil && (session.UIN != request.UIN || request.DealType != game.ShopDealTypePurchase) {
		err = fmt.Errorf("invalid gift session/deal identity")
	}
	if err == nil {
		var players *application.PlayerService
		players, err = server.players()
		if err == nil {
			result, err = players.PresentCommodity(context.Background(), request.UIN, request.DestinationUIN, server.shopCatalog, request.CommodityID, applicationPaymentMethod(request.PayType), request.AttachInfo)
		}
	}
	if err != nil {
		server.log(logEvent{Level: "error", Event: "shop_present_failed", ConnectionID: id, AccountID: fmt.Sprint(request.UIN), ErrorContext: err.Error()})
	} else {
		response.ResultString = result.Message
		if result.Status == application.PurchaseSuccess {
			response.ResultID = game.ShopBuyResultSuccess
			response.PayMoney = result.Product.SugarPrice
			session.replaceProfile(result.Purchase.Source)
			server.syncPrimarySessionProfile(session, request.UIN, result.Purchase.Source)
			server.syncPrimarySessionProfile(session, request.DestinationUIN, result.Purchase.Target)
			// Both notices use the durable commit. The disposable shop socket
			// has no absolute-balance field in RESPONSE_PRESENT.
			server.runAfter(100*time.Millisecond, func() {
				server.sendGiftBalance(request.UIN)
				server.deliverGiftNotices(request.DestinationUIN)
			})
		}
	}
	packet, encodeErr := game.BuildLocalShopPresentResponse(data, response)
	if encodeErr != nil {
		server.log(logEvent{Level: "error", Event: "shop_present_response_failed", ConnectionID: id, ErrorContext: encodeErr.Error()})
		return nil, "qqt_shop_present_response_failed", true
	}
	status := fmt.Sprintf("qqt_shop_present_%s_target_%d_commodity_%d", result.Status, request.DestinationUIN, request.CommodityID)
	server.log(logEvent{Level: "info", Event: "shop_present_settled", ConnectionID: id, AccountID: fmt.Sprint(request.UIN), Result: status})
	return packet, status, true
}

func (server *Server) giftPrimary(uin uint32) *connectionSession {
	server.liveMu.RLock()
	defer server.liveMu.RUnlock()
	for _, candidate := range server.liveSessions {
		if candidate.liveUIN.Load() == uin && candidate.connection != nil {
			return candidate
		}
	}
	return nil
}

func (server *Server) sendGiftBalance(uin uint32) {
	server.giftDeliveryMu.Lock()
	defer server.giftDeliveryMu.Unlock()
	primary := server.giftPrimary(uin)
	if primary == nil {
		return
	}
	// Read at delivery time: two concurrent gifts must not enqueue an older
	// captured balance after a newer absolute balance.
	profile, err := server.playerStore.Load(context.Background(), uin)
	if err != nil {
		server.log(logEvent{Level: "error", Event: "gift_balance_load_failed", AccountID: fmt.Sprint(uin), ErrorContext: err.Error()})
		return
	}
	packet, err := game.BuildLocalGameMoneyNotificationFromRequest(primary.packetTemplate(), game.GameMoneyNotification{UIN: uin, Money: profile.GameInfo.Money, MoneyType: 0})
	if err != nil {
		server.log(logEvent{Level: "error", Event: "gift_balance_build_failed", AccountID: fmt.Sprint(uin), ErrorContext: err.Error()})
		return
	}
	server.writeTCP(primary.connection, primary.connectionID, primary.localAddress, primary.remoteAddress, packet, "qqt_gift_balance_absolute")
}

// Delivery uses the existing private-message protocol as a local-server
// notification policy. Offline gifts stay in SQLite until the next login;
// the note is marked delivered only after the TCP write succeeds.
func (server *Server) deliverGiftNotices(uin uint32) {
	if server.playerStore == nil {
		return
	}
	server.giftDeliveryMu.Lock()
	defer server.giftDeliveryMu.Unlock()
	primary := server.giftPrimary(uin)
	if primary == nil {
		return
	}
	notices, err := server.playerStore.PendingGiftNotices(context.Background(), uin)
	if err != nil {
		server.log(logEvent{Level: "error", Event: "gift_notices_load_failed", AccountID: fmt.Sprint(uin), ErrorContext: err.Error()})
		return
	}
	for _, notice := range notices {
		if err = server.deliverGiftNotice(primary, notice); err != nil {
			server.log(logEvent{Level: "error", Event: "gift_notice_delivery_failed", AccountID: fmt.Sprint(uin), ErrorContext: err.Error()})
			return
		}
	}
}

func (server *Server) deliverGiftNotice(primary *connectionSession, notice persistence.GiftNotice) error {
	ctx := context.Background()
	item, present, err := server.playerStore.InventoryItem(ctx, notice.TargetUIN, notice.ItemID)
	if err != nil {
		return err
	}
	template := primary.packetTemplate()
	if present {
		packet, err := game.BuildLocalPlayerItemAddNotification(template, game.PlayerItemAddNotification{UIN: notice.TargetUIN, Time: uint32(time.Now().Unix()), SourceUIN: notice.SourceUIN, CommodityID: notice.CommodityID, Items: []game.ItemInfo{item}})
		if err != nil {
			return err
		}
		if !server.writeTCP(primary.connection, primary.connectionID, primary.localAddress, primary.remoteAddress, packet, "qqt_gift_inventory_absolute") {
			return net.ErrClosed
		}
	}
	name := []rune(notice.ProductName)
	for len(name) > 0 {
		b, err := simplifiedchinese.GBK.NewEncoder().Bytes([]byte(string(name)))
		if err != nil {
			return err
		}
		if len(b) <= 100 {
			break
		}
		name = name[:len(name)-1]
	}
	text := fmt.Sprintf("赠送给您「%s」", string(name))
	if notice.Note != "" {
		text += "。附言：" + notice.Note
	}
	encoded, err := simplifiedchinese.GBK.NewEncoder().Bytes([]byte(text))
	if err != nil {
		return err
	}
	content := fmt.Sprintf("wisper_uin(%d)_content[%d](%s)", notice.SourceUIN, len(encoded), text)
	packet, err := game.BuildLocalAcrossSectionChatNotification(template, game.AcrossSectionChatNotification{SourceUIN: notice.SourceUIN, Nickname: notice.SourceName, Content: content})
	if err != nil {
		return err
	}
	if !server.writeTCP(primary.connection, primary.connectionID, primary.localAddress, primary.remoteAddress, packet, "qqt_gift_note") {
		return net.ErrClosed
	}
	return server.playerStore.MarkGiftNoticeDelivered(ctx, notice.ID, notice.TargetUIN)
}
