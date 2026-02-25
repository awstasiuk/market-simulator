package server

import (
	"context"
	"sort"
	"testing"
	"time"

	"market-simulator/exchange/gen/market"
)

// ─── test helpers ─────────────────────────────────────────────────────────────

// newTestServer creates an ExchangeServer with a far-future tick interval so
// the background ticker never fires during tests. Stop is registered as a
// test cleanup so t.Cleanup handles it automatically.
func newTestServer(t *testing.T) *ExchangeServer {
	t.Helper()
	s := NewExchangeServerWithOptions(24*time.Hour, 5)
	t.Cleanup(s.Stop)
	return s
}

// ptr returns a pointer to v, used for optional proto fields.
func ptr[T any](v T) *T { return &v }

func mustCreateAccount(t *testing.T, s *ExchangeServer, agentID string, cash int64, positions map[string]int64) {
	t.Helper()
	var pos []*market.PositionEntry
	for sym, qty := range positions {
		pos = append(pos, &market.PositionEntry{Symbol: sym, Qty: qty})
	}
	resp, err := s.CreateAccount(context.Background(), &market.CreateAccountRequest{
		AgentId:   agentID,
		CashTicks: cash,
		Positions: pos,
	})
	if err != nil || !resp.Ok {
		t.Fatalf("CreateAccount(%q): err=%v ok=%v reason=%q", agentID, err, resp.GetOk(), resp.GetReason())
	}
}

func submitLimit(t *testing.T, s *ExchangeServer, agentID, symbol, side string, qty, price, clientID int64) *market.SubmitOrderResponse {
	t.Helper()
	sideEnum := market.OrderSide_ORDER_SIDE_BUY
	if side == "SELL" {
		sideEnum = market.OrderSide_ORDER_SIDE_SELL
	}
	resp, err := s.SubmitOrder(context.Background(), &market.SubmitOrderRequest{
		AgentId:       agentID,
		Symbol:        symbol,
		Side:          sideEnum,
		Type:          market.OrderType_ORDER_TYPE_LIMIT,
		Qty:           qty,
		PriceTicks:    ptr(price),
		ClientOrderId: clientID,
	})
	if err != nil {
		t.Fatalf("SubmitOrder: %v", err)
	}
	return resp
}

func mustAccept(t *testing.T, resp *market.SubmitOrderResponse) {
	t.Helper()
	if resp.Status != market.OrderStatus_ORDER_STATUS_ACCEPTED {
		t.Fatalf("expected ACCEPTED, got %v reason=%q", resp.Status, resp.Reason)
	}
}

func getPortfolio(t *testing.T, s *ExchangeServer, agentID string) *market.GetPortfolioResponse {
	t.Helper()
	resp, err := s.GetPortfolio(context.Background(), &market.GetPortfolioRequest{AgentId: agentID})
	if err != nil {
		t.Fatalf("GetPortfolio: %v", err)
	}
	return resp
}

// positionFor finds the qty for a symbol in a GetPortfolioResponse.Positions slice.
func positionFor(resp *market.GetPortfolioResponse, symbol string) int64 {
	for _, p := range resp.Positions {
		if p.Symbol == symbol {
			return p.Qty
		}
	}
	return 0
}

// reservedSharesFor finds the reserved qty for a symbol.
func reservedSharesFor(resp *market.GetPortfolioResponse, symbol string) int64 {
	for _, p := range resp.ReservedShares {
		if p.Symbol == symbol {
			return p.Qty
		}
	}
	return 0
}

// ─── SubmitOrder: validation ──────────────────────────────────────────────────

func TestSubmitOrderNilRequest(t *testing.T) {
	s := newTestServer(t)
	resp, err := s.SubmitOrder(context.Background(), nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp.Status != market.OrderStatus_ORDER_STATUS_REJECTED {
		t.Errorf("expected REJECTED, got %v", resp.Status)
	}
}

func TestSubmitOrderMissingFields(t *testing.T) {
	s := newTestServer(t)
	cases := []struct {
		name string
		req  *market.SubmitOrderRequest
	}{
		{"blank agent_id", &market.SubmitOrderRequest{
			Symbol: "SIM", Side: market.OrderSide_ORDER_SIDE_BUY,
			Type: market.OrderType_ORDER_TYPE_LIMIT, Qty: 1, PriceTicks: ptr(int64(100)),
		}},
		{"blank symbol", &market.SubmitOrderRequest{
			AgentId: "alice", Side: market.OrderSide_ORDER_SIDE_BUY,
			Type: market.OrderType_ORDER_TYPE_LIMIT, Qty: 1, PriceTicks: ptr(int64(100)),
		}},
		{"unspecified side", &market.SubmitOrderRequest{
			AgentId: "alice", Symbol: "SIM",
			Type: market.OrderType_ORDER_TYPE_LIMIT, Qty: 1, PriceTicks: ptr(int64(100)),
		}},
		{"unspecified type", &market.SubmitOrderRequest{
			AgentId: "alice", Symbol: "SIM",
			Side: market.OrderSide_ORDER_SIDE_BUY, Qty: 1, PriceTicks: ptr(int64(100)),
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			resp, err := s.SubmitOrder(context.Background(), tc.req)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if resp.Status != market.OrderStatus_ORDER_STATUS_REJECTED {
				t.Errorf("expected REJECTED, got %v", resp.Status)
			}
		})
	}
}

func TestSubmitOrderQtyValidation(t *testing.T) {
	s := newTestServer(t)

	t.Run("limit qty=0", func(t *testing.T) {
		resp, err := s.SubmitOrder(context.Background(), &market.SubmitOrderRequest{
			AgentId: "a", Symbol: "SIM",
			Side: market.OrderSide_ORDER_SIDE_BUY, Type: market.OrderType_ORDER_TYPE_LIMIT,
			Qty: 0, PriceTicks: ptr(int64(100)),
		})
		if err != nil || resp.Status != market.OrderStatus_ORDER_STATUS_REJECTED {
			t.Errorf("expected REJECTED for qty=0 limit order")
		}
	})

	t.Run("market buy no cash_budget", func(t *testing.T) {
		resp, err := s.SubmitOrder(context.Background(), &market.SubmitOrderRequest{
			AgentId: "a", Symbol: "SIM",
			Side: market.OrderSide_ORDER_SIDE_BUY, Type: market.OrderType_ORDER_TYPE_MARKET,
		})
		if err != nil || resp.Status != market.OrderStatus_ORDER_STATUS_REJECTED {
			t.Errorf("expected REJECTED for market buy without cash_budget_ticks")
		}
	})

	t.Run("market sell qty=0", func(t *testing.T) {
		resp, err := s.SubmitOrder(context.Background(), &market.SubmitOrderRequest{
			AgentId: "a", Symbol: "SIM",
			Side: market.OrderSide_ORDER_SIDE_SELL, Type: market.OrderType_ORDER_TYPE_MARKET,
			Qty: 0,
		})
		if err != nil || resp.Status != market.OrderStatus_ORDER_STATUS_REJECTED {
			t.Errorf("expected REJECTED for market sell with qty=0")
		}
	})
}

// ─── client_order_id echo ─────────────────────────────────────────────────────

func TestClientOrderIDEchoedOnAccept(t *testing.T) {
	s := newTestServer(t)
	mustCreateAccount(t, s, "alice", 100_000, nil)

	resp := submitLimit(t, s, "alice", "SIM", "BUY", 5, 100, 42)
	mustAccept(t, resp)

	if resp.ClientOrderId != 42 {
		t.Errorf("client_order_id: want 42, got %d", resp.ClientOrderId)
	}
}

func TestClientOrderIDEchoedOnReject(t *testing.T) {
	s := newTestServer(t)
	resp, err := s.SubmitOrder(context.Background(), &market.SubmitOrderRequest{
		AgentId:       "",
		Symbol:        "SIM",
		Side:          market.OrderSide_ORDER_SIDE_BUY,
		Type:          market.OrderType_ORDER_TYPE_LIMIT,
		Qty:           1,
		PriceTicks:    ptr(int64(100)),
		ClientOrderId: 77,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp.Status != market.OrderStatus_ORDER_STATUS_REJECTED {
		t.Errorf("expected REJECTED")
	}
	if resp.ClientOrderId != 77 {
		t.Errorf("client_order_id echoed on reject: want 77, got %d", resp.ClientOrderId)
	}
}

// ─── ListOrders ───────────────────────────────────────────────────────────────

func TestLimitOrderRestsAndAppearsInListOrders(t *testing.T) {
	s := newTestServer(t)
	mustCreateAccount(t, s, "alice", 100_000, nil)

	resp := submitLimit(t, s, "alice", "SIM", "BUY", 10, 100, 0)
	mustAccept(t, resp)
	orderID := resp.OrderId

	lo, err := s.ListOrders(context.Background(), &market.ListOrdersRequest{AgentId: "alice"})
	if err != nil {
		t.Fatalf("ListOrders: %v", err)
	}
	if len(lo.Orders) != 1 {
		t.Fatalf("expected 1 resting order, got %d", len(lo.Orders))
	}
	o := lo.Orders[0]
	if o.OrderId != orderID {
		t.Errorf("order_id: want %s, got %s", orderID, o.OrderId)
	}
	if o.Side != market.OrderSide_ORDER_SIDE_BUY {
		t.Errorf("side: want BUY, got %v", o.Side)
	}
	if o.Qty != 10 {
		t.Errorf("qty: want 10, got %d", o.Qty)
	}
	if o.PriceTicks != 100 {
		t.Errorf("price_ticks: want 100, got %d", o.PriceTicks)
	}
}

func TestListOrdersSymbolFilter(t *testing.T) {
	s := newTestServer(t)
	mustCreateAccount(t, s, "alice", 100_000, nil)

	mustAccept(t, submitLimit(t, s, "alice", "SIM", "BUY", 1, 100, 0))
	mustAccept(t, submitLimit(t, s, "alice", "FOO", "BUY", 1, 200, 0))

	lo, err := s.ListOrders(context.Background(), &market.ListOrdersRequest{AgentId: "alice", Symbol: "SIM"})
	if err != nil {
		t.Fatalf("ListOrders: %v", err)
	}
	if len(lo.Orders) != 1 {
		t.Fatalf("expected 1 order for SIM, got %d", len(lo.Orders))
	}
	if lo.Orders[0].Symbol != "SIM" {
		t.Errorf("symbol: want SIM, got %s", lo.Orders[0].Symbol)
	}
}

func TestListOrdersAgentIsolation(t *testing.T) {
	s := newTestServer(t)
	mustCreateAccount(t, s, "alice", 100_000, nil)
	mustCreateAccount(t, s, "bob", 100_000, nil)

	mustAccept(t, submitLimit(t, s, "alice", "SIM", "BUY", 1, 100, 0))
	mustAccept(t, submitLimit(t, s, "bob", "SIM", "BUY", 1, 100, 0))

	lo, err := s.ListOrders(context.Background(), &market.ListOrdersRequest{AgentId: "alice"})
	if err != nil {
		t.Fatalf("ListOrders: %v", err)
	}
	if len(lo.Orders) != 1 {
		t.Fatalf("alice should see exactly 1 order, got %d", len(lo.Orders))
	}
	if lo.Orders[0].Symbol != "SIM" {
		t.Errorf("unexpected symbol %s", lo.Orders[0].Symbol)
	}
}

// ─── limit cross fills & settles ─────────────────────────────────────────────

func TestLimitCrossFillsAndSettles(t *testing.T) {
	s := newTestServer(t)
	mustCreateAccount(t, s, "buyer", 100_000, nil)
	mustCreateAccount(t, s, "seller", 0, map[string]int64{"SIM": 10})

	// Seller rests a sell.
	sellerResp := submitLimit(t, s, "seller", "SIM", "SELL", 10, 100, 0)
	mustAccept(t, sellerResp)
	restingID := sellerResp.OrderId

	// Buyer crosses it fully.
	buyResp := submitLimit(t, s, "buyer", "SIM", "BUY", 10, 100, 0)
	mustAccept(t, buyResp)

	if buyResp.FilledQty != 10 {
		t.Errorf("FilledQty: want 10, got %d", buyResp.FilledQty)
	}
	if buyResp.CostTicks != 1000 {
		t.Errorf("CostTicks: want 1000, got %d", buyResp.CostTicks)
	}

	// Resting sell order removed from index.
	lo, _ := s.ListOrders(context.Background(), &market.ListOrdersRequest{AgentId: "seller"})
	for _, o := range lo.Orders {
		if o.OrderId == restingID {
			t.Error("resting sell should have been removed from ListOrders after full fill")
		}
	}

	// Ledger settled correctly.
	buyerPF := getPortfolio(t, s, "buyer")
	if buyerPF.Cash != 100_000-1000 {
		t.Errorf("buyer cash: want %d, got %d", 100_000-1000, buyerPF.Cash)
	}
	if positionFor(buyerPF, "SIM") != 10 {
		t.Errorf("buyer SIM shares: want 10, got %d", positionFor(buyerPF, "SIM"))
	}

	sellerPF := getPortfolio(t, s, "seller")
	if sellerPF.Cash != 1000 {
		t.Errorf("seller cash: want 1000, got %d", sellerPF.Cash)
	}
}

func TestPartialFillRestingOrderRemainsInListOrders(t *testing.T) {
	s := newTestServer(t)
	mustCreateAccount(t, s, "buyer", 100_000, nil)
	mustCreateAccount(t, s, "seller", 0, map[string]int64{"SIM": 20})

	// Seller rests 10 shares.
	sellerResp := submitLimit(t, s, "seller", "SIM", "SELL", 10, 100, 0)
	mustAccept(t, sellerResp)

	// Buyer aggresses for only 4 shares.
	buyResp := submitLimit(t, s, "buyer", "SIM", "BUY", 4, 100, 0)
	mustAccept(t, buyResp)
	if buyResp.FilledQty != 4 {
		t.Errorf("FilledQty: want 4, got %d", buyResp.FilledQty)
	}

	// Resting sell order should still appear with remaining qty 6.
	lo, err := s.ListOrders(context.Background(), &market.ListOrdersRequest{AgentId: "seller"})
	if err != nil {
		t.Fatalf("ListOrders: %v", err)
	}
	if len(lo.Orders) != 1 {
		t.Fatalf("expected 1 resting order for seller, got %d", len(lo.Orders))
	}
	if lo.Orders[0].Qty != 6 {
		t.Errorf("remaining qty: want 6, got %d", lo.Orders[0].Qty)
	}
}

// ─── CancelOrder ─────────────────────────────────────────────────────────────

func TestCancelOrderAccepted(t *testing.T) {
	s := newTestServer(t)
	mustCreateAccount(t, s, "alice", 100_000, nil)

	orderResp := submitLimit(t, s, "alice", "SIM", "BUY", 5, 100, 0)
	mustAccept(t, orderResp)

	cancelResp, err := s.CancelOrder(context.Background(), &market.CancelOrderRequest{
		OrderId: orderResp.OrderId,
		AgentId: "alice",
	})
	if err != nil {
		t.Fatalf("CancelOrder: %v", err)
	}
	if cancelResp.Status != market.CancelStatus_CANCEL_STATUS_CANCELED {
		t.Errorf("expected CANCELED, got %v reason=%q", cancelResp.Status, cancelResp.Reason)
	}

	// No longer in ListOrders.
	lo, _ := s.ListOrders(context.Background(), &market.ListOrdersRequest{AgentId: "alice"})
	if len(lo.Orders) != 0 {
		t.Errorf("expected 0 orders after cancel, got %d", len(lo.Orders))
	}

	// Reserved cash released.
	pf := getPortfolio(t, s, "alice")
	if pf.ReservedCash != 0 {
		t.Errorf("reserved cash after cancel: want 0, got %d", pf.ReservedCash)
	}
}

func TestCancelOrderUnknownID(t *testing.T) {
	s := newTestServer(t)
	resp, err := s.CancelOrder(context.Background(), &market.CancelOrderRequest{
		OrderId: "nonexistent",
		AgentId: "someone",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp.Status != market.CancelStatus_CANCEL_STATUS_NOT_FOUND {
		t.Errorf("expected NOT_FOUND, got %v", resp.Status)
	}
}

// ─── market buy ──────────────────────────────────────────────────────────────

func TestMarketBuyFillsAndSettles(t *testing.T) {
	s := newTestServer(t)
	mustCreateAccount(t, s, "buyer", 100_000, nil)
	mustCreateAccount(t, s, "seller", 0, map[string]int64{"SIM": 10})

	sellerResp := submitLimit(t, s, "seller", "SIM", "SELL", 10, 100, 0)
	mustAccept(t, sellerResp)
	restingID := sellerResp.OrderId

	resp, err := s.SubmitOrder(context.Background(), &market.SubmitOrderRequest{
		AgentId:         "buyer",
		Symbol:          "SIM",
		Side:            market.OrderSide_ORDER_SIDE_BUY,
		Type:            market.OrderType_ORDER_TYPE_MARKET,
		CashBudgetTicks: ptr(int64(2000)), // budget well above cost
	})
	if err != nil {
		t.Fatalf("SubmitOrder market buy: %v", err)
	}
	mustAccept(t, resp)
	if resp.FilledQty != 10 {
		t.Errorf("FilledQty: want 10, got %d", resp.FilledQty)
	}
	if resp.CostTicks != 1000 {
		t.Errorf("CostTicks: want 1000, got %d", resp.CostTicks)
	}

	// Resting sell removed from index.
	lo, _ := s.ListOrders(context.Background(), &market.ListOrdersRequest{AgentId: "seller"})
	for _, o := range lo.Orders {
		if o.OrderId == restingID {
			t.Error("resting sell should have been removed after market buy fill")
		}
	}

	// Buyer cash: 100_000 - 1000 = 99_000; unspent 1000 refunded.
	pf := getPortfolio(t, s, "buyer")
	if pf.Cash != 99_000 {
		t.Errorf("buyer cash: want 99000, got %d", pf.Cash)
	}
	if positionFor(pf, "SIM") != 10 {
		t.Errorf("buyer SIM: want 10, got %d", positionFor(pf, "SIM"))
	}
}

func TestMarketBuyNoLiquidity(t *testing.T) {
	s := newTestServer(t)
	mustCreateAccount(t, s, "buyer", 100_000, nil)

	resp, err := s.SubmitOrder(context.Background(), &market.SubmitOrderRequest{
		AgentId:         "buyer",
		Symbol:          "SIM",
		Side:            market.OrderSide_ORDER_SIDE_BUY,
		Type:            market.OrderType_ORDER_TYPE_MARKET,
		CashBudgetTicks: ptr(int64(5000)),
	})
	if err != nil {
		t.Fatalf("SubmitOrder: %v", err)
	}
	if resp.Status != market.OrderStatus_ORDER_STATUS_REJECTED {
		t.Errorf("expected REJECTED for no-liquidity market buy, got %v", resp.Status)
	}

	// Full budget refunded.
	pf := getPortfolio(t, s, "buyer")
	if pf.Cash != 100_000 {
		t.Errorf("buyer cash after no-fill: want 100000, got %d", pf.Cash)
	}
	if pf.ReservedCash != 0 {
		t.Errorf("reserved cash should be 0 after rejection, got %d", pf.ReservedCash)
	}
}

// ─── market sell ─────────────────────────────────────────────────────────────

func TestMarketSellFillsAndSettles(t *testing.T) {
	s := newTestServer(t)
	mustCreateAccount(t, s, "buyer", 100_000, nil)
	mustCreateAccount(t, s, "seller", 0, map[string]int64{"SIM": 10})

	// Buyer rests a bid.
	buyResp := submitLimit(t, s, "buyer", "SIM", "BUY", 10, 100, 0)
	mustAccept(t, buyResp)
	restingID := buyResp.OrderId

	// Seller hits with a market sell.
	resp, err := s.SubmitOrder(context.Background(), &market.SubmitOrderRequest{
		AgentId: "seller",
		Symbol:  "SIM",
		Side:    market.OrderSide_ORDER_SIDE_SELL,
		Type:    market.OrderType_ORDER_TYPE_MARKET,
		Qty:     10,
	})
	if err != nil {
		t.Fatalf("SubmitOrder market sell: %v", err)
	}
	mustAccept(t, resp)
	if resp.FilledQty != 10 {
		t.Errorf("FilledQty: want 10, got %d", resp.FilledQty)
	}

	// Resting buy removed.
	lo, _ := s.ListOrders(context.Background(), &market.ListOrdersRequest{AgentId: "buyer"})
	for _, o := range lo.Orders {
		if o.OrderId == restingID {
			t.Error("resting buy should have been removed after market sell fill")
		}
	}

	sellerPF := getPortfolio(t, s, "seller")
	if sellerPF.Cash != 1000 {
		t.Errorf("seller cash: want 1000, got %d", sellerPF.Cash)
	}
}

func TestMarketSellNoLiquidity(t *testing.T) {
	s := newTestServer(t)
	mustCreateAccount(t, s, "seller", 0, map[string]int64{"SIM": 10})

	resp, err := s.SubmitOrder(context.Background(), &market.SubmitOrderRequest{
		AgentId: "seller",
		Symbol:  "SIM",
		Side:    market.OrderSide_ORDER_SIDE_SELL,
		Type:    market.OrderType_ORDER_TYPE_MARKET,
		Qty:     10,
	})
	if err != nil {
		t.Fatalf("SubmitOrder: %v", err)
	}
	if resp.Status != market.OrderStatus_ORDER_STATUS_REJECTED {
		t.Errorf("expected REJECTED for no-liquidity market sell, got %v", resp.Status)
	}

	// Share reserve released.
	pf := getPortfolio(t, s, "seller")
	if reservedSharesFor(pf, "SIM") != 0 {
		t.Errorf("reserved shares should be 0 after rejection, got %d", reservedSharesFor(pf, "SIM"))
	}
}

func TestMarketSellPartialFillReleasesUnfilledReserve(t *testing.T) {
	s := newTestServer(t)
	mustCreateAccount(t, s, "buyer", 100_000, nil)
	mustCreateAccount(t, s, "seller", 0, map[string]int64{"SIM": 10})

	// Only 4 bids available.
	mustAccept(t, submitLimit(t, s, "buyer", "SIM", "BUY", 4, 100, 0))

	resp, err := s.SubmitOrder(context.Background(), &market.SubmitOrderRequest{
		AgentId: "seller",
		Symbol:  "SIM",
		Side:    market.OrderSide_ORDER_SIDE_SELL,
		Type:    market.OrderType_ORDER_TYPE_MARKET,
		Qty:     10,
	})
	if err != nil {
		t.Fatalf("SubmitOrder: %v", err)
	}
	mustAccept(t, resp)
	if resp.FilledQty != 4 {
		t.Errorf("FilledQty: want 4, got %d", resp.FilledQty)
	}

	// Unfilled reserve (6 shares) must be released.
	pf := getPortfolio(t, s, "seller")
	if reservedSharesFor(pf, "SIM") != 0 {
		t.Errorf("reserved shares should be 0 after partial sell, got %d", reservedSharesFor(pf, "SIM"))
	}
	// 6 shares remain in positions.
	if positionFor(pf, "SIM") != 6 {
		t.Errorf("seller remaining SIM: want 6, got %d", positionFor(pf, "SIM"))
	}
}

// ─── CreateAccount / GetPortfolio RPCs ───────────────────────────────────────

func TestCreateAccountDuplicate(t *testing.T) {
	s := newTestServer(t)
	mustCreateAccount(t, s, "alice", 1000, nil)

	resp, err := s.CreateAccount(context.Background(), &market.CreateAccountRequest{
		AgentId:   "alice",
		CashTicks: 9999,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp.Ok {
		t.Error("expected ok=false on duplicate create")
	}
	if resp.Reason == "" {
		t.Error("expected non-empty reason on duplicate create")
	}
}

func TestGetPortfolioUnknownAgent(t *testing.T) {
	s := newTestServer(t)
	resp, err := s.GetPortfolio(context.Background(), &market.GetPortfolioRequest{AgentId: "nobody"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp.Found {
		t.Error("expected found=false for unknown agent")
	}
}

// ─── ListOrders: nil / empty agent ───────────────────────────────────────────

func TestListOrdersEmptyAgentID(t *testing.T) {
	s := newTestServer(t)
	resp, err := s.ListOrders(context.Background(), &market.ListOrdersRequest{AgentId: ""})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(resp.Orders) != 0 {
		t.Errorf("expected empty list for blank agent_id, got %d", len(resp.Orders))
	}
}

// ─── orderIdx / reserved cash consistency ────────────────────────────────────

// TestReservedCashAfterLimitOrder verifies the ledger reserve matches the
// expected qty×price when a limit buy rests.
func TestReservedCashAfterLimitOrder(t *testing.T) {
	s := newTestServer(t)
	mustCreateAccount(t, s, "alice", 100_000, nil)

	mustAccept(t, submitLimit(t, s, "alice", "SIM", "BUY", 5, 200, 0))

	pf := getPortfolio(t, s, "alice")
	if pf.ReservedCash != 1000 { // 5 × 200
		t.Errorf("reserved cash: want 1000, got %d", pf.ReservedCash)
	}
}

// TestMultipleSymbolsListOrders ensures multi-symbol books are populated and
// queried correctly via ListOrders without a symbol filter.
func TestMultipleSymbolsListOrders(t *testing.T) {
	s := newTestServer(t)
	mustCreateAccount(t, s, "alice", 100_000, nil)

	mustAccept(t, submitLimit(t, s, "alice", "SIM", "BUY", 1, 100, 0))
	mustAccept(t, submitLimit(t, s, "alice", "FOO", "BUY", 1, 200, 0))
	mustAccept(t, submitLimit(t, s, "alice", "BAR", "BUY", 1, 300, 0))

	lo, err := s.ListOrders(context.Background(), &market.ListOrdersRequest{AgentId: "alice"})
	if err != nil {
		t.Fatalf("ListOrders: %v", err)
	}
	if len(lo.Orders) != 3 {
		t.Errorf("expected 3 resting orders, got %d", len(lo.Orders))
	}

	// Collect symbols returned.
	syms := make([]string, 0, len(lo.Orders))
	for _, o := range lo.Orders {
		syms = append(syms, o.Symbol)
	}
	sort.Strings(syms)
	want := []string{"BAR", "FOO", "SIM"}
	for i, w := range want {
		if syms[i] != w {
			t.Errorf("symbol[%d]: want %s, got %s", i, w, syms[i])
		}
	}
}
