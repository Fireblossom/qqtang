package persistence

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"qqtang/internal/protocol/game"
)

type GiftPlan struct {
	SourceUIN, TargetUIN uint32
	CommodityID          uint32
	ProductName          string
	Item                 game.ItemInfo
	Price                uint32
	Stackable            bool
	Note                 string
}

type GiftNotice struct {
	ID                                int64
	SourceUIN, TargetUIN, CommodityID uint32
	ItemID                            uint16
	SourceName, ProductName, Note     string
}

type GiftPurchase struct {
	Source, Target game.PlayerProfile
	Notice         GiftNotice
}

// GiftInventoryItem atomically debits the giver, grants the receiver, and
// stores the delivery note. No client request can grant before the debit.
func (store *PlayerStore) GiftInventoryItem(ctx context.Context, plan GiftPlan) (GiftPurchase, error) {
	if store == nil || store.db == nil || plan.SourceUIN == 0 || plan.TargetUIN == 0 || plan.Item.ItemID == 0 || plan.Item.NumOfItem == 0 {
		return GiftPurchase{}, fmt.Errorf("gift requires source, target and non-empty item")
	}
	tx, err := store.db.BeginTx(ctx, nil)
	if err != nil {
		return GiftPurchase{}, err
	}
	defer tx.Rollback()
	if err = requirePlayerTx(ctx, tx, plan.TargetUIN); err != nil {
		return GiftPurchase{}, err
	}
	var encoded string
	if err = tx.QueryRowContext(ctx, `SELECT profile_json FROM local_players WHERE uin=?`, plan.SourceUIN).Scan(&encoded); err != nil {
		return GiftPurchase{}, err
	}
	var source game.PlayerProfile
	if err = json.Unmarshal([]byte(encoded), &source); err != nil {
		return GiftPurchase{}, err
	}
	var existing uint64
	err = tx.QueryRowContext(ctx, `SELECT quantity FROM player_inventory WHERE uin=? AND item_id=?`, plan.TargetUIN, plan.Item.ItemID).Scan(&existing)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return GiftPurchase{}, err
	}
	if existing > 0 && !plan.Stackable {
		return GiftPurchase{}, ErrInventoryItemOwned
	}
	quantity := existing + uint64(plan.Item.NumOfItem)
	if quantity > uint64(^uint32(0)) {
		return GiftPurchase{}, fmt.Errorf("gift item quantity exceeds uint32")
	}
	if source.GameInfo.Money < plan.Price {
		return GiftPurchase{}, ErrInsufficientGameMoney
	}
	if err = validateInventoryKindLimitTx(ctx, tx, plan.TargetUIN, []uint16{plan.Item.ItemID}); err != nil {
		return GiftPurchase{}, err
	}
	source.GameInfo.Money -= plan.Price
	source.Inventory = nil
	updated, err := json.Marshal(source)
	if err != nil {
		return GiftPurchase{}, err
	}
	if _, err = tx.ExecContext(ctx, `UPDATE local_players SET profile_json=?, updated_utc=? WHERE uin=?`, string(updated), time.Now().UTC().Format(time.RFC3339Nano), plan.SourceUIN); err != nil {
		return GiftPurchase{}, err
	}
	item := plan.Item
	item.NumOfItem = uint32(quantity)
	item.BuyTime = 0
	item.AvailPeriod = game.LocalPermanentAvailablePeriod
	if _, err = tx.ExecContext(ctx, `INSERT INTO player_inventory(uin,item_id,quantity,item_status,item_role_id,item_effect,item_color,buy_time,available_period)
		VALUES(?,?,?,?,?,?,?,?,?) ON CONFLICT(uin,item_id) DO UPDATE SET quantity=excluded.quantity,
		buy_time=excluded.buy_time,available_period=max(player_inventory.available_period,excluded.available_period)`,
		plan.TargetUIN, item.ItemID, item.NumOfItem, item.ItemStatus, item.ItemRoleID, item.ItemEffect, item.ItemColor, item.BuyTime, item.AvailPeriod); err != nil {
		return GiftPurchase{}, err
	}
	row, err := tx.ExecContext(ctx, `INSERT INTO player_gift_notices(source_uin,target_uin,commodity_id,item_id,source_name,product_name,note)
		VALUES(?,?,?,?,?,?,?)`, plan.SourceUIN, plan.TargetUIN, plan.CommodityID, item.ItemID, source.Nickname, plan.ProductName, plan.Note)
	if err != nil {
		return GiftPurchase{}, err
	}
	id, err := row.LastInsertId()
	if err != nil {
		return GiftPurchase{}, err
	}
	if err = tx.Commit(); err != nil {
		return GiftPurchase{}, err
	}
	result := GiftPurchase{Notice: GiftNotice{ID: id, SourceUIN: plan.SourceUIN, TargetUIN: plan.TargetUIN, CommodityID: plan.CommodityID, ItemID: item.ItemID,
		SourceName: source.Nickname, ProductName: plan.ProductName, Note: plan.Note}}
	result.Source, err = store.Load(ctx, plan.SourceUIN)
	if err != nil {
		return GiftPurchase{}, err
	}
	result.Target, err = store.Load(ctx, plan.TargetUIN)
	return result, err
}

func (store *PlayerStore) PendingGiftNotices(ctx context.Context, uin uint32) ([]GiftNotice, error) {
	rows, err := store.db.QueryContext(ctx, `SELECT id,source_uin,target_uin,commodity_id,item_id,source_name,product_name,note
		FROM player_gift_notices WHERE target_uin=? AND delivered=0 ORDER BY id`, uin)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []GiftNotice
	for rows.Next() {
		var n GiftNotice
		if err = rows.Scan(&n.ID, &n.SourceUIN, &n.TargetUIN, &n.CommodityID, &n.ItemID, &n.SourceName, &n.ProductName, &n.Note); err != nil {
			return nil, err
		}
		out = append(out, n)
	}
	return out, rows.Err()
}

func (store *PlayerStore) MarkGiftNoticeDelivered(ctx context.Context, id int64, uin uint32) error {
	_, err := store.db.ExecContext(ctx, `UPDATE player_gift_notices SET delivered=1 WHERE id=? AND target_uin=?`, id, uin)
	return err
}
