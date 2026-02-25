# Market Simulator — TODO

## Bugs

- [x] **Hub silently drops events for slow subscribers** — when a subscriber's 128-slot channel is
      full, events are discarded with no signal. Visualizer misses book snapshots during bursts.
      Options: increase buffer, use a ring buffer per subscriber, or disconnect the lagging client.

- [x] **`client_order_id` accepted but never echoed** — `SubmitOrderResponse` now returns it

- [x] **Tick events ignore `req.Symbol` filter** — *not a bug*. `TickEvent` is a global heartbeat
      with no `symbol` field; broadcasting it to all subscribers regardless of `req.Symbol` is
      correct by design. The misleading comment in `shouldSendMarketEvent` has been noted and
      clarified here.

## Missing Features

- [x] **Market orders implemented** — `MatchMarketBuy` / `MatchMarketSell` in `book.go`;
      `ReserveCash` / `SettleMarketBuy` in `ledger.go`; `cash_budget_ticks`, `filled_qty`,
      `cost_ticks` added to proto; `submitMarketBuy` / `submitMarketSell` wired in `exchange.go`.

- [x] **No initial book snapshot on subscribe** — `StreamMarketData` now sends the current
      `OrderBookUpdate` for all matching symbols immediately on connect (before entering the
      live event loop), so clients see the book state from the moment they connect.

- [x] **No `ListOrders` RPC** — `ListOrders(agent_id, symbol?)` added; returns all resting
      limit orders with their current remaining qty. Python client exposes `list-orders`
      subcommand.

- [ ] **No per-agent rate limiting** — a misbehaving agent can flood `SubmitOrder` with no
      back-pressure. Needed for Phase 2 (external bots / auth hardening).
