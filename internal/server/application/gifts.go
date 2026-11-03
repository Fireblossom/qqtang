package application

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"qqtang/internal/game/shopcatalog"
	"qqtang/internal/protocol/game"
	"qqtang/internal/server/persistence"
)

type GiftResult struct {
	Status   PurchaseStatus
	Message  string
	Product  shopcatalog.Product
	Purchase persistence.GiftPurchase
}

func (service *PlayerService) PresentCommodity(ctx context.Context, source, target uint32, catalog *shopcatalog.Catalog, commodity uint32, method PaymentMethod, note string) (GiftResult, error) {
	if service == nil || service.repository == nil || catalog == nil || source == 0 || target == 0 {
		return GiftResult{}, fmt.Errorf("gift service or accounts unavailable")
	}
	first, second := source, target
	if second < first {
		first, second = second, first
	}
	unlock := service.lockPlayer(first)
	defer unlock()
	if second != first {
		unlock := service.lockPlayer(second)
		defer unlock()
	}
	product, found := catalog.Lookup(commodity)
	if !found {
		return GiftResult{Status: PurchaseUnknownCommodity, Message: "商品不存在或已下架"}, nil
	}
	if method != PaymentGameMoney {
		return GiftResult{Status: PurchaseUnsupportedMethod, Message: "本地赠送仅支持糖币", Product: product}, nil
	}
	_, cosmetic := service.lookupEquipment(product.ItemID)
	purchase, err := service.repository.GiftInventoryItem(ctx, persistence.GiftPlan{SourceUIN: source, TargetUIN: target, CommodityID: commodity, ProductName: product.Name,
		Item: game.NewPermanentItemInfo(product.ItemID, product.Quantity), Price: product.SugarPrice, Stackable: !cosmetic, Note: note})
	if err != nil {
		status, message := PurchaseStatus(""), ""
		switch {
		case errors.Is(err, sql.ErrNoRows):
			status, message = PurchaseStatus("unknown_recipient"), "收件账号不存在"
		case errors.Is(err, persistence.ErrInsufficientGameMoney):
			status, message = PurchaseInsufficientFunds, "糖币不足"
		case errors.Is(err, persistence.ErrInventoryItemOwned):
			status, message = PurchaseAlreadyOwned, "对方已拥有该永久道具"
		case errors.Is(err, persistence.ErrInventoryKindLimitReached):
			status, message = PurchaseInventoryFull, "对方背包种类已满"
		default:
			return GiftResult{}, err
		}
		return GiftResult{Status: status, Message: message, Product: product}, nil
	}
	purchase.Source, err = service.projectEquipmentLocked(ctx, source, purchase.Source)
	if err != nil {
		return GiftResult{}, err
	}
	purchase.Target, err = service.projectEquipmentLocked(ctx, target, purchase.Target)
	if err != nil {
		return GiftResult{}, err
	}
	return GiftResult{Status: PurchaseSuccess, Message: "赠送成功", Product: product, Purchase: purchase}, nil
}
