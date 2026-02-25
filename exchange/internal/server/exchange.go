package server

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	"market-simulator/exchange/gen/market"
	"market-simulator/exchange/internal/orderbook"
	"market-simulator/exchange/internal/portfolio"
)

// ─── candle accumulator ───────────────────────────────────────────────────────

type liveCandle struct {
	symbol    string
	startTick int64
	open      int64
	high      int64
	low       int64
	close     int64
	volume    int64
	hasData   bool
}

func (c *liveCandle) addTrade(price, qty int64) {
	if !c.hasData {
		c.open, c.high, c.low = price, price, price
		c.hasData = true
	} else {
		if price > c.high {
			c.high = price
		}
		if price < c.low {
			c.low = price
		}
	}
	c.close = price
	c.volume += qty
}

func (c *liveCandle) flush(intervalMs int64) *market.CandleClosed {
	if !c.hasData {
		return nil
	}
	closed := &market.CandleClosed{
		Symbol:     c.symbol,
		IntervalMs: intervalMs,
		StartTick:  c.startTick,
		Open:       c.open,
		High:       c.high,
		Low:        c.low,
		Close:      c.close,
		Volume:     c.volume,
	}
	c.hasData = false
	c.volume = 0
	return closed
}

// ─── ExchangeServer ───────────────────────────────────────────────────────────

// orderMeta stores what the server needs about a resting order: enough to
// route a cancel to the right book and release the correct portfolio reserve.
type orderMeta struct {
	symbol     string
	agentID    string
	side       orderbook.Side
	priceTicks int64
}

type ExchangeServer struct {
	market.UnimplementedExchangeServiceServer

	tickInterval       time.Duration
	candleTickInterval int64 // close a candle every N ticks

	orderSeq atomic.Int64
	tickSeq  atomic.Int64
	tradeSeq atomic.Int64

	// booksMu guards only the books map; individual books are self-locking.
	booksMu sync.RWMutex
	books   map[string]*orderbook.Book

	// dirtyBooks tracks symbols whose books changed since the last tick flush.
	dirtyMu    sync.Mutex
	dirtyBooks map[string]struct{}

	// liveCandles accumulates OHLCV data per symbol, closed every candleTickInterval.
	candleMu    sync.Mutex
	liveCandles map[string]*liveCandle

	// candleHistory stores all closed candles per symbol for replay to new subscribers.
	// Capped at maxCandleHistory entries per symbol.
	historyMu     sync.RWMutex
	candleHistory map[string][]*market.CandleClosed

	// orderIdx maps resting order IDs to their metadata for O(1) cancel routing.
	orderIdxMu sync.RWMutex
	orderIdx   map[string]orderMeta

	// ledger tracks cash and position state (with escrow reserves) for all agents.
	ledger *portfolio.Ledger

	marketHub *marketDataHub
	tradeHub  *tradeHub

	stop chan struct{}
}

func NewExchangeServer(tickInterval time.Duration) *ExchangeServer {
	return NewExchangeServerWithOptions(tickInterval, 5)
}

func NewExchangeServerWithOptions(tickInterval time.Duration, candleTickInterval int64) *ExchangeServer {
	s := &ExchangeServer{
		tickInterval:       tickInterval,
		candleTickInterval: candleTickInterval,
		books:              make(map[string]*orderbook.Book),
		dirtyBooks:         make(map[string]struct{}),
		liveCandles:        make(map[string]*liveCandle),
		candleHistory:      make(map[string][]*market.CandleClosed),
		orderIdx:           make(map[string]orderMeta),
		ledger:             portfolio.NewLedger(),
		marketHub:          newMarketDataHub(),
		tradeHub:           newTradeHub(),
		stop:               make(chan struct{}),
	}
	go s.runTicker()
	return s
}

// Stop signals the ticker goroutine to exit cleanly.
func (s *ExchangeServer) Stop() {
	close(s.stop)
}

// ─── SubmitOrder ──────────────────────────────────────────────────────────────

func (s *ExchangeServer) SubmitOrder(
	_ context.Context,
	req *market.SubmitOrderRequest,
) (*market.SubmitOrderResponse, error) {
	if req == nil {
		return rejectOrder("missing request", 0), nil
	}
	if req.AgentId == "" || req.Symbol == "" {
		return rejectOrder("agent_id and symbol are required", req.ClientOrderId), nil
	}
	if req.Side == market.OrderSide_ORDER_SIDE_UNSPECIFIED ||
		req.Type == market.OrderType_ORDER_TYPE_UNSPECIFIED {
		return rejectOrder("side and type must be valid", req.ClientOrderId), nil
	}
	// Market buy uses cash_budget_ticks instead of qty; all other order types need qty > 0.
	if req.Type != market.OrderType_ORDER_TYPE_MARKET || req.Side != market.OrderSide_ORDER_SIDE_BUY {
		if req.Qty <= 0 {
			return rejectOrder("qty must be > 0", req.ClientOrderId), nil
		}
	}
	if req.Type == market.OrderType_ORDER_TYPE_LIMIT && req.PriceTicks == nil {
		return rejectOrder("price_ticks required for limit order", req.ClientOrderId), nil
	}
	if req.Type == market.OrderType_ORDER_TYPE_MARKET && req.Side == market.OrderSide_ORDER_SIDE_BUY {
		if req.CashBudgetTicks == nil || *req.CashBudgetTicks <= 0 {
			return rejectOrder("cash_budget_ticks required and must be > 0 for market buy", req.ClientOrderId), nil
		}
	}
	if req.Type == market.OrderType_ORDER_TYPE_MARKET && req.Side == market.OrderSide_ORDER_SIDE_SELL {
		if req.Qty <= 0 {
			return rejectOrder("qty must be > 0 for market sell", req.ClientOrderId), nil
		}
	}

	side := toBookSide(req.Side)
	book := s.getOrCreateBook(req.Symbol)
	orderID := fmt.Sprintf("o-%d", s.orderSeq.Add(1))

	// ─── market orders ────────────────────────────────────────────────────────
	if req.Type == market.OrderType_ORDER_TYPE_MARKET {
		if side == orderbook.SideBuy {
			return s.submitMarketBuy(req, orderID, book)
		}
		return s.submitMarketSell(req, orderID, book)
	}

	// ─── limit order (original path) ─────────────────────────────────────────
	priceTicks := extractPriceTicks(req)

	// Reserve outgoing assets before touching the book. Rejects orders that
	// exceed the agent's available balance, enforcing solvency.
	switch side {
	case orderbook.SideBuy:
		if err := s.ledger.ReserveBuy(req.AgentId, req.Qty, priceTicks); err != nil {
			return rejectOrder(err.Error(), req.ClientOrderId), nil
		}
	case orderbook.SideSell:
		if err := s.ledger.ReserveSell(req.AgentId, req.Symbol, req.Qty); err != nil {
			return rejectOrder(err.Error(), req.ClientOrderId), nil
		}
	}

	order := orderbook.Order{
		ID:         orderID,
		AgentID:    req.AgentId,
		Side:       side,
		Type:       toBookType(req.Type),
		Qty:        req.Qty,
		PriceTicks: priceTicks,
	}

	result := book.Match(order) // Book is self-locking; no server-level lock needed.

	if len(result.Trades) > 0 {
		currentTick := s.tickSeq.Load()
		for _, trade := range result.Trades {
			// Settle both sides of the fill in the ledger.
			s.ledger.ApplyBuyerFill(trade.BuyAgentID, req.Symbol, trade.Qty, trade.PriceTicks, trade.BuyLimitPriceTicks)
			s.ledger.ApplySellerFill(trade.SellAgentID, req.Symbol, trade.Qty, trade.PriceTicks)
			tradeID := fmt.Sprintf("t-%d", s.tradeSeq.Add(1))
			s.tradeHub.Broadcast(&market.TradeEvent{
				TradeId:     tradeID,
				Symbol:      req.Symbol,
				PriceTicks:  trade.PriceTicks,
				Qty:         trade.Qty,
				BuyOrderId:  trade.BuyOrderID,
				SellOrderId: trade.SellOrderID,
				Tick:        currentTick,
			})
			s.updateCandle(req.Symbol, trade.PriceTicks, trade.Qty)
		}
		// Mark dirty so runTicker broadcasts one book snapshot this tick.
		s.markDirty(req.Symbol)
	}

	// Remove fully-consumed resting orders from the server index.
	if len(result.FullyFilledRestingOrderIDs) > 0 {
		s.orderIdxMu.Lock()
		for _, id := range result.FullyFilledRestingOrderIDs {
			delete(s.orderIdx, id)
		}
		s.orderIdxMu.Unlock()
	}

	// Index the order if it was rested so CancelOrder can route to the right book.
	if result.Rested {
		s.orderIdxMu.Lock()
		s.orderIdx[orderID] = orderMeta{
			symbol:     req.Symbol,
			agentID:    req.AgentId,
			side:       side,
			priceTicks: priceTicks,
		}
		s.orderIdxMu.Unlock()
	}

	// Compute fill totals for the response.
	var filledQty, costTicks int64
	for _, t := range result.Trades {
		filledQty += t.Qty
		costTicks += t.Qty * t.PriceTicks
	}

	return &market.SubmitOrderResponse{
		OrderId:       orderID,
		Status:        market.OrderStatus_ORDER_STATUS_ACCEPTED,
		FilledQty:     filledQty,
		CostTicks:     costTicks,
		ClientOrderId: req.ClientOrderId,
	}, nil
}

// ─── CancelOrder ──────────────────────────────────────────────────────────────

func (s *ExchangeServer) CancelOrder(
	_ context.Context,
	req *market.CancelOrderRequest,
) (*market.CancelOrderResponse, error) {
	if req == nil || req.OrderId == "" {
		return &market.CancelOrderResponse{
			Status: market.CancelStatus_CANCEL_STATUS_REJECTED,
			Reason: "order_id is required",
		}, nil
	}

	s.orderIdxMu.RLock()
	meta, ok := s.orderIdx[req.OrderId]
	s.orderIdxMu.RUnlock()

	if !ok {
		return &market.CancelOrderResponse{
			Status: market.CancelStatus_CANCEL_STATUS_NOT_FOUND,
			Reason: "order not found",
		}, nil
	}

	book := s.getBook(meta.symbol)
	if book == nil {
		return &market.CancelOrderResponse{
			Status: market.CancelStatus_CANCEL_STATUS_NOT_FOUND,
			Reason: "order not found in book",
		}, nil
	}
	cancelled, found := book.Cancel(req.OrderId)
	if !found {
		return &market.CancelOrderResponse{
			Status: market.CancelStatus_CANCEL_STATUS_NOT_FOUND,
			Reason: "order not found in book",
		}, nil
	}

	// Release the portfolio reservation for the cancelled remaining quantity.
	switch meta.side {
	case orderbook.SideBuy:
		s.ledger.ReleaseBuy(meta.agentID, cancelled.Qty, meta.priceTicks)
	case orderbook.SideSell:
		s.ledger.ReleaseSell(meta.agentID, meta.symbol, cancelled.Qty)
	}

	s.orderIdxMu.Lock()
	delete(s.orderIdx, req.OrderId)
	s.orderIdxMu.Unlock()

	s.markDirty(meta.symbol)

	return &market.CancelOrderResponse{
		Status: market.CancelStatus_CANCEL_STATUS_CANCELED,
	}, nil
}

// submitMarketBuy executes a market buy order: sweep asks with a cash budget,
// settle actual cost, and refund any unspent budget.
func (s *ExchangeServer) submitMarketBuy(
	req *market.SubmitOrderRequest,
	orderID string,
	book *orderbook.Book,
) (*market.SubmitOrderResponse, error) {
	budget := req.GetCashBudgetTicks()

	if err := s.ledger.ReserveCash(req.AgentId, budget); err != nil {
		return rejectOrder(err.Error(), req.ClientOrderId), nil
	}

	result := book.MatchMarketBuy(req.AgentId, orderID, budget)

	if len(result.Trades) > 0 {
		currentTick := s.tickSeq.Load()
		var totalQty int64
		for _, trade := range result.Trades {
			// For market buy the buyer is settled in bulk by SettleMarketBuy below;
			// only settle the seller side per fill here.
			s.ledger.ApplySellerFill(trade.SellAgentID, req.Symbol, trade.Qty, trade.PriceTicks)
			tradeID := fmt.Sprintf("t-%d", s.tradeSeq.Add(1))
			s.tradeHub.Broadcast(&market.TradeEvent{
				TradeId:     tradeID,
				Symbol:      req.Symbol,
				PriceTicks:  trade.PriceTicks,
				Qty:         trade.Qty,
				BuyOrderId:  trade.BuyOrderID,
				SellOrderId: trade.SellOrderID,
				Tick:        currentTick,
			})
			s.updateCandle(req.Symbol, trade.PriceTicks, trade.Qty)
			totalQty += trade.Qty
		}
		s.markDirty(req.Symbol)

		// Clean up fully filled resting orders from the index.
		if len(result.FullyFilledRestingOrderIDs) > 0 {
			s.orderIdxMu.Lock()
			for _, id := range result.FullyFilledRestingOrderIDs {
				delete(s.orderIdx, id)
			}
			s.orderIdxMu.Unlock()
		}

		// Atomically release the full reservation and deduct actual cost.
		s.ledger.SettleMarketBuy(req.AgentId, req.Symbol, totalQty, result.SpentCashTicks, budget)

		return &market.SubmitOrderResponse{
			OrderId:       orderID,
			Status:        market.OrderStatus_ORDER_STATUS_ACCEPTED,
			FilledQty:     totalQty,
			CostTicks:     result.SpentCashTicks,
			ClientOrderId: req.ClientOrderId,
		}, nil
	}

	// No fills at all — release the full reservation and reject.
	s.ledger.SettleMarketBuy(req.AgentId, req.Symbol, 0, 0, budget)
	return rejectOrder("no liquidity: no asks available", req.ClientOrderId), nil
}

// submitMarketSell executes a market sell order: sweep bids for the requested
// qty, accept if any fill occurred, and release the reserve for unfilled shares.
func (s *ExchangeServer) submitMarketSell(
	req *market.SubmitOrderRequest,
	orderID string,
	book *orderbook.Book,
) (*market.SubmitOrderResponse, error) {
	if err := s.ledger.ReserveSell(req.AgentId, req.Symbol, req.Qty); err != nil {
		return rejectOrder(err.Error(), req.ClientOrderId), nil
	}

	result := book.MatchMarketSell(req.AgentId, orderID, req.Qty)

	if len(result.Trades) == 0 {
		// Zero liquidity — release full reserve and reject.
		s.ledger.ReleaseSell(req.AgentId, req.Symbol, req.Qty)
		return rejectOrder("no liquidity: no bids available", req.ClientOrderId), nil
	}

	currentTick := s.tickSeq.Load()
	var cashReceived int64
	for _, trade := range result.Trades {
		s.ledger.ApplyBuyerFill(trade.BuyAgentID, req.Symbol, trade.Qty, trade.PriceTicks, trade.BuyLimitPriceTicks)
		s.ledger.ApplySellerFill(trade.SellAgentID, req.Symbol, trade.Qty, trade.PriceTicks)
		tradeID := fmt.Sprintf("t-%d", s.tradeSeq.Add(1))
		s.tradeHub.Broadcast(&market.TradeEvent{
			TradeId:     tradeID,
			Symbol:      req.Symbol,
			PriceTicks:  trade.PriceTicks,
			Qty:         trade.Qty,
			BuyOrderId:  trade.BuyOrderID,
			SellOrderId: trade.SellOrderID,
			Tick:        currentTick,
		})
		s.updateCandle(req.Symbol, trade.PriceTicks, trade.Qty)
		cashReceived += trade.Qty * trade.PriceTicks
	}
	s.markDirty(req.Symbol)

	// Clean up fully filled resting orders from the index.
	if len(result.FullyFilledRestingOrderIDs) > 0 {
		s.orderIdxMu.Lock()
		for _, id := range result.FullyFilledRestingOrderIDs {
			delete(s.orderIdx, id)
		}
		s.orderIdxMu.Unlock()
	}

	// Release the reserve for shares that were not filled.
	if result.UnfilledQty > 0 {
		s.ledger.ReleaseSell(req.AgentId, req.Symbol, result.UnfilledQty)
	}

	filledQty := req.Qty - result.UnfilledQty
	return &market.SubmitOrderResponse{
		OrderId:       orderID,
		Status:        market.OrderStatus_ORDER_STATUS_ACCEPTED,
		FilledQty:     filledQty,
		CostTicks:     cashReceived,
		ClientOrderId: req.ClientOrderId,
	}, nil
}

// ─── account / portfolio RPCs ─────────────────────────────────────────────────

// CreateAccount seeds a new agent account with initial cash and positions.
// Returns ok=false (not an RPC error) if the account already exists or the
// request is malformed, so callers can handle it without error-code gymnastics.
func (s *ExchangeServer) CreateAccount(
	_ context.Context,
	req *market.CreateAccountRequest,
) (*market.CreateAccountResponse, error) {
	if req == nil || req.AgentId == "" {
		return &market.CreateAccountResponse{Ok: false, Reason: "agent_id is required"}, nil
	}
	positions := make(map[string]int64, len(req.Positions))
	for _, p := range req.Positions {
		if p.Symbol != "" && p.Qty > 0 {
			positions[p.Symbol] = p.Qty
		}
	}
	if err := s.ledger.CreateAccount(req.AgentId, req.CashTicks, positions); err != nil {
		return &market.CreateAccountResponse{Ok: false, Reason: err.Error()}, nil
	}
	return &market.CreateAccountResponse{Ok: true}, nil
}

// GetPortfolio returns a live snapshot of an agent's cash and share positions.
func (s *ExchangeServer) GetPortfolio(
	_ context.Context,
	req *market.GetPortfolioRequest,
) (*market.GetPortfolioResponse, error) {
	if req == nil || req.AgentId == "" {
		return &market.GetPortfolioResponse{Found: false}, nil
	}
	snap, ok := s.ledger.Portfolio(req.AgentId)
	if !ok {
		return &market.GetPortfolioResponse{Found: false}, nil
	}
	resp := &market.GetPortfolioResponse{
		Found:        true,
		Cash:         snap.Cash,
		ReservedCash: snap.ReservedCash,
	}
	for sym, qty := range snap.Positions {
		resp.Positions = append(resp.Positions, &market.PositionEntry{Symbol: sym, Qty: qty})
	}
	for sym, qty := range snap.ReservedShares {
		resp.ReservedShares = append(resp.ReservedShares, &market.PositionEntry{Symbol: sym, Qty: qty})
	}
	return resp, nil
}

// ListOrders returns the currently resting limit orders for an agent.
// If req.Symbol is non-empty, only orders for that symbol are returned.
func (s *ExchangeServer) ListOrders(
	_ context.Context,
	req *market.ListOrdersRequest,
) (*market.ListOrdersResponse, error) {
	if req == nil || req.AgentId == "" {
		return &market.ListOrdersResponse{}, nil
	}

	// Collect matching order IDs from the index under read lock.
	type candidate struct {
		id   string
		meta orderMeta
	}
	var candidates []candidate
	s.orderIdxMu.RLock()
	for id, meta := range s.orderIdx {
		if meta.agentID != req.AgentId {
			continue
		}
		if req.Symbol != "" && meta.symbol != req.Symbol {
			continue
		}
		candidates = append(candidates, candidate{id: id, meta: meta})
	}
	s.orderIdxMu.RUnlock()

	// For each candidate, look up its current resting qty in the book.
	resp := &market.ListOrdersResponse{}
	for _, c := range candidates {
		book := s.getBook(c.meta.symbol)
		if book == nil {
			continue
		}
		order, ok := book.GetOrder(c.id)
		if !ok {
			continue
		}
		side := market.OrderSide_ORDER_SIDE_BUY
		if c.meta.side == orderbook.SideSell {
			side = market.OrderSide_ORDER_SIDE_SELL
		}
		resp.Orders = append(resp.Orders, &market.RestingOrder{
			OrderId:    c.id,
			Symbol:     c.meta.symbol,
			Side:       side,
			Qty:        order.Qty,
			PriceTicks: c.meta.priceTicks,
		})
	}
	return resp, nil
}

// ─── streaming RPCs ───────────────────────────────────────────────────────────────────────────────────────────────────────────────────────────────────

func (s *ExchangeServer) StreamMarketData(
	req *market.MarketDataRequest,
	stream market.ExchangeService_StreamMarketDataServer,
) error {
	if req == nil {
		return fmt.Errorf("missing request")
	}

	// Subscribe before taking any snapshots so we don't miss events that arrive
	// between the snapshot read and entering the receive loop.
	subID, events := s.marketHub.Subscribe()
	defer s.marketHub.Unsubscribe(subID)

	// ── initial book snapshot ─────────────────────────────────────────────────
	if req.IncludeOrderBook {
		s.booksMu.RLock()
		var symbols []string
		if req.Symbol != "" {
			symbols = []string{req.Symbol}
		} else {
			for sym := range s.books {
				symbols = append(symbols, sym)
			}
		}
		s.booksMu.RUnlock()
		for _, sym := range symbols {
			book := s.getBook(sym)
			if book == nil {
				continue
			}
			bids, asks := book.SnapshotLevels()
			if err := stream.Send(&market.MarketDataEvent{
				Payload: &market.MarketDataEvent_OrderBook{
					OrderBook: &market.OrderBookUpdate{
						Symbol: sym,
						Bids:   toProtoLevels(bids),
						Asks:   toProtoLevels(asks),
					},
				},
			}); err != nil {
				return err
			}
		}
	}

	// ── historical candle replay ──────────────────────────────────────────────
	if req.IncludeCandles {
		s.historyMu.RLock()
		var toReplay []*market.CandleClosed
		if req.Symbol != "" {
			hist := s.candleHistory[req.Symbol]
			toReplay = make([]*market.CandleClosed, len(hist))
			copy(toReplay, hist)
		} else {
			for _, hist := range s.candleHistory {
				toReplay = append(toReplay, hist...)
			}
		}
		s.historyMu.RUnlock()
		for _, c := range toReplay {
			if err := stream.Send(&market.MarketDataEvent{
				Payload: &market.MarketDataEvent_Candle{Candle: c},
			}); err != nil {
				return err
			}
		}
	}

	for {
		select {
		case <-stream.Context().Done():
			return stream.Context().Err()
		case event, ok := <-events:
			if !ok {
				return nil
			}
			if !shouldSendMarketEvent(req, event) {
				continue
			}
			if err := stream.Send(event); err != nil {
				return err
			}
		}
	}
}

func (s *ExchangeServer) StreamTrades(
	req *market.TradeStreamRequest,
	stream market.ExchangeService_StreamTradesServer,
) error {
	if req == nil {
		return fmt.Errorf("missing request")
	}

	subID, events := s.tradeHub.Subscribe()
	defer s.tradeHub.Unsubscribe(subID)

	for {
		select {
		case <-stream.Context().Done():
			return stream.Context().Err()
		case event, ok := <-events:
			if !ok {
				return nil
			}
			if req.Symbol != "" && event.Symbol != req.Symbol {
				continue
			}
			if err := stream.Send(event); err != nil {
				return err
			}
		}
	}
}

// ─── ticker ───────────────────────────────────────────────────────────────────

func (s *ExchangeServer) runTicker() {
	ticker := time.NewTicker(s.tickInterval)
	defer ticker.Stop()

	for {
		select {
		case <-s.stop:
			return
		case tickTime := <-ticker.C:
			tick := s.tickSeq.Add(1)

			// 1. Emit tick event.
			s.marketHub.Broadcast(&market.MarketDataEvent{
				Payload: &market.MarketDataEvent_Tick{
					Tick: &market.TickEvent{
						Tick:             tick,
						ServerTimeUnixMs: tickTime.UnixMilli(),
					},
				},
			})

			// 2. Flush one OrderBookUpdate per dirty symbol.
			s.flushDirtyBooks()

			// 3. Close and emit candles every candleTickInterval ticks.
			if tick%s.candleTickInterval == 0 {
				intervalMs := int64(s.tickInterval/time.Millisecond) * s.candleTickInterval
				s.flushCandles(tick, intervalMs)
			}
		}
	}
}

// flushDirtyBooks snapshots and broadcasts one OrderBookUpdate per dirty symbol,
// then clears the dirty set. Called exclusively from runTicker.
func (s *ExchangeServer) flushDirtyBooks() {
	s.dirtyMu.Lock()
	dirty := s.dirtyBooks
	s.dirtyBooks = make(map[string]struct{})
	s.dirtyMu.Unlock()

	for symbol := range dirty {
		book := s.getBook(symbol)
		if book == nil {
			continue
		}
		bids, asks := book.SnapshotLevels()
		s.marketHub.Broadcast(&market.MarketDataEvent{
			Payload: &market.MarketDataEvent_OrderBook{
				OrderBook: &market.OrderBookUpdate{
					Symbol: symbol,
					Bids:   toProtoLevels(bids),
					Asks:   toProtoLevels(asks),
				},
			},
		})
	}
}

// maxCandleHistory is the maximum number of closed candles retained per symbol
// for replay to newly connecting subscribers.
const maxCandleHistory = 500

// flushCandles closes all live candles that have data, broadcasts CandleClosed,
// and appends the result to the per-symbol history buffer.
func (s *ExchangeServer) flushCandles(tick, intervalMs int64) {
	s.candleMu.Lock()
	defer s.candleMu.Unlock()

	for symbol, c := range s.liveCandles {
		closed := c.flush(intervalMs)
		if closed == nil {
			continue
		}
		s.marketHub.Broadcast(&market.MarketDataEvent{
			Payload: &market.MarketDataEvent_Candle{
				Candle: closed,
			},
		})
		// Append to history, capping at maxCandleHistory entries.
		s.historyMu.Lock()
		hist := append(s.candleHistory[symbol], closed)
		if len(hist) > maxCandleHistory {
			hist = hist[len(hist)-maxCandleHistory:]
		}
		s.candleHistory[symbol] = hist
		s.historyMu.Unlock()
		// Reset for next period.
		c.startTick = tick + 1
		s.liveCandles[symbol] = c
	}
}

// ─── helpers ──────────────────────────────────────────────────────────────────

func (s *ExchangeServer) markDirty(symbol string) {
	s.dirtyMu.Lock()
	s.dirtyBooks[symbol] = struct{}{}
	s.dirtyMu.Unlock()
}

func (s *ExchangeServer) updateCandle(symbol string, priceTicks, qty int64) {
	s.candleMu.Lock()
	defer s.candleMu.Unlock()

	c, ok := s.liveCandles[symbol]
	if !ok {
		startTick := s.tickSeq.Load()
		c = &liveCandle{symbol: symbol, startTick: startTick}
		s.liveCandles[symbol] = c
	}
	c.addTrade(priceTicks, qty)
}

func (s *ExchangeServer) getOrCreateBook(symbol string) *orderbook.Book {
	// Fast path: book already exists.
	s.booksMu.RLock()
	book, ok := s.books[symbol]
	s.booksMu.RUnlock()
	if ok {
		return book
	}

	// Slow path: create under write lock.
	s.booksMu.Lock()
	defer s.booksMu.Unlock()
	if book, ok = s.books[symbol]; ok {
		return book // another goroutine created it between locks
	}
	book = orderbook.NewBook(symbol)
	s.books[symbol] = book
	return book
}

func (s *ExchangeServer) getBook(symbol string) *orderbook.Book {
	s.booksMu.RLock()
	defer s.booksMu.RUnlock()
	return s.books[symbol]
}

func rejectOrder(reason string, clientOrderID int64) *market.SubmitOrderResponse {
	return &market.SubmitOrderResponse{
		Status:        market.OrderStatus_ORDER_STATUS_REJECTED,
		Reason:        reason,
		ClientOrderId: clientOrderID,
	}
}

func shouldSendMarketEvent(req *market.MarketDataRequest, event *market.MarketDataEvent) bool {
	switch payload := event.Payload.(type) {
	case *market.MarketDataEvent_Tick:
		// TickEvent is a global heartbeat with no symbol field; always forward.
		_ = payload
		return true
	case *market.MarketDataEvent_OrderBook:
		if !req.IncludeOrderBook {
			return false
		}
		if req.Symbol == "" {
			return true
		}
		return payload.OrderBook.Symbol == req.Symbol
	case *market.MarketDataEvent_Candle:
		if !req.IncludeCandles {
			return false
		}
		if req.Symbol == "" {
			return true
		}
		return payload.Candle.Symbol == req.Symbol
	default:
		return false
	}
}

func toBookSide(side market.OrderSide) orderbook.Side {
	switch side {
	case market.OrderSide_ORDER_SIDE_BUY:
		return orderbook.SideBuy
	case market.OrderSide_ORDER_SIDE_SELL:
		return orderbook.SideSell
	default:
		return orderbook.SideBuy
	}
}

func toBookType(orderType market.OrderType) orderbook.OrderType {
	switch orderType {
	case market.OrderType_ORDER_TYPE_LIMIT:
		return orderbook.TypeLimit
	case market.OrderType_ORDER_TYPE_MARKET:
		return orderbook.TypeMarket
	default:
		return orderbook.TypeLimit
	}
}

func extractPriceTicks(req *market.SubmitOrderRequest) int64 {
	if req.PriceTicks == nil {
		return 0
	}
	return *req.PriceTicks
}

func toProtoLevels(levels []orderbook.Level) []*market.PriceLevel {
	if len(levels) == 0 {
		return nil
	}
	out := make([]*market.PriceLevel, 0, len(levels))
	for _, level := range levels {
		out = append(out, &market.PriceLevel{
			PriceTicks: level.PriceTicks,
			Qty:        level.Qty,
		})
	}
	return out
}
