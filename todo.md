# Market Simulator — TODO

## Bugs

- [x] **Order book flood on every submit** — `SubmitOrder` broadcasts an `OrderBookUpdate` on every order
      instead of batching once per tick in `runTicker`. Under 100 traders this saturates the hub.

- [x] **`sortedPrices` allocs + sorts on every order** — `matchBuy`/`matchSell` allocate a fresh
      `[]int64` and call `sort.Slice` (O(n log n)) on each incoming order.
      Replaced by the proposed sorted-slice book (see `docs/orderbook-design.md`).

- [ ] **Hub silently drops events for slow subscribers** — when a subscriber's 128-slot channel is
      full, events are discarded with no signal. Visualizer misses book snapshots during bursts.
      Options: increase buffer, use a ring buffer per subscriber, or disconnect the lagging client.

- [x] **No graceful shutdown** — `runTicker` goroutine has no stop channel; `ExchangeServer` has no
      `Stop()` / `Close()`. Goroutine leaks when the gRPC server exits.

- [ ] **`client_order_id` accepted but never echoed** — `SubmitOrderResponse` doesn't return it,
      so agents can't correlate fills back to their submitted order without separately tracking
      the returned `order_id`.

- [x] **`getOrCreateBook` double-lock pattern** — the book pointer is obtained under one lock,
      then used under a second separate lock. Safe today but fragile; consolidate into one critical
      section or use `sync.Map`.

- [ ] **Tick events ignore `req.Symbol` filter** — `shouldSendMarketEvent` returns `true` for all
      `TickEvent` regardless of the subscriber's requested symbol. Will broadcast all ticks in a
      multi-symbol deployment.

## Missing Features

- [x] **`CancelOrder` not implemented** — always returns `CANCEL_STATUS_REJECTED`. The book has no
      cancel path. With random traders continuously posting limit orders the book grows unbounded;
      far-off-market orders never leave.

- [ ] **Market orders not implemented** — server immediately rejects `ORDER_TYPE_MARKET`.
      The proto, enum, and book `TypeMarket` constant exist but the execution path is absent.

- [x] **Server-side candle builder absent** — `CandleClosed` message and
      `MarketDataEvent_Candle` payload exist in the proto but are never emitted.
      Candles should be aggregated inside `runTicker` (one live candle per symbol,
      closed every N ticks) so all clients see consistent, authoritative OHLCV data.

- [ ] **No initial book snapshot on subscribe** — a client that connects mid-session only receives
      *new* events; the current book state is never replayed.
      Add a `GetBookSnapshot(symbol) → OrderBookUpdate` unary RPC, or send the current
      snapshot immediately on `StreamMarketData` subscription.

- [ ] **No fill details in `SubmitOrderResponse`** — when a crossing limit order immediately fills,
      the response is just `ACCEPTED` with no qty or price. Add a `fills` repeated field so agents
      can react synchronously without subscribing to the trade stream.

- [x] **No portfolio / position tracking** — agents can submit unlimited sells of stock they don't
      hold. No balance, position limits, margin, or short-selling rules.
      Even a simple `map[agentID]map[symbol]int64` position ledger would catch naive overselling.

- [ ] **No `ListOrders` RPC** — agents have no way to query their currently resting orders.
      Combined with missing cancel, there is no way to manage open interest.

- [ ] **No per-agent rate limiting** — a misbehaving agent can flood `SubmitOrder` with no
      back-pressure. Needed for Phase 2 (external bots / auth hardening).

## Structural Improvements

- [x] **Replace `map[int64][]Order` + sort-on-read with sorted-slice book** — O(log n) insert,
      O(1) best-bid/ask, O(1) snapshot iteration, O(log n) cancel.
      See proposed design in `docs/orderbook-design.md`.

- [x] **Batch book updates on tick** — collect *dirty* symbols during `SubmitOrder` and flush one
      `OrderBookUpdate` per symbol per tick inside `runTicker`. Eliminates the per-order broadcast
      flood and aligns with the 200 ms tick design principle.

- [x] **Split `MatchLimit` into `Match` + `Rest`** — makes the book's contract explicit:
      `Match(order) []Trade` executes against resting orders; `Rest(order)` adds the unfilled
      remainder. Easier to test in isolation.

- [x] **Separate matching from locking in `exchange.go`** — the book should be self-contained and
      lock-free (one book per symbol, accessed from a single goroutine or with its own mutex).
      The server-level `mu` RWMutex should only guard the `books` map, not individual book ops.

- [x] **Add `go.sum`-managed dependency for `slices` package or stay stdlib** — Go 1.21+ stdlib
      `slices` package provides `slices.Insert` / `slices.Delete` which make the sorted-slice book
      implementation clean without adding external deps.
