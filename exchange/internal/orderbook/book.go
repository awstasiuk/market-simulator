package orderbook

import (
	"slices"
	"sort"
	"sync"
)

// ─── public types ─────────────────────────────────────────────────────────────

type Side int
type OrderType int

const (
	SideBuy Side = iota + 1
	SideSell
)

const (
	TypeLimit OrderType = iota + 1
	TypeMarket
)

type Order struct {
	ID         string
	AgentID    string
	Side       Side
	Type       OrderType
	Qty        int64
	PriceTicks int64
}

type Level struct {
	PriceTicks int64
	Qty        int64
}

type Trade struct {
	PriceTicks          int64
	Qty                 int64
	BuyOrderID          string
	SellOrderID         string
	BuyAgentID          string
	SellAgentID         string
	BuyLimitPriceTicks  int64 // limit price of the resting/aggressor buy order
	SellLimitPriceTicks int64 // limit price of the resting/aggressor sell order
}

// MatchResult is returned by Match, MatchMarketBuy, and MatchMarketSell.
type MatchResult struct {
	Trades []Trade
	// Rested is true when any unfilled quantity was added to the book.
	Rested bool
	// FullyFilledRestingOrderIDs contains IDs of previously-resting orders
	// that were completely consumed by this match. Callers use this to clean
	// up server-side indexes (e.g. orderIdx, portfolio reserves).
	FullyFilledRestingOrderIDs []string
	// SpentCashTicks is the total cash paid across all fills.
	// Non-zero only for MatchMarketBuy; for limit orders use trade prices directly.
	SpentCashTicks int64
	// UnfilledQty is the share quantity that could not be filled due to
	// insufficient contra-side liquidity. Set by MatchMarketSell only.
	UnfilledQty int64
}

// ─── levelQueue ───────────────────────────────────────────────────────────────

// levelQueue is a FIFO queue of orders at a single price level.
// A head index is used instead of reslicing so the dead prefix can be
// compacted explicitly and dequeue stays O(1) without reallocating.
type levelQueue struct {
	orders []Order
	head   int
}

func (q *levelQueue) push(o Order) { q.orders = append(q.orders, o) }

func (q *levelQueue) empty() bool { return q.head >= len(q.orders) }

func (q *levelQueue) front() Order { return q.orders[q.head] }

func (q *levelQueue) updateFront(o Order) { q.orders[q.head] = o }

func (q *levelQueue) dequeue() {
	q.head++
	// Compact when the dead prefix exceeds half the backing array.
	if q.head > 0 && q.head >= len(q.orders)/2 {
		q.orders = q.orders[q.head:]
		q.head = 0
	}
}

func (q *levelQueue) totalQty() int64 {
	var total int64
	for _, o := range q.orders[q.head:] {
		total += o.Qty
	}
	return total
}

// remove removes the order with orderID from the queue and returns it.
// slices.Delete shifts elements left from i+1; the head index remains valid.
func (q *levelQueue) remove(orderID string) (Order, bool) {
	for i := q.head; i < len(q.orders); i++ {
		if q.orders[i].ID == orderID {
			o := q.orders[i]
			q.orders = slices.Delete(q.orders, i, i+1)
			return o, true
		}
	}
	return Order{}, false
}

// ─── priceSide ────────────────────────────────────────────────────────────────

// priceSide holds one side (bids or asks) of the book.
//
// prices is maintained sorted at all times:
//   - asks: ascending  (prices[0] = best ask = lowest price)
//   - bids: descending (prices[0] = best bid = highest price)
//
// Insert/delete use binary search (O(log n)) + slices.Insert/Delete (O(k)
// shift, where k = distinct price levels, typically tens in this simulator).
// Iteration for matching and snapshots requires no sort.
type priceSide struct {
	prices []int64
	levels map[int64]*levelQueue
	asc    bool // true = asks (ascending), false = bids (descending)
}

func newPriceSide(asc bool) *priceSide {
	return &priceSide{
		levels: make(map[int64]*levelQueue),
		asc:    asc,
	}
}

// best returns the best price on this side, O(1).
func (s *priceSide) best() (int64, bool) {
	if len(s.prices) == 0 {
		return 0, false
	}
	return s.prices[0], true
}

func (s *priceSide) add(o Order) {
	q, ok := s.levels[o.PriceTicks]
	if !ok {
		q = &levelQueue{}
		s.levels[o.PriceTicks] = q
		idx := s.searchIdx(o.PriceTicks)
		s.prices = slices.Insert(s.prices, idx, o.PriceTicks)
	}
	q.push(o)
}

func (s *priceSide) pruneLevel(price int64) {
	delete(s.levels, price)
	idx := s.searchIdx(price)
	if idx < len(s.prices) && s.prices[idx] == price {
		s.prices = slices.Delete(s.prices, idx, idx+1)
	}
}

func (s *priceSide) searchIdx(p int64) int {
	if s.asc {
		return sort.Search(len(s.prices), func(i int) bool { return s.prices[i] >= p })
	}
	return sort.Search(len(s.prices), func(i int) bool { return s.prices[i] <= p })
}

// snapshot returns all levels in sorted order, O(n), no sort required.
func (s *priceSide) snapshot() []Level {
	if len(s.prices) == 0 {
		return nil
	}
	out := make([]Level, 0, len(s.prices))
	for _, p := range s.prices {
		out = append(out, Level{PriceTicks: p, Qty: s.levels[p].totalQty()})
	}
	return out
}

// ─── cancel index ─────────────────────────────────────────────────────────────

type orderRef struct {
	price int64
	side  Side
}

// ─── Book ─────────────────────────────────────────────────────────────────────

// Book is a price-time priority limit order book for a single symbol.
// All exported methods are thread-safe via an internal mutex.
type Book struct {
	symbol string
	mu     sync.Mutex
	bids   *priceSide
	asks   *priceSide
	// index maps resting order IDs to their book location for O(log n + m) cancel.
	index map[string]orderRef
}

func NewBook(symbol string) *Book {
	return &Book{
		symbol: symbol,
		bids:   newPriceSide(false),
		asks:   newPriceSide(true),
		index:  make(map[string]orderRef),
	}
}

// Match executes order against the opposing side using price-time priority.
// Any unfilled quantity is rested in the book.
// Complexity: O(m) where m = number of price levels crossed; no alloc or sort.
func (b *Book) Match(order Order) MatchResult {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.match(order)
}

// Cancel removes a resting order by ID.
// Returns the cancelled order (with its remaining Qty) and true if found and removed.
func (b *Book) Cancel(orderID string) (Order, bool) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.cancel(orderID)
}

// BestBid returns the highest bid price, O(1).
func (b *Book) BestBid() (int64, bool) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.bids.best()
}

// BestAsk returns the lowest ask price, O(1).
func (b *Book) BestAsk() (int64, bool) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.asks.best()
}

// SnapshotLevels returns (bids, asks) in sorted order.
// O(n) — prices is always maintained sorted; no sort on read.
func (b *Book) SnapshotLevels() (bids []Level, asks []Level) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.bids.snapshot(), b.asks.snapshot()
}

// GetOrder returns the current resting state of an order by ID, including its
// remaining quantity. Returns false if the order is not in this book.
func (b *Book) GetOrder(orderID string) (Order, bool) {
	b.mu.Lock()
	defer b.mu.Unlock()
	ref, ok := b.index[orderID]
	if !ok {
		return Order{}, false
	}
	q := b.sideOf(ref.side).levels[ref.price]
	if q == nil {
		return Order{}, false
	}
	for i := q.head; i < len(q.orders); i++ {
		if q.orders[i].ID == orderID {
			return q.orders[i], true
		}
	}
	return Order{}, false
}

// MatchMarketBuy executes a market buy by spending up to cashBudget ticks.
// Fills at best available ask prices; stops when budget is exhausted or the
// book has no more asks that can be afforded (i.e. price > remaining budget).
// The order never rests. SpentCashTicks in the result holds the total cash
// paid; the caller is responsible for refunding the unspent remainder.
func (b *Book) MatchMarketBuy(agentID, orderID string, cashBudget int64) MatchResult {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.matchMarketBuy(agentID, orderID, cashBudget)
}

// MatchMarketSell executes a market sell for qty shares at best available bid
// prices. The order never rests. UnfilledQty in the result holds the share
// quantity that could not be matched due to insufficient bid liquidity.
func (b *Book) MatchMarketSell(agentID, orderID string, qty int64) MatchResult {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.matchMarketSell(agentID, orderID, qty)
}

// ─── internal (book.mu must be held) ──────────────────────────────────────────

func (b *Book) match(order Order) MatchResult {
	if order.Qty <= 0 {
		return MatchResult{}
	}
	if order.Side == SideBuy {
		return b.matchAgainst(order, b.asks,
			func(restingPrice int64) bool { return restingPrice <= order.PriceTicks },
			func(r Order, qty int64) Trade {
				return Trade{
					PriceTicks:          r.PriceTicks,
					Qty:                 qty,
					BuyOrderID:          order.ID,
					SellOrderID:         r.ID,
					BuyAgentID:          order.AgentID,
					SellAgentID:         r.AgentID,
					BuyLimitPriceTicks:  order.PriceTicks,
					SellLimitPriceTicks: r.PriceTicks,
				}
			},
		)
	}
	return b.matchAgainst(order, b.bids,
		func(restingPrice int64) bool { return restingPrice >= order.PriceTicks },
		func(r Order, qty int64) Trade {
			return Trade{
				PriceTicks:          r.PriceTicks,
				Qty:                 qty,
				BuyOrderID:          r.ID,
				SellOrderID:         order.ID,
				BuyAgentID:          r.AgentID,
				SellAgentID:         order.AgentID,
				BuyLimitPriceTicks:  r.PriceTicks,
				SellLimitPriceTicks: order.PriceTicks,
			}
		},
	)
}

func (b *Book) matchAgainst(
	order Order,
	contra *priceSide,
	priceOK func(int64) bool,
	makeTrade func(Order, int64) Trade,
) MatchResult {
	var trades []Trade
	var fullyFilledRestingIDs []string

	for len(contra.prices) > 0 && order.Qty > 0 {
		bestPrice := contra.prices[0]
		if !priceOK(bestPrice) {
			break
		}
		q := contra.levels[bestPrice]
		for !q.empty() && order.Qty > 0 {
			resting := q.front()
			fill := minQty(order.Qty, resting.Qty)
			trades = append(trades, makeTrade(resting, fill))
			order.Qty -= fill
			resting.Qty -= fill
			if resting.Qty == 0 {
				fullyFilledRestingIDs = append(fullyFilledRestingIDs, resting.ID)
				delete(b.index, resting.ID)
				q.dequeue()
			} else {
				q.updateFront(resting)
				break
			}
		}
		if q.empty() {
			contra.pruneLevel(bestPrice)
		}
	}

	rested := false
	if order.Qty > 0 {
		b.sideOf(order.Side).add(order)
		b.index[order.ID] = orderRef{price: order.PriceTicks, side: order.Side}
		rested = true
	}

	return MatchResult{Trades: trades, Rested: rested, FullyFilledRestingOrderIDs: fullyFilledRestingIDs}
}

func (b *Book) cancel(orderID string) (Order, bool) {
	ref, ok := b.index[orderID]
	if !ok {
		return Order{}, false
	}
	s := b.sideOf(ref.side)
	q, ok := s.levels[ref.price]
	if !ok {
		return Order{}, false
	}
	o, ok := q.remove(orderID)
	if !ok {
		return Order{}, false
	}
	delete(b.index, orderID)
	if q.empty() {
		s.pruneLevel(ref.price)
	}
	return o, true
}

func (b *Book) sideOf(s Side) *priceSide {
	if s == SideBuy {
		return b.bids
	}
	return b.asks
}

func minQty(a, b int64) int64 {
	if a < b {
		return a
	}
	return b
}

// matchMarketBuy sweeps ask levels from best to worst, spending cashBudget.
// Stops when budget can no longer afford even one share at the current level.
func (b *Book) matchMarketBuy(agentID, orderID string, cashBudget int64) MatchResult {
	var trades []Trade
	var fullyFilled []string
	spent := int64(0)

	for len(b.asks.prices) > 0 && cashBudget > 0 {
		price := b.asks.prices[0]
		canBuy := cashBudget / price // integer: how many shares budget covers at this level
		if canBuy == 0 {
			break // cheapest ask is still too expensive for remaining budget
		}
		q := b.asks.levels[price]
		for !q.empty() && canBuy > 0 {
			resting := q.front()
			fill := minQty(canBuy, resting.Qty)
			trades = append(trades, Trade{
				PriceTicks:          price,
				Qty:                 fill,
				BuyOrderID:          orderID,
				SellOrderID:         resting.ID,
				BuyAgentID:          agentID,
				SellAgentID:         resting.AgentID,
				BuyLimitPriceTicks:  0, // no limit — server uses bulk settlement
				SellLimitPriceTicks: resting.PriceTicks,
			})
			cost := fill * price
			cashBudget -= cost
			spent += cost
			canBuy -= fill
			resting.Qty -= fill
			if resting.Qty == 0 {
				fullyFilled = append(fullyFilled, resting.ID)
				delete(b.index, resting.ID)
				q.dequeue()
			} else {
				q.updateFront(resting)
			}
		}
		if q.empty() {
			b.asks.pruneLevel(price)
		}
	}

	return MatchResult{
		Trades:                     trades,
		Rested:                     false,
		FullyFilledRestingOrderIDs: fullyFilled,
		SpentCashTicks:             spent,
	}
}

// matchMarketSell sweeps bid levels from best to worst, filling qty shares.
func (b *Book) matchMarketSell(agentID, orderID string, qty int64) MatchResult {
	var trades []Trade
	var fullyFilled []string
	remaining := qty

	for len(b.bids.prices) > 0 && remaining > 0 {
		price := b.bids.prices[0]
		q := b.bids.levels[price]
		for !q.empty() && remaining > 0 {
			resting := q.front()
			fill := minQty(remaining, resting.Qty)
			trades = append(trades, Trade{
				PriceTicks:          price,
				Qty:                 fill,
				BuyOrderID:          resting.ID,
				SellOrderID:         orderID,
				BuyAgentID:          resting.AgentID,
				SellAgentID:         agentID,
				BuyLimitPriceTicks:  resting.PriceTicks,
				SellLimitPriceTicks: 0, // no limit — server settles via ApplySellerFill
			})
			remaining -= fill
			resting.Qty -= fill
			if resting.Qty == 0 {
				fullyFilled = append(fullyFilled, resting.ID)
				delete(b.index, resting.ID)
				q.dequeue()
			} else {
				q.updateFront(resting)
			}
		}
		if q.empty() {
			b.bids.pruneLevel(price)
		}
	}

	return MatchResult{
		Trades:                     trades,
		Rested:                     false,
		FullyFilledRestingOrderIDs: fullyFilled,
		UnfilledQty:                remaining,
	}
}
