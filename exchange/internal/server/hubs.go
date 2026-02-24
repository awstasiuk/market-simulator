package server

import (
	"sync"
	"sync/atomic"

	"market-simulator/exchange/gen/market"
)

type marketDataHub struct {
	mu   sync.RWMutex
	subs map[int64]chan *market.MarketDataEvent
	seq  atomic.Int64
}

func newMarketDataHub() *marketDataHub {
	return &marketDataHub{
		subs: make(map[int64]chan *market.MarketDataEvent),
	}
}

func (h *marketDataHub) Subscribe() (int64, <-chan *market.MarketDataEvent) {
	id := h.seq.Add(1)
	ch := make(chan *market.MarketDataEvent, 128)

	h.mu.Lock()
	h.subs[id] = ch
	h.mu.Unlock()

	return id, ch
}

func (h *marketDataHub) Unsubscribe(id int64) {
	h.mu.Lock()
	ch, ok := h.subs[id]
	if ok {
		delete(h.subs, id)
		close(ch)
	}
	h.mu.Unlock()
}

func (h *marketDataHub) Broadcast(event *market.MarketDataEvent) {
	h.mu.RLock()
	for _, ch := range h.subs {
		select {
		case ch <- event:
		default:
		}
	}
	h.mu.RUnlock()
}

type tradeHub struct {
	mu   sync.RWMutex
	subs map[int64]chan *market.TradeEvent
	seq  atomic.Int64
}

func newTradeHub() *tradeHub {
	return &tradeHub{
		subs: make(map[int64]chan *market.TradeEvent),
	}
}

func (h *tradeHub) Subscribe() (int64, <-chan *market.TradeEvent) {
	id := h.seq.Add(1)
	ch := make(chan *market.TradeEvent, 128)

	h.mu.Lock()
	h.subs[id] = ch
	h.mu.Unlock()

	return id, ch
}

func (h *tradeHub) Unsubscribe(id int64) {
	h.mu.Lock()
	ch, ok := h.subs[id]
	if ok {
		delete(h.subs, id)
		close(ch)
	}
	h.mu.Unlock()
}

func (h *tradeHub) Broadcast(event *market.TradeEvent) {
	h.mu.RLock()
	for _, ch := range h.subs {
		select {
		case ch <- event:
		default:
		}
	}
	h.mu.RUnlock()
}
