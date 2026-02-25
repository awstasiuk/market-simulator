package server

import (
	"sync"
	"testing"
	"time"

	"market-simulator/exchange/gen/market"
)

// ─── helpers ─────────────────────────────────────────────────────────────────

// tickEvt wraps a bare TickEvent (tick counter only — no symbol on the proto).
func tickEvt(tick int64) *market.MarketDataEvent {
	return &market.MarketDataEvent{
		Payload: &market.MarketDataEvent_Tick{
			Tick: &market.TickEvent{Tick: tick},
		},
	}
}

// candleEvt wraps a CandleClosed event for a given symbol and close price.
func candleEvt(symbol string, close int64) *market.MarketDataEvent {
	return &market.MarketDataEvent{
		Payload: &market.MarketDataEvent_Candle{
			Candle: &market.CandleClosed{Symbol: symbol, Close: close},
		},
	}
}

func waitForMarketEvent(t *testing.T, ch <-chan *market.MarketDataEvent, d time.Duration) *market.MarketDataEvent {
	t.Helper()
	select {
	case evt, ok := <-ch:
		if !ok {
			return nil
		}
		return evt
	case <-time.After(d):
		t.Errorf("timeout waiting for MarketDataEvent")
		return nil
	}
}

func waitForTradeEvent(t *testing.T, ch <-chan *market.TradeEvent, d time.Duration) *market.TradeEvent {
	t.Helper()
	select {
	case evt, ok := <-ch:
		if !ok {
			return nil
		}
		return evt
	case <-time.After(d):
		t.Errorf("timeout waiting for TradeEvent")
		return nil
	}
}

// drainAndExpectClosed reads all buffered items from ch and then expects the
// channel to be closed (next receive returns ok==false).
func drainAndExpectClosed(t *testing.T, ch <-chan *market.MarketDataEvent) {
	t.Helper()
	deadline := time.Now().Add(500 * time.Millisecond)
	for time.Now().Before(deadline) {
		select {
		case _, ok := <-ch:
			if !ok {
				return // closed — correct
			}
			// buffered item: keep draining
		default:
			time.Sleep(5 * time.Millisecond)
		}
	}
	t.Error("slow subscriber channel was never closed after eviction")
}

// ─── marketDataHub tests ──────────────────────────────────────────────────────

func TestBroadcastDeliversToAllSubscribers(t *testing.T) {
	h := newMarketDataHub()

	_, ch1 := h.Subscribe()
	_, ch2 := h.Subscribe()

	h.Broadcast(candleEvt("SIM", 100))

	got1 := waitForMarketEvent(t, ch1, 200*time.Millisecond)
	got2 := waitForMarketEvent(t, ch2, 200*time.Millisecond)

	if got1 == nil || got1.GetCandle().GetSymbol() != "SIM" {
		t.Errorf("subscriber 1 did not receive correct event: %v", got1)
	}
	if got2 == nil || got2.GetCandle().GetSymbol() != "SIM" {
		t.Errorf("subscriber 2 did not receive correct event: %v", got2)
	}
}

func TestUnsubscribeStopsDelivery(t *testing.T) {
	h := newMarketDataHub()

	id, ch := h.Subscribe()
	h.Unsubscribe(id)

	h.Broadcast(tickEvt(1))

	// Unsubscribe closes the channel immediately.  Any receive should either see
	// the channel closed (ok==false) or find nothing delivered.
	select {
	case v, ok := <-ch:
		if ok {
			t.Errorf("received live event on unsubscribed channel: %v", v)
		}
		// closed — correct
	case <-time.After(100 * time.Millisecond):
		// No value — acceptable; the Broadcast skipped the removed subscriber.
	}
}

// TestLaggingSubscriberDisconnected verifies that when a subscriber's channel
// is full the hub closes it, forcing that streaming handler to detect disconnect,
// while continuing to deliver to healthy subscribers.
func TestLaggingSubscriberDisconnected(t *testing.T) {
	const bufSize = 128 // must match hub's make(chan …, 128)

	h := newMarketDataHub()

	_, slow := h.Subscribe() // will intentionally not drain
	_, fast := h.Subscribe()

	// Fill both channels by broadcasting exactly bufSize events.
	for i := 0; i < bufSize; i++ {
		h.Broadcast(tickEvt(int64(i + 1)))
	}

	// Drain fast so its buffer has room before the eviction broadcast.
	for i := 0; i < bufSize; i++ {
		select {
		case <-fast:
		case <-time.After(200 * time.Millisecond):
			t.Fatalf("fast subscriber missing event %d", i)
		}
	}

	// One more broadcast: slow's channel is full → hub must evict and close it.
	// Broadcast is synchronous, so eviction is complete before we proceed.
	h.Broadcast(tickEvt(9999))

	// slow channel must be closed after draining any buffered items.
	drainAndExpectClosed(t, slow)

	// fast must still receive the eviction-triggering broadcast.
	got := waitForMarketEvent(t, fast, 200*time.Millisecond)
	if got == nil {
		t.Error("fast subscriber stopped receiving after slow was evicted")
	}
}

// TestConcurrentSubscribeBroadcast is a race-detector test: many goroutines
// subscribe/unsubscribe while broadcasts fire concurrently.
func TestConcurrentSubscribeBroadcast(t *testing.T) {
	h := newMarketDataHub()

	var wg sync.WaitGroup
	const workers = 20
	const rounds = 50

	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for r := 0; r < rounds; r++ {
				id, ch := h.Subscribe()
				select {
				case <-ch:
				default:
				}
				h.Unsubscribe(id)
			}
		}()
	}

	// Broadcast concurrently.
	done := make(chan struct{})
	go func() {
		for {
			select {
			case <-done:
				return
			default:
				h.Broadcast(tickEvt(1))
			}
		}
	}()

	wg.Wait()
	close(done)
}

// ─── tradeHub tests ───────────────────────────────────────────────────────────

// TestTradeHubBroadcastDelivers verifies that a subscribed client receives a
// broadcast event.
func TestTradeHubBroadcastDelivers(t *testing.T) {
	h := newTradeHub()

	_, ch := h.Subscribe()

	h.Broadcast(&market.TradeEvent{Symbol: "SIM", TradeId: "ord-1"})

	got := waitForTradeEvent(t, ch, 200*time.Millisecond)
	if got == nil {
		t.Fatal("no event received")
	}
	if got.TradeId != "ord-1" {
		t.Errorf("TradeId: want ord-1, got %s", got.TradeId)
	}
}

// TestTradeHubMultipleSubscribersAllReceive verifies fan-out.
func TestTradeHubMultipleSubscribersAllReceive(t *testing.T) {
	h := newTradeHub()

	_, ch1 := h.Subscribe()
	_, ch2 := h.Subscribe()

	h.Broadcast(&market.TradeEvent{Symbol: "SIM", TradeId: "ord-2"})

	got1 := waitForTradeEvent(t, ch1, 200*time.Millisecond)
	got2 := waitForTradeEvent(t, ch2, 200*time.Millisecond)

	if got1 == nil || got1.TradeId != "ord-2" {
		t.Errorf("subscriber 1 event: %v", got1)
	}
	if got2 == nil || got2.TradeId != "ord-2" {
		t.Errorf("subscriber 2 event: %v", got2)
	}
}

// TestTradeHubUnsubscribeStopsDelivery verifies that after Unsubscribe the
// channel is closed and no further events arrive.
func TestTradeHubUnsubscribeStopsDelivery(t *testing.T) {
	h := newTradeHub()

	id, ch := h.Subscribe()
	h.Unsubscribe(id)

	h.Broadcast(&market.TradeEvent{Symbol: "SIM", TradeId: "should-not-arrive"})

	select {
	case v, ok := <-ch:
		if ok {
			t.Errorf("received live event on unsubscribed trade channel: %v", v)
		}
		// closed — correct
	case <-time.After(100 * time.Millisecond):
		// Nothing received — also acceptable.
	}
}
