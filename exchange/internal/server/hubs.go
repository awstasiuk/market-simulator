package server

import (
	"log"
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
	var lagging []int64

	h.mu.RLock()
	for id, ch := range h.subs {
		select {
		case ch <- event:
		default:
			lagging = append(lagging, id)
		}
	}
	h.mu.RUnlock()

	if len(lagging) == 0 {
		return
	}
	h.mu.Lock()
	for _, id := range lagging {
		if ch, ok := h.subs[id]; ok {
			delete(h.subs, id)
			close(ch)
			log.Printf("marketDataHub: disconnected lagging subscriber %d (channel full)", id)
		}
	}
	h.mu.Unlock()
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
	var lagging []int64

	h.mu.RLock()
	for id, ch := range h.subs {
		select {
		case ch <- event:
		default:
			lagging = append(lagging, id)
		}
	}
	h.mu.RUnlock()

	if len(lagging) == 0 {
		return
	}
	h.mu.Lock()
	for _, id := range lagging {
		if ch, ok := h.subs[id]; ok {
			delete(h.subs, id)
			close(ch)
			log.Printf("tradeHub: disconnected lagging subscriber %d (channel full)", id)
		}
	}
	h.mu.Unlock()
}
