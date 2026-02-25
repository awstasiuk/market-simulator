// Package portfolio provides a sharded, thread-safe ledger that tracks cash
// and share positions for all agents with escrow-based reserve semantics.
//
// # Reserve semantics
//
// When a resting order is placed the required outgoing asset is locked:
//   - Buy  limit: cash   reserve += qty × limitPrice
//   - Sell limit: shares reserve[symbol] += qty
//
// When a fill fires the reserve is released and the position is updated
// atomically. When an order is cancelled the reserve is released without a
// position change.
//
// # Sharding
//
// Accounts are spread across [numShards] independent shards keyed by a fast
// FNV-1a hash of agentID. Each shard carries its own RWMutex so agents in
// different shards never contend, giving near-linear read throughput.
package portfolio

import (
	"fmt"
	"hash/fnv"
	"sync"
)

const numShards = 64

// ─── account ─────────────────────────────────────────────────────────────────

// account holds the full financial state of one agent.
// Fields must not be read or written without the parent shard's lock.
type account struct {
	// cash is the agent's total cash balance (available + reserved).
	// Cash is denominated in price ticks (same unit as order prices).
	cash         int64
	reservedCash int64 // locked for pending buy orders

	// positions holds total share counts per symbol (available + reserved).
	positions map[string]int64
	// reservedShares holds the locked share count per symbol for pending sells.
	reservedShares map[string]int64
}

func newAccount(cash int64, positions map[string]int64) *account {
	pos := make(map[string]int64, len(positions))
	for sym, qty := range positions {
		if qty > 0 {
			pos[sym] = qty
		}
	}
	return &account{
		cash:           cash,
		positions:      pos,
		reservedShares: make(map[string]int64),
	}
}

func (a *account) availableCash() int64 { return a.cash - a.reservedCash }

func (a *account) availableShares(symbol string) int64 {
	return a.positions[symbol] - a.reservedShares[symbol]
}

// ─── shard ────────────────────────────────────────────────────────────────────

type shard struct {
	mu       sync.RWMutex
	accounts map[string]*account
}

// getOrCreate returns the account for agentID, creating an empty one if absent.
// Must be called with the shard write lock held.
func (sh *shard) getOrCreate(agentID string) *account {
	if a, ok := sh.accounts[agentID]; ok {
		return a
	}
	a := newAccount(0, nil)
	sh.accounts[agentID] = a
	return a
}

// ─── Ledger ───────────────────────────────────────────────────────────────────

// Ledger is a sharded, thread-safe portfolio store for many agents × symbols.
// All public methods are safe for concurrent use from arbitrary goroutines.
type Ledger struct {
	shards [numShards]shard
}

// NewLedger creates an empty Ledger.
func NewLedger() *Ledger {
	l := &Ledger{}
	for i := range l.shards {
		l.shards[i].accounts = make(map[string]*account)
	}
	return l
}

func (l *Ledger) shardOf(agentID string) *shard {
	h := fnv.New32a()
	h.Write([]byte(agentID))
	return &l.shards[h.Sum32()%numShards]
}

// ─── account management ───────────────────────────────────────────────────────

// CreateAccount seeds a new account with initial cash (in ticks) and positions.
// Returns an error if an account for agentID already exists.
func (l *Ledger) CreateAccount(agentID string, cashTicks int64, positions map[string]int64) error {
	sh := l.shardOf(agentID)
	sh.mu.Lock()
	defer sh.mu.Unlock()

	if _, exists := sh.accounts[agentID]; exists {
		return fmt.Errorf("account %q already exists", agentID)
	}
	sh.accounts[agentID] = newAccount(cashTicks, positions)
	return nil
}

// ─── reserve / release ────────────────────────────────────────────────────────

// ReserveBuy locks qty × priceTicks cash for a resting buy order.
// If the agent has no account it is auto-created with zero balances; the
// order will then be rejected because available cash = 0.
// Returns an error (reject reason) if insufficient cash is available.
func (l *Ledger) ReserveBuy(agentID string, qty, priceTicks int64) error {
	sh := l.shardOf(agentID)
	sh.mu.Lock()
	defer sh.mu.Unlock()

	a := sh.getOrCreate(agentID)
	required := qty * priceTicks
	if a.availableCash() < required {
		return fmt.Errorf("insufficient cash: need %d ticks, available %d", required, a.availableCash())
	}
	a.reservedCash += required
	return nil
}

// ReserveSell locks qty shares of symbol for a resting sell order.
// Auto-creates an empty account if absent (order will then be rejected).
// Returns an error (reject reason) if insufficient shares are available.
func (l *Ledger) ReserveSell(agentID, symbol string, qty int64) error {
	sh := l.shardOf(agentID)
	sh.mu.Lock()
	defer sh.mu.Unlock()

	a := sh.getOrCreate(agentID)
	available := a.availableShares(symbol)
	if available < qty {
		return fmt.Errorf("insufficient shares of %s: need %d, available %d", symbol, qty, available)
	}
	a.reservedShares[symbol] += qty
	return nil
}

// ReleaseBuy releases reserved cash on order cancel or full fill (called by
// the server when it determines a buy order is no longer resting).
// cancelledQty × priceTicks cash is released from the reservation.
func (l *Ledger) ReleaseBuy(agentID string, cancelledQty, priceTicks int64) {
	sh := l.shardOf(agentID)
	sh.mu.Lock()
	defer sh.mu.Unlock()

	if a, ok := sh.accounts[agentID]; ok {
		a.reservedCash -= cancelledQty * priceTicks
		if a.reservedCash < 0 {
			a.reservedCash = 0 // guard against accounting drift
		}
	}
}

// ReleaseSell releases reserved shares on order cancel.
func (l *Ledger) ReleaseSell(agentID, symbol string, cancelledQty int64) {
	sh := l.shardOf(agentID)
	sh.mu.Lock()
	defer sh.mu.Unlock()

	if a, ok := sh.accounts[agentID]; ok {
		a.reservedShares[symbol] -= cancelledQty
		if a.reservedShares[symbol] <= 0 {
			delete(a.reservedShares, symbol)
		}
	}
}

// ReserveCash locks a lump-sum cash amount for a market buy order where the
// per-share price is not known in advance. Call SettleMarketBuy afterward to
// atomically deduct the actual cost and release the unused reservation.
func (l *Ledger) ReserveCash(agentID string, cashTicks int64) error {
	sh := l.shardOf(agentID)
	sh.mu.Lock()
	defer sh.mu.Unlock()

	a := sh.getOrCreate(agentID)
	if a.availableCash() < cashTicks {
		return fmt.Errorf("insufficient cash: need %d ticks, available %d", cashTicks, a.availableCash())
	}
	a.reservedCash += cashTicks
	return nil
}

// SettleMarketBuy atomically closes out a market buy order:
//   - releases the full budget reservation (reserveBudgetTicks)
//   - deducts the actual cash spent (totalCostTicks)
//   - credits the received shares
//
// The net effect on available cash is: (reserveBudgetTicks - totalCostTicks)
// which equals the refund. Call this once after all fills are known.
func (l *Ledger) SettleMarketBuy(agentID, symbol string, qty, totalCostTicks, reserveBudgetTicks int64) {
	sh := l.shardOf(agentID)
	sh.mu.Lock()
	defer sh.mu.Unlock()

	a := sh.getOrCreate(agentID)
	a.reservedCash -= reserveBudgetTicks
	if a.reservedCash < 0 {
		a.reservedCash = 0
	}
	a.cash -= totalCostTicks
	if qty > 0 {
		a.positions[symbol] += qty
	}
}

// ─── fill settlement ──────────────────────────────────────────────────────────

// ApplyBuyerFill settles one fill for the buyer:
//   - Deducts qty × tradePriceTicks from cash (actual cost).
//   - Releases qty × buyLimitPriceTicks from the reservation (escrow amount).
//   - Credits qty shares of symbol.
//
// Called for both aggressor buys and resting buys that were hit.
func (l *Ledger) ApplyBuyerFill(agentID, symbol string, qty, tradePriceTicks, buyLimitPriceTicks int64) {
	sh := l.shardOf(agentID)
	sh.mu.Lock()
	defer sh.mu.Unlock()

	a := sh.getOrCreate(agentID)
	a.reservedCash -= qty * buyLimitPriceTicks
	if a.reservedCash < 0 {
		a.reservedCash = 0
	}
	a.cash -= qty * tradePriceTicks
	a.positions[symbol] += qty
}

// ApplySellerFill settles one fill for the seller:
//   - Credits qty × tradePriceTicks to cash.
//   - Releases qty shares from reservedShares.
//   - Debits qty shares of symbol from positions.
//
// Called for both aggressor sells and resting sells that were hit.
func (l *Ledger) ApplySellerFill(agentID, symbol string, qty, tradePriceTicks int64) {
	sh := l.shardOf(agentID)
	sh.mu.Lock()
	defer sh.mu.Unlock()

	a := sh.getOrCreate(agentID)
	a.reservedShares[symbol] -= qty
	if a.reservedShares[symbol] <= 0 {
		delete(a.reservedShares, symbol)
	}
	a.positions[symbol] -= qty
	if a.positions[symbol] <= 0 {
		delete(a.positions, symbol)
	}
	a.cash += qty * tradePriceTicks
}

// ─── read API ─────────────────────────────────────────────────────────────────

// PortfolioSnapshot is an immutable read-only view of an agent's portfolio.
type PortfolioSnapshot struct {
	// Cash is the total cash balance (available + reserved), in price ticks.
	Cash int64
	// ReservedCash is the portion of Cash locked for pending buy orders.
	ReservedCash int64
	// Positions maps symbol → total share count (available + reserved).
	Positions map[string]int64
	// ReservedShares maps symbol → share count locked for pending sell orders.
	ReservedShares map[string]int64
}

// AvailableCash returns the spendable portion of Cash.
func (p PortfolioSnapshot) AvailableCash() int64 { return p.Cash - p.ReservedCash }

// AvailableShares returns the shares of symbol not locked in open sell orders.
func (p PortfolioSnapshot) AvailableShares(symbol string) int64 {
	return p.Positions[symbol] - p.ReservedShares[symbol]
}

// Portfolio returns a deep-copy snapshot of the agent's portfolio.
// Returns (snapshot, true) if the account exists, (zero, false) otherwise.
func (l *Ledger) Portfolio(agentID string) (PortfolioSnapshot, bool) {
	sh := l.shardOf(agentID)
	sh.mu.RLock()
	defer sh.mu.RUnlock()

	a, ok := sh.accounts[agentID]
	if !ok {
		return PortfolioSnapshot{}, false
	}

	snap := PortfolioSnapshot{
		Cash:           a.cash,
		ReservedCash:   a.reservedCash,
		Positions:      make(map[string]int64, len(a.positions)),
		ReservedShares: make(map[string]int64, len(a.reservedShares)),
	}
	for sym, qty := range a.positions {
		snap.Positions[sym] = qty
	}
	for sym, qty := range a.reservedShares {
		snap.ReservedShares[sym] = qty
	}
	return snap, true
}

// AllPortfolios returns snapshots for every known agent.
// Iterates all shards; primarily intended for diagnostics or admin RPCs.
func (l *Ledger) AllPortfolios() map[string]PortfolioSnapshot {
	out := make(map[string]PortfolioSnapshot)
	for i := range l.shards {
		sh := &l.shards[i]
		sh.mu.RLock()
		for id, a := range sh.accounts {
			snap := PortfolioSnapshot{
				Cash:           a.cash,
				ReservedCash:   a.reservedCash,
				Positions:      make(map[string]int64, len(a.positions)),
				ReservedShares: make(map[string]int64, len(a.reservedShares)),
			}
			for sym, qty := range a.positions {
				snap.Positions[sym] = qty
			}
			for sym, qty := range a.reservedShares {
				snap.ReservedShares[sym] = qty
			}
			out[id] = snap
		}
		sh.mu.RUnlock()
	}
	return out
}
