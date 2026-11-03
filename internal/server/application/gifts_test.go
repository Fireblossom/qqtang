package application

import (
	"fmt"
	"path/filepath"
	"qqtang/internal/game/itemcatalog"
	"qqtang/internal/game/shopcatalog"
	"qqtang/internal/protocol/game"
	"qqtang/internal/server/persistence"
	"testing"
	"time"
)

func TestPresentCommodityValidatesPaymentRecipientAndLocksBothAccounts(t *testing.T) {
	store, err := persistence.OpenPlayerStore(filepath.Join(t.TempDir(), "gifts.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	for _, uin := range []uint32{1000001, 1000002} {
		p := game.DefaultPlayerProfile()
		p.Inventory = nil
		p.GameInfo.Money = 2500
		if err = store.Save(t.Context(), uin, p); err != nil {
			t.Fatal(err)
		}
	}
	catalog, err := shopcatalog.NewCatalog(848, []itemcatalog.RegistryEntry{{ItemID: 20050, Category: "item", ResourceID: 20050, Name: "医疗包"}}, []itemcatalog.CommodityEntry{{CommodityID: 20050, Category: "item", ResourceID: 20050, Name: "医疗包"}}, shopcatalog.DefaultCommodityLimit)
	if err != nil {
		t.Fatal(err)
	}
	service, err := NewPlayerService(store, nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, request := range []struct {
		target uint32
		method PaymentMethod
		status PurchaseStatus
	}{{1000099, PaymentGameMoney, "unknown_recipient"}, {1000002, PaymentQCoin, PurchaseUnsupportedMethod}} {
		r, err := service.PresentCommodity(t.Context(), 1000001, request.target, catalog, 20050, request.method, "hi")
		if err != nil || r.Status != request.status {
			t.Fatalf("business rejection: %+v / %v", r, err)
		}
	}
	result := make(chan error, 2)
	for _, pair := range [][2]uint32{{1000001, 1000002}, {1000002, 1000001}} {
		go func(pair [2]uint32) {
			r, err := service.PresentCommodity(t.Context(), pair[0], pair[1], catalog, 20050, PaymentGameMoney, "hi")
			if err == nil && r.Status != PurchaseSuccess {
				err = fmt.Errorf("gift rejected: %s", r.Status)
			}
			result <- err
		}(pair)
	}
	for i := 0; i < 2; i++ {
		select {
		case err := <-result:
			if err != nil {
				t.Fatal(err)
			}
		case <-time.After(3 * time.Second):
			t.Fatal("opposite-direction gifts deadlocked")
		}
	}
	for _, uin := range []uint32{1000001, 1000002} {
		p, _ := store.Load(t.Context(), uin)
		if p.GameInfo.Money != 1500 || len(p.Inventory) != 1 || p.Inventory[0].NumOfItem != 1 {
			t.Fatal("gift touched the wrong account or amount")
		}
	}
}
