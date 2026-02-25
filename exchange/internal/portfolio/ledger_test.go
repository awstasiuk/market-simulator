package portfolio

import (
	"testing"
)

// ─── helpers ──────────────────────────────────────────────────────────────────

func newLedger() *Ledger { return NewLedger() }

func mustCreate(t *testing.T, l *Ledger, id string, cash int64, positions map[string]int64) {
	t.Helper()
	if err := l.CreateAccount(id, cash, positions); err != nil {
		t.Fatalf("CreateAccount(%q): %v", id, err)
	}
}

func mustPortfolio(t *testing.T, l *Ledger, id string) PortfolioSnapshot {
	t.Helper()
	snap, ok := l.Portfolio(id)
	if !ok {
		t.Fatalf("Portfolio(%q): account not found", id)
	}
	return snap
}

// ─── CreateAccount ────────────────────────────────────────────────────────────

func TestCreateAccountDuplicate(t *testing.T) {
	l := newLedger()
	mustCreate(t, l, "alice", 1000, nil)

	err := l.CreateAccount("alice", 9999, nil)
	if err == nil {
		t.Fatal("expected error on duplicate CreateAccount, got nil")
	}

	// Original balance must be unchanged.
	snap := mustPortfolio(t, l, "alice")
	if snap.Cash != 1000 {
		t.Errorf("cash after duplicate create: want 1000, got %d", snap.Cash)
	}
}

// ─── ReserveBuy ───────────────────────────────────────────────────────────────

func TestReserveBuyLocksCase(t *testing.T) {
	l := newLedger()
	mustCreate(t, l, "alice", 1000, nil)

	if err := l.ReserveBuy("alice", 3, 100); err != nil { // reserve 300
		t.Fatalf("ReserveBuy: %v", err)
	}

	snap := mustPortfolio(t, l, "alice")
	if snap.Cash != 1000 {
		t.Errorf("total cash should be unchanged: want 1000, got %d", snap.Cash)
	}
	if snap.ReservedCash != 300 {
		t.Errorf("reserved cash: want 300, got %d", snap.ReservedCash)
	}
	if snap.AvailableCash() != 700 {
		t.Errorf("available cash: want 700, got %d", snap.AvailableCash())
	}
}

func TestReserveBuyOverBalance(t *testing.T) {
	l := newLedger()
	mustCreate(t, l, "alice", 100, nil)

	err := l.ReserveBuy("alice", 5, 100) // needs 500, only 100 available
	if err == nil {
		t.Fatal("expected error for over-balance reserve, got nil")
	}

	snap := mustPortfolio(t, l, "alice")
	if snap.ReservedCash != 0 {
		t.Errorf("reserved cash should be 0 after failed reserve, got %d", snap.ReservedCash)
	}
	if snap.Cash != 100 {
		t.Errorf("total cash should be unchanged, got %d", snap.Cash)
	}
}

// ─── ReleaseBuy ───────────────────────────────────────────────────────────────

func TestReleaseBuyRestoresCash(t *testing.T) {
	l := newLedger()
	mustCreate(t, l, "alice", 1000, nil)
	if err := l.ReserveBuy("alice", 3, 100); err != nil {
		t.Fatalf("ReserveBuy: %v", err)
	}

	l.ReleaseBuy("alice", 3, 100) // release the same amount

	snap := mustPortfolio(t, l, "alice")
	if snap.ReservedCash != 0 {
		t.Errorf("reserved cash after release: want 0, got %d", snap.ReservedCash)
	}
	if snap.AvailableCash() != 1000 {
		t.Errorf("available cash after release: want 1000, got %d", snap.AvailableCash())
	}
}

// ─── ReserveSell ──────────────────────────────────────────────────────────────

func TestReserveSellLocksShares(t *testing.T) {
	l := newLedger()
	mustCreate(t, l, "bob", 0, map[string]int64{"SIM": 50})

	if err := l.ReserveSell("bob", "SIM", 20); err != nil {
		t.Fatalf("ReserveSell: %v", err)
	}

	snap := mustPortfolio(t, l, "bob")
	if snap.Positions["SIM"] != 50 {
		t.Errorf("total shares unchanged: want 50, got %d", snap.Positions["SIM"])
	}
	if snap.ReservedShares["SIM"] != 20 {
		t.Errorf("reserved shares: want 20, got %d", snap.ReservedShares["SIM"])
	}
	if snap.AvailableShares("SIM") != 30 {
		t.Errorf("available shares: want 30, got %d", snap.AvailableShares("SIM"))
	}
}

func TestReserveSellInsufficientShares(t *testing.T) {
	l := newLedger()
	mustCreate(t, l, "bob", 0, map[string]int64{"SIM": 5})

	err := l.ReserveSell("bob", "SIM", 10)
	if err == nil {
		t.Fatal("expected error for insufficient shares, got nil")
	}

	snap := mustPortfolio(t, l, "bob")
	if snap.ReservedShares["SIM"] != 0 {
		t.Errorf("reserved shares should be 0 after failed reserve, got %d", snap.ReservedShares["SIM"])
	}
}

// ─── ApplyBuyerFill / ApplySellerFill ─────────────────────────────────────────

func TestApplyBuyerAndSellerFill(t *testing.T) {
	l := newLedger()
	// Buyer has 1000 cash, limit price 120; trade executes at 100.
	mustCreate(t, l, "buyer", 1000, nil)
	if err := l.ReserveBuy("buyer", 5, 120); err != nil { // reserve 600
		t.Fatalf("ReserveBuy: %v", err)
	}

	// Seller has 10 SIM shares, reserved 5 of them.
	mustCreate(t, l, "seller", 0, map[string]int64{"SIM": 10})
	if err := l.ReserveSell("seller", "SIM", 5); err != nil {
		t.Fatalf("ReserveSell: %v", err)
	}

	// Fill 5 shares at price 100; buyer limit was 120 so surplus 20×5=100 is refunded.
	l.ApplyBuyerFill("buyer", "SIM", 5, 100, 120)
	l.ApplySellerFill("seller", "SIM", 5, 100)

	buyer := mustPortfolio(t, l, "buyer")
	// Cash deducted by actual cost: 1000 - 500 = 500.
	if buyer.Cash != 500 {
		t.Errorf("buyer cash: want 500, got %d", buyer.Cash)
	}
	// Reservation fully released (600 reserved, released 600).
	if buyer.ReservedCash != 0 {
		t.Errorf("buyer reserved cash: want 0, got %d", buyer.ReservedCash)
	}
	// Buyer received 5 shares.
	if buyer.Positions["SIM"] != 5 {
		t.Errorf("buyer SIM shares: want 5, got %d", buyer.Positions["SIM"])
	}

	seller := mustPortfolio(t, l, "seller")
	// Seller received 500 cash.
	if seller.Cash != 500 {
		t.Errorf("seller cash: want 500, got %d", seller.Cash)
	}
	// Shares deducted: 10 - 5 = 5.
	if seller.Positions["SIM"] != 5 {
		t.Errorf("seller SIM shares: want 5, got %d", seller.Positions["SIM"])
	}
	// Reservation cleared.
	if seller.ReservedShares["SIM"] != 0 {
		t.Errorf("seller reserved shares: want 0, got %d", seller.ReservedShares["SIM"])
	}
}

// ─── SettleMarketBuy ──────────────────────────────────────────────────────────

func TestSettleMarketBuy(t *testing.T) {
	l := newLedger()
	mustCreate(t, l, "buyer", 1000, nil)
	// Reserve entire budget of 500.
	if err := l.ReserveCash("buyer", 500); err != nil {
		t.Fatalf("ReserveCash: %v", err)
	}

	// Actual cost was 300; bought 3 shares; unspent 200 refunded.
	l.SettleMarketBuy("buyer", "SIM", 3, 300, 500)

	snap := mustPortfolio(t, l, "buyer")
	// Cash: 1000 - 300 = 700.
	if snap.Cash != 700 {
		t.Errorf("cash after settle: want 700, got %d", snap.Cash)
	}
	// No remaining reservation.
	if snap.ReservedCash != 0 {
		t.Errorf("reserved cash after settle: want 0, got %d", snap.ReservedCash)
	}
	// Shares credited.
	if snap.Positions["SIM"] != 3 {
		t.Errorf("SIM shares after settle: want 3, got %d", snap.Positions["SIM"])
	}
}

// ─── Portfolio snapshot ───────────────────────────────────────────────────────

func TestPortfolioSnapshot(t *testing.T) {
	l := newLedger()
	mustCreate(t, l, "carol", 2000, map[string]int64{"SIM": 100, "FOO": 50})

	// Reserve some of each.
	if err := l.ReserveBuy("carol", 4, 200); err != nil { // 800 reserved
		t.Fatalf("ReserveBuy: %v", err)
	}
	if err := l.ReserveSell("carol", "SIM", 30); err != nil {
		t.Fatalf("ReserveSell: %v", err)
	}

	snap := mustPortfolio(t, l, "carol")

	if snap.Cash != 2000 {
		t.Errorf("Cash: want 2000, got %d", snap.Cash)
	}
	if snap.ReservedCash != 800 {
		t.Errorf("ReservedCash: want 800, got %d", snap.ReservedCash)
	}
	if snap.AvailableCash() != 1200 {
		t.Errorf("AvailableCash: want 1200, got %d", snap.AvailableCash())
	}
	if snap.Positions["SIM"] != 100 {
		t.Errorf("SIM total: want 100, got %d", snap.Positions["SIM"])
	}
	if snap.ReservedShares["SIM"] != 30 {
		t.Errorf("SIM reserved: want 30, got %d", snap.ReservedShares["SIM"])
	}
	if snap.AvailableShares("SIM") != 70 {
		t.Errorf("SIM available: want 70, got %d", snap.AvailableShares("SIM"))
	}
	if snap.Positions["FOO"] != 50 {
		t.Errorf("FOO total: want 50, got %d", snap.Positions["FOO"])
	}
}

func TestPortfolioUnknownAgent(t *testing.T) {
	l := newLedger()
	_, ok := l.Portfolio("nobody")
	if ok {
		t.Error("Portfolio should return false for unknown agent")
	}
}
