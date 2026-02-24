# Order Book Data Structure — Proposed Design

## Problem with the Current Implementation

The current book uses `map[int64][]Order` for bids and asks.  Every operation that
needs sorted price access — matching and snapshots — calls `sortedPrices()`, which:

1. Allocates a fresh `[]int64` by iterating the map.
2. Calls `sort.Slice` on it (O(n log n), where n = distinct price levels).

This runs on **every incoming order**.  With 100 traders firing every 5–50 ticks at
200 ms/tick, that is hundreds of sorts per second across potentially hundreds of
price levels.  There is also no cancel path — removing a resting order would require
a linear scan of the whole map.

---

## Proposed Structure

```
Book
├── asks  *priceSide   (ascending:  prices[0] = best ask)
├── bids  *priceSide   (descending: prices[0] = best bid)
└── index map[string]orderRef   (order_id → side + price, for cancel)
```

### `priceSide`

```go
type priceSide struct {
    prices []int64               // sorted; ascending (asks) or descending (bids)
    levels map[int64]*levelQueue // price → FIFO queue of orders at that level
    asc    bool                  // true = asks, false = bids
}
```

The `prices` slice is always kept sorted.  Insertion uses `sort.SearchInts` (binary
search) to find the insertion point, then `slices.Insert` (stdlib ≥ Go 1.21) to
splice in O(k) where k = distinct price levels.  Deletion after a level empties
uses the same binary search + splice.

### `levelQueue` — FIFO queue with O(1) amortised head removal

```go
type levelQueue struct {
    orders []Order
    head   int   // index of the first live order
}

func (q *levelQueue) push(o Order)          { q.orders = append(q.orders, o) }
func (q *levelQueue) peek() Order           { return q.orders[q.head] }
func (q *levelQueue) pop()                  { q.head++ }
func (q *levelQueue) updateHead(o Order)    { q.orders[q.head] = o }
func (q *levelQueue) empty() bool           { return q.head >= len(q.orders) }
func (q *levelQueue) totalQty() int64       { /* sum from head */ }
```

`head` is incremented in O(1) instead of reslicing.  When `head` reaches half the
backing array length the live orders are compacted (copied forward), bounding
wasted memory.

### `orderRef` — cancel index

```go
type orderRef struct {
    price int64
    side  Side
}
```

Every resting order is entered into `Book.index[order.ID]`.  Cancel is then:

1. O(1) map lookup → get price and side.
2. O(log n) binary search in `priceSide.prices` to confirm the level exists.
3. O(m) linear scan of the queue at that level (m = orders at that price, typically
   very small with ≤200 active agents).
4. Remove from queue; if queue empties, delete price from `prices` slice and `levels`
   map.

---

## Complexity Comparison

| Operation     | Current                    | Proposed                        |
|---------------|----------------------------|---------------------------------|
| Add/rest order | O(1) map append           | O(log n) binary search + O(k) splice |
| Best bid/ask  | O(n log n) sort            | **O(1)** — `prices[0]`          |
| Match         | O(n log n) sort + O(q)     | **O(m log n)** — iterate sorted prices, no re-sort |
| Cancel        | Not implemented (O(n) scan) | **O(log n + m)** via index     |
| Snapshot      | O(n log n) sort            | **O(n)** — prices already sorted |

n = distinct price levels, k = same (splice shift), m = orders at one level, q = queue length.

In practice, n is small (tens to low hundreds of price levels at any given time) so
the O(k) splice cost of insertion is negligible, while the elimination of
per-order sorting is the significant win.

---

## Concrete Go Sketch

```go
package orderbook

import (
    "slices" // stdlib >= Go 1.21
    "sort"
)

// ── level queue ──────────────────────────────────────────────────────────────

type levelQueue struct {
    orders []Order
    head   int
}

func (q *levelQueue) push(o Order)       { q.orders = append(q.orders, o) }
func (q *levelQueue) empty() bool        { return q.head >= len(q.orders) }
func (q *levelQueue) front() *Order      { return &q.orders[q.head] }
func (q *levelQueue) dequeue()           { q.head++ }

func (q *levelQueue) totalQty() int64 {
    var total int64
    for _, o := range q.orders[q.head:] {
        total += o.Qty
    }
    return total
}

// Remove a specific order by ID. Returns true if found and removed.
func (q *levelQueue) remove(orderID string) bool {
    for i := q.head; i < len(q.orders); i++ {
        if q.orders[i].ID == orderID {
            q.orders = slices.Delete(q.orders, i, i+1)
            if i == q.head && q.head > 0 {
                q.head-- // keep head valid after front removal before compaction
            }
            return true
        }
    }
    return false
}

// ── price side ───────────────────────────────────────────────────────────────

type priceSide struct {
    prices []int64
    levels map[int64]*levelQueue
    asc    bool // true = asks (ascending), false = bids (descending)
}

func newPriceSide(asc bool) *priceSide {
    return &priceSide{levels: make(map[int64]*levelQueue), asc: asc}
}

func (s *priceSide) bestPrice() (int64, bool) {
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
        s.insertPrice(o.PriceTicks)
    }
    q.push(o)
}

func (s *priceSide) insertPrice(p int64) {
    idx := s.searchIdx(p)
    s.prices = slices.Insert(s.prices, idx, p)
}

func (s *priceSide) deletePrice(p int64) {
    idx := s.searchIdx(p)
    if idx < len(s.prices) && s.prices[idx] == p {
        s.prices = slices.Delete(s.prices, idx, idx+1)
    }
    delete(s.levels, p)
}

// searchIdx returns the index where p should sit.
func (s *priceSide) searchIdx(p int64) int {
    if s.asc {
        return sort.Search(len(s.prices), func(i int) bool { return s.prices[i] >= p })
    }
    return sort.Search(len(s.prices), func(i int) bool { return s.prices[i] <= p })
}

// ── cancel index ─────────────────────────────────────────────────────────────

type orderRef struct {
    price int64
    side  Side
}

// ── book ─────────────────────────────────────────────────────────────────────

type Book struct {
    symbol string
    bids   *priceSide
    asks   *priceSide
    index  map[string]orderRef // order_id → location
}

func NewBook(symbol string) *Book {
    return &Book{
        symbol: symbol,
        bids:   newPriceSide(false),
        asks:   newPriceSide(true),
        index:  make(map[string]orderRef),
    }
}

func (b *Book) side(s Side) *priceSide {
    if s == SideBuy {
        return b.bids
    }
    return b.asks
}

// Match executes the incoming order against resting orders, returning filled
// trades.  Any unfilled remainder is automatically rested in the book.
func (b *Book) Match(order Order) []Trade {
    if order.Qty <= 0 {
        return nil
    }

    var (
        contra         *priceSide
        priceOK        func(restingPrice int64) bool
        makeTrade      func(resting Order, qty int64) Trade
    )

    if order.Side == SideBuy {
        contra = b.asks
        priceOK = func(p int64) bool { return p <= order.PriceTicks }
        makeTrade = func(r Order, qty int64) Trade {
            return Trade{PriceTicks: r.PriceTicks, Qty: qty,
                BuyOrderID: order.ID, SellOrderID: r.ID}
        }
    } else {
        contra = b.bids
        priceOK = func(p int64) bool { return p >= order.PriceTicks }
        makeTrade = func(r Order, qty int64) Trade {
            return Trade{PriceTicks: r.PriceTicks, Qty: qty,
                BuyOrderID: r.ID, SellOrderID: order.ID}
        }
    }

    var trades []Trade

    for len(contra.prices) > 0 && order.Qty > 0 {
        bestPrice := contra.prices[0]
        if !priceOK(bestPrice) {
            break
        }

        q := contra.levels[bestPrice]
        for !q.empty() && order.Qty > 0 {
            resting := q.front()
            fill := min(order.Qty, resting.Qty)
            trades = append(trades, makeTrade(*resting, fill))

            order.Qty   -= fill
            resting.Qty -= fill
            if resting.Qty == 0 {
                delete(b.index, resting.ID)
                q.dequeue()
            }
            // resting.Qty updated in place via pointer — no copy needed
        }

        if q.empty() {
            contra.deletePrice(bestPrice)
        }
    }

    if order.Qty > 0 {
        b.side(order.Side).add(order)
        b.index[order.ID] = orderRef{price: order.PriceTicks, side: order.Side}
    }

    return trades
}

// Cancel removes a resting order by ID.
func (b *Book) Cancel(orderID string) bool {
    ref, ok := b.index[orderID]
    if !ok {
        return false
    }
    s := b.side(ref.side)
    q := s.levels[ref.price]
    if q == nil {
        return false
    }
    found := q.remove(orderID)
    if found {
        delete(b.index, orderID)
        if q.empty() {
            s.deletePrice(ref.price)
        }
    }
    return found
}

// BestBid returns the highest bid price and whether one exists.
func (b *Book) BestBid() (int64, bool) { return b.bids.bestPrice() }

// BestAsk returns the lowest ask price and whether one exists.
func (b *Book) BestAsk() (int64, bool) { return b.asks.bestPrice() }

// SnapshotLevels returns all price levels in sorted order without allocating
// or sorting — the prices slice is already maintained sorted.
func (b *Book) SnapshotLevels() (bids, asks []Level) {
    bids = make([]Level, 0, len(b.bids.prices))
    for _, p := range b.bids.prices {
        bids = append(bids, Level{PriceTicks: p, Qty: b.bids.levels[p].totalQty()})
    }
    asks = make([]Level, 0, len(b.asks.prices))
    for _, p := range b.asks.prices {
        asks = append(asks, Level{PriceTicks: p, Qty: b.asks.levels[p].totalQty()})
    }
    return bids, asks
}
```

---

## Key Design Decisions

**Why a sorted slice and not a tree (`btree`, skip-list)?**

An external tree library adds a dependency and complexity.  In a real LOB the number
of *distinct* price levels at any moment is small (tens to a few hundred for a
simulated symbol), so O(k) slice shifts are cheaper in practice than tree node
pointer chasing due to CPU cache locality.  If the symbol ever had thousands of
concurrent distinct price levels a `btree` would be better, but that is not a
concern for this simulator.

**Why keep `prices` as a separate slice instead of just sorting `maps.Keys` lazily?**

So that `BestBid()` and `BestAsk()` are O(1) and `SnapshotLevels()` needs no sort.
The trade-off is maintaining the invariant that `prices` stays in sync with `levels`.

**Why `levelQueue.head` instead of `queue = queue[1:]`?**

Reslicing shifts the backing array reference on every dequeue, which looks O(1) but
means GC cannot collect the front elements until the whole slice is GC'd.  The `head`
index approach keeps the backing array pinned but the live window is explicit and
compaction can be triggered when the dead prefix grows large.
