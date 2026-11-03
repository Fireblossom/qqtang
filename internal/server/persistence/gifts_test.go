package persistence

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"qqtang/internal/protocol/game"
)

func giftStore(t *testing.T, money uint32) *PlayerStore {
	t.Helper()
	store, err := OpenPlayerStore(filepath.Join(t.TempDir(), "gifts.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	for _, uin := range []uint32{1000001, 1000002} {
		p := game.DefaultPlayerProfile()
		p.Inventory = nil
		p.GameInfo.Money = money
		if err = store.Save(context.Background(), uin, p); err != nil {
			t.Fatal(err)
		}
	}
	return store
}

func giftPlan() GiftPlan {
	return GiftPlan{SourceUIN: 1000001, TargetUIN: 1000002, CommodityID: 19, ProductName: "帽子", Item: game.NewPermanentItemInfo(19, 1), Price: 1000, Note: "给你礼物"}
}

func TestGiftDebitsSourceGrantsTargetAndPersistsNote(t *testing.T) {
	ctx := context.Background()
	store := giftStore(t, 2500)
	plan := giftPlan()
	result, err := store.GiftInventoryItem(ctx, plan)
	if err != nil {
		t.Fatal(err)
	}
	if result.Source.GameInfo.Money != 1500 || len(result.Source.Inventory) != 0 || result.Target.GameInfo.Money != 2500 || len(result.Target.Inventory) != 1 || result.Target.Inventory[0].ItemID != 19 {
		t.Fatal("gift did not affect the correct accounts")
	}
	if _, err = store.GiftInventoryItem(ctx, plan); !errors.Is(err, ErrInventoryItemOwned) {
		t.Fatalf("duplicate gift: %v", err)
	}
	current, _ := store.Load(ctx, plan.SourceUIN)
	if current.GameInfo.Money != 1500 {
		t.Fatal("rejected duplicate charged money")
	}
	notes, err := store.PendingGiftNotices(ctx, plan.TargetUIN)
	if err != nil || len(notes) != 1 || notes[0].Note != plan.Note {
		t.Fatalf("pending notes: %+v / %v", notes, err)
	}
	store.MarkGiftNoticeDelivered(ctx, notes[0].ID, plan.SourceUIN)
	notes, _ = store.PendingGiftNotices(ctx, plan.TargetUIN)
	if len(notes) != 1 {
		t.Fatal("another account marked a gift delivered")
	}
	store.MarkGiftNoticeDelivered(ctx, notes[0].ID, plan.TargetUIN)
	notes, _ = store.PendingGiftNotices(ctx, plan.TargetUIN)
	if len(notes) != 0 {
		t.Fatal("delivered note remained pending")
	}
}

func TestGiftRollsBackDebitGrantAndNoteTogether(t *testing.T) {
	ctx := context.Background()
	store := giftStore(t, 2500)
	plan := giftPlan()
	plan.Note = strings.Repeat("a", 256)
	// Fail the note constraint after both UPDATE money and INSERT inventory.
	if _, err := store.GiftInventoryItem(ctx, plan); err == nil {
		t.Fatal("accepted oversized delivery note")
	}
	source, _ := store.Load(ctx, plan.SourceUIN)
	target, _ := store.Load(ctx, plan.TargetUIN)
	if source.GameInfo.Money != 2500 || len(target.Inventory) != 0 {
		t.Fatal("failed transaction changed an account")
	}
	notes, _ := store.PendingGiftNotices(ctx, plan.TargetUIN)
	if len(notes) != 0 {
		t.Fatal("failed transaction left a note")
	}
	plan = giftPlan()
	plan.Price = 3000
	if _, err := store.GiftInventoryItem(ctx, plan); !errors.Is(err, ErrInsufficientGameMoney) {
		t.Fatalf("insufficient funds: %v", err)
	}
	plan = giftPlan()
	plan.TargetUIN = 1000099
	if _, err := store.GiftInventoryItem(ctx, plan); err == nil {
		t.Fatal("gift created an unknown recipient")
	}
}

func TestConcurrentGiftsCannotOverspend(t *testing.T) {
	ctx := context.Background()
	store := giftStore(t, 2500)
	plan := giftPlan()
	plan.Stackable = true
	var wait sync.WaitGroup
	results := make(chan error, 8)
	for i := 0; i < 8; i++ {
		wait.Add(1)
		go func() { defer wait.Done(); _, err := store.GiftInventoryItem(ctx, plan); results <- err }()
	}
	wait.Wait()
	close(results)
	success := 0
	for err := range results {
		if err == nil {
			success++
		} else if !errors.Is(err, ErrInsufficientGameMoney) {
			t.Fatal(err)
		}
	}
	source, _ := store.Load(ctx, plan.SourceUIN)
	target, _ := store.Load(ctx, plan.TargetUIN)
	notes, _ := store.PendingGiftNotices(ctx, plan.TargetUIN)
	if success != 2 || source.GameInfo.Money != 500 || target.Inventory[0].NumOfItem != 2 || len(notes) != 2 {
		t.Fatal("concurrent gift accounting mismatch")
	}
}
