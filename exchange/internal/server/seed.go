package server

import (
	"encoding/json"
	"fmt"
	"os"
)

// seedEntry is the JSON shape for one account record in a seed file.
type seedEntry struct {
	AgentID   string           `json:"agent_id"`
	CashTicks int64            `json:"cash_ticks"`
	Positions map[string]int64 `json:"positions,omitempty"`
}

// LoadSeedFile reads a JSON array of account definitions and creates each
// account in the portfolio ledger. Existing accounts cause an error; use this
// exclusively at startup before any trading begins.
//
// Example seed file format:
//
//	[
//	  { "agent_id": "alice", "cash_ticks": 1000000, "positions": {"SIM": 500} },
//	  { "agent_id": "bob",   "cash_ticks":  500000 }
//	]
//
// cash_ticks is denominated in the same price-tick units used by orders,
// so a value of 1000000 equals $10,000.00 at 100 ticks per dollar.
func (s *ExchangeServer) LoadSeedFile(path string) error {
	raw, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("seed file: %w", err)
	}

	var entries []seedEntry
	if err := json.Unmarshal(raw, &entries); err != nil {
		return fmt.Errorf("seed file parse error: %w", err)
	}

	for i, e := range entries {
		if e.AgentID == "" {
			return fmt.Errorf("seed entry %d: agent_id is required", i)
		}
		if err := s.ledger.CreateAccount(e.AgentID, e.CashTicks, e.Positions); err != nil {
			return fmt.Errorf("seed entry %d (%q): %w", i, e.AgentID, err)
		}
	}
	return nil
}
