"""Offline test for the visualizer (no exchange, no window)."""
import sys
sys.path.insert(0, ".")

import matplotlib
matplotlib.use("Agg")  # non-interactive backend

from ui.visualizer import (
    MarketState, Candle, _LiveCandle,
    _draw_candles, _draw_volume, _draw_book,
)
import matplotlib.pyplot as plt

state = MarketState(candle_ticks=5)

# Simulate trades + ticks
for tick in range(30):
    if tick % 3 == 0:
        state.on_trade(tick, 5000 + tick * 2, qty=5)
    state.on_tick(tick)

snap = state.snapshot()
print(f"candles={len(snap['candles'])}  live={snap['live']}  "
      f"tick={snap['tick']}  price={snap['price']}")

assert snap["tick"] == 29
assert snap["price"] is not None
assert len(snap["candles"]) > 0

fig, (ax1, ax2, ax3) = plt.subplots(1, 3)
_draw_candles(ax1, snap["candles"], snap["live"])
_draw_volume(ax2,  snap["candles"], snap["live"])
_draw_book(ax3,    snap["bids"],    snap["asks"])
plt.close(fig)

print("All visualizer checks passed.")
