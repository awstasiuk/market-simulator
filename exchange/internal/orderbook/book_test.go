package orderbook

import (
	"testing"
)

// ─── helpers ──────────────────────────────────────────────────────────────────

func newBook() *Book { return NewBook("SIM") }

func limitBuyOrder(id, agentID string, qty, price int64) Order {
	return Order{ID: id, AgentID: agentID, Side: SideBuy, Type: TypeLimit, Qty: qty, PriceTicks: price}
}

func limitSellOrder(id, agentID string, qty, price int64) Order {
	return Order{ID: id, AgentID: agentID, Side: SideSell, Type: TypeLimit, Qty: qty, PriceTicks: price}
}

// ─── resting ──────────────────────────────────────────────────────────────────

func TestRestingLimitOrder(t *testing.T) {
	b := newBook()
	result := b.Match(limitBuyOrder("o1", "alice", 10, 100))

	if result.Rested != true {
		t.Fatal("expected order to rest")
	}
	if len(result.Trades) != 0 {
		t.Fatalf("expected no trades, got %d", len(result.Trades))
	}

	bids, asks := b.SnapshotLevels()
	if len(asks) != 0 {
		t.Fatalf("expected no asks, got %d", len(asks))
	}
	if len(bids) != 1 || bids[0].PriceTicks != 100 || bids[0].Qty != 10 {
		t.Fatalf("unexpected bids snapshot: %v", bids)
	}

	order, ok := b.GetOrder("o1")
	if !ok {
		t.Fatal("GetOrder: expected to find resting order")
	}
	if order.Qty != 10 || order.PriceTicks != 100 {
		t.Fatalf("GetOrder: unexpected order state: %+v", order)
	}
}

// ─── price-time priority ──────────────────────────────────────────────────────

func TestPriceTimePriority(t *testing.T) {
	b := newBook()
	// Two bids at the same price; o1 arrived first.
	b.Match(limitBuyOrder("o1", "alice", 5, 100))
	b.Match(limitBuyOrder("o2", "bob", 5, 100))

	// A sell that crosses both bids fully.
	result := b.Match(limitSellOrder("o3", "carol", 5, 90))

	if len(result.Trades) != 1 {
		t.Fatalf("expected 1 trade, got %d", len(result.Trades))
	}
	trade := result.Trades[0]
	if trade.BuyOrderID != "o1" {
		t.Errorf("expected o1 (first at price) to fill first, got %s", trade.BuyOrderID)
	}
	if trade.Qty != 5 {
		t.Errorf("expected fill qty 5, got %d", trade.Qty)
	}

	// o1 fully filled; o2 still resting.
	_, o1Found := b.GetOrder("o1")
	if o1Found {
		t.Error("o1 should have been removed after full fill")
	}
	o2, o2Found := b.GetOrder("o2")
	if !o2Found {
		t.Fatal("o2 should still be resting")
	}
	if o2.Qty != 5 {
		t.Errorf("o2 remaining qty: want 5, got %d", o2.Qty)
	}
}

// ─── full cross ───────────────────────────────────────────────────────────────

func TestFullLimitCross(t *testing.T) {
	b := newBook()
	b.Match(limitSellOrder("sell1", "seller", 10, 100))

	result := b.Match(limitBuyOrder("buy1", "buyer", 10, 100))

	if len(result.Trades) != 1 {
		t.Fatalf("expected 1 trade, got %d", len(result.Trades))
	}
	tr := result.Trades[0]
	if tr.PriceTicks != 100 {
		t.Errorf("trade price: want 100, got %d", tr.PriceTicks)
	}
	if tr.Qty != 10 {
		t.Errorf("trade qty: want 10, got %d", tr.Qty)
	}
	if tr.BuyAgentID != "buyer" || tr.SellAgentID != "seller" {
		t.Errorf("trade agent IDs: buy=%s sell=%s", tr.BuyAgentID, tr.SellAgentID)
	}
	if result.Rested {
		t.Error("aggressor should not have rested after full fill")
	}

	// Both orders should be gone from the book.
	bids, asks := b.SnapshotLevels()
	if len(bids)+len(asks) != 0 {
		t.Errorf("book should be empty after full cross")
	}
}

// ─── partial fill ─────────────────────────────────────────────────────────────

func TestPartialFill(t *testing.T) {
	b := newBook()
	b.Match(limitSellOrder("sell1", "seller", 3, 100))

	// Aggressor buys 10 but only 3 available.
	result := b.Match(limitBuyOrder("buy1", "buyer", 10, 100))

	if len(result.Trades) != 1 {
		t.Fatalf("expected 1 trade, got %d", len(result.Trades))
	}
	if result.Trades[0].Qty != 3 {
		t.Errorf("partial fill qty: want 3, got %d", result.Trades[0].Qty)
	}
	if !result.Rested {
		t.Error("unfilled portion of aggressor should have rested")
	}

	// sell1 fully consumed.
	_, sellFound := b.GetOrder("sell1")
	if sellFound {
		t.Error("sell1 should have been removed after full fill")
	}

	// buy1 resting with remaining qty.
	buy, buyFound := b.GetOrder("buy1")
	if !buyFound {
		t.Fatal("buy1 should be resting with remaining qty")
	}
	if buy.Qty != 7 {
		t.Errorf("buy1 remaining qty: want 7, got %d", buy.Qty)
	}
}

// ─── cancel ───────────────────────────────────────────────────────────────────

func TestCancelRestingOrder(t *testing.T) {
	b := newBook()
	b.Match(limitBuyOrder("o1", "alice", 10, 100))

	cancelled, ok := b.Cancel("o1")
	if !ok {
		t.Fatal("Cancel returned not-found for a resting order")
	}
	if cancelled.Qty != 10 {
		t.Errorf("cancelled remaining qty: want 10, got %d", cancelled.Qty)
	}

	_, found := b.GetOrder("o1")
	if found {
		t.Error("GetOrder should return false after cancel")
	}

	bids, _ := b.SnapshotLevels()
	if len(bids) != 0 {
		t.Error("bid side should be empty after cancel")
	}
}

func TestCancelUnknownOrder(t *testing.T) {
	b := newBook()
	cancelled, ok := b.Cancel("nonexistent")
	if ok {
		t.Error("Cancel should return false for unknown order ID")
	}
	if cancelled.ID != "" || cancelled.Qty != 0 {
		t.Errorf("cancelled order should be zero value, got %+v", cancelled)
	}
}

// ─── snapshot ordering ────────────────────────────────────────────────────────

func TestSnapshotLevelsOrdering(t *testing.T) {
	b := newBook()
	// Add bids at 90, 100, 95 to ensure they are sorted highest-first.
	b.Match(limitBuyOrder("b1", "a", 1, 90))
	b.Match(limitBuyOrder("b2", "a", 1, 100))
	b.Match(limitBuyOrder("b3", "a", 1, 95))
	// Add asks at 110, 105, 120 to ensure they are sorted lowest-first.
	b.Match(limitSellOrder("s1", "a", 1, 110))
	b.Match(limitSellOrder("s2", "a", 1, 105))
	b.Match(limitSellOrder("s3", "a", 1, 120))

	bids, asks := b.SnapshotLevels()

	if len(bids) != 3 {
		t.Fatalf("expected 3 bid levels, got %d", len(bids))
	}
	wantBids := []int64{100, 95, 90}
	for i, w := range wantBids {
		if bids[i].PriceTicks != w {
			t.Errorf("bid[%d]: want %d, got %d", i, w, bids[i].PriceTicks)
		}
	}

	if len(asks) != 3 {
		t.Fatalf("expected 3 ask levels, got %d", len(asks))
	}
	wantAsks := []int64{105, 110, 120}
	for i, w := range wantAsks {
		if asks[i].PriceTicks != w {
			t.Errorf("ask[%d]: want %d, got %d", i, w, asks[i].PriceTicks)
		}
	}
}

// ─── market buy ───────────────────────────────────────────────────────────────

func TestMatchMarketBuySweepsLevels(t *testing.T) {
	b := newBook()
	b.Match(limitSellOrder("s1", "s", 5, 100))
	b.Match(limitSellOrder("s2", "s", 5, 105))

	// Budget exactly covers 5@100 + 5@105 = 1025.
	result := b.MatchMarketBuy("buyer", "mb1", 1025)

	if len(result.Trades) != 2 {
		t.Fatalf("expected 2 trades, got %d", len(result.Trades))
	}
	if result.SpentCashTicks != 1025 {
		t.Errorf("SpentCashTicks: want 1025, got %d", result.SpentCashTicks)
	}
	// Both selling orders absorbed.
	_, asks := b.SnapshotLevels()
	if len(asks) != 0 {
		t.Errorf("ask side should be empty after full sweep, got %d levels", len(asks))
	}
}

func TestMatchMarketBuyBudgetExhaustedMidLevel(t *testing.T) {
	b := newBook()
	b.Match(limitSellOrder("s1", "s", 10, 100)) // 10 shares @ 100 = 1000

	// Budget covers only 3 shares at price 100.
	result := b.MatchMarketBuy("buyer", "mb1", 350)

	if len(result.Trades) != 1 {
		t.Fatalf("expected 1 trade, got %d", len(result.Trades))
	}
	if result.Trades[0].Qty != 3 {
		t.Errorf("filled qty: want 3, got %d", result.Trades[0].Qty)
	}
	if result.SpentCashTicks != 300 {
		t.Errorf("SpentCashTicks: want 300, got %d", result.SpentCashTicks)
	}

	// 7 shares remain at the ask level.
	_, asks := b.SnapshotLevels()
	if len(asks) != 1 || asks[0].Qty != 7 {
		t.Errorf("remaining ask qty: want 7, got %v", asks)
	}
}

func TestMatchMarketBuyNoLiquidity(t *testing.T) {
	b := newBook()
	result := b.MatchMarketBuy("buyer", "mb1", 5000)

	if len(result.Trades) != 0 {
		t.Errorf("expected no trades, got %d", len(result.Trades))
	}
	if result.SpentCashTicks != 0 {
		t.Errorf("SpentCashTicks should be 0, got %d", result.SpentCashTicks)
	}
}

// ─── market sell ──────────────────────────────────────────────────────────────

func TestMatchMarketSellSweepsLevels(t *testing.T) {
	b := newBook()
	b.Match(limitBuyOrder("b1", "b", 5, 100))
	b.Match(limitBuyOrder("b2", "b", 5, 95))

	result := b.MatchMarketSell("seller", "ms1", 10)

	if len(result.Trades) != 2 {
		t.Fatalf("expected 2 trades, got %d", len(result.Trades))
	}
	if result.UnfilledQty != 0 {
		t.Errorf("UnfilledQty: want 0, got %d", result.UnfilledQty)
	}
	bids, _ := b.SnapshotLevels()
	if len(bids) != 0 {
		t.Errorf("bid side should be empty after full sweep, got %d levels", len(bids))
	}
}

func TestMatchMarketSellPartialLiquidity(t *testing.T) {
	b := newBook()
	b.Match(limitBuyOrder("b1", "b", 4, 100)) // only 4 bids available

	result := b.MatchMarketSell("seller", "ms1", 10)

	if len(result.Trades) != 1 {
		t.Fatalf("expected 1 trade, got %d", len(result.Trades))
	}
	if result.Trades[0].Qty != 4 {
		t.Errorf("filled qty: want 4, got %d", result.Trades[0].Qty)
	}
	if result.UnfilledQty != 6 {
		t.Errorf("UnfilledQty: want 6, got %d", result.UnfilledQty)
	}
}

func TestMatchMarketSellNoLiquidity(t *testing.T) {
	b := newBook()
	result := b.MatchMarketSell("seller", "ms1", 10)

	if len(result.Trades) != 0 {
		t.Errorf("expected no trades, got %d", len(result.Trades))
	}
	if result.UnfilledQty != 10 {
		t.Errorf("UnfilledQty: want 10, got %d", result.UnfilledQty)
	}
}
