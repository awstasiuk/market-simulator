"""Real-time market visualizer for the market simulator.

Connects to the exchange gRPC streams and renders a dark-themed GUI window
showing:
  - Candlestick chart (client-side candle aggregation from the trade stream)
  - Volume bars beneath each candle
  - Live order book depth (bid/ask levels)
  - Title bar showing current tick, last price, and best bid/ask spread

Candles are built locally from the trade stream in configurable-tick intervals
(default 5 ticks = 1 second at the 200 ms exchange tick rate).  The exchange's
own CandleClosed events are used when received and overwrite the client-built
candle for that period.

Usage (exchange must already be running):
    python -m ui.visualizer
    python -m ui.visualizer --host 127.0.0.1 --port 50051 --symbol SIM
    python -m ui.visualizer --candle-ticks 10 --max-candles 80

Requires matplotlib (pip install matplotlib).
All prices are stored as raw ticks and divided by 100 for display ($50.00).
"""

from __future__ import annotations

import argparse
import collections
import threading
from dataclasses import dataclass, field
from typing import Deque

import grpc
import matplotlib
matplotlib.use("TkAgg")  # Works out-of-the-box on Windows; change to Qt5Agg if needed
import matplotlib.axes
import matplotlib.gridspec as gridspec
import matplotlib.patches as mpatches
import matplotlib.pyplot as plt
from matplotlib.animation import FuncAnimation  # noqa: F401 – kept for type clarity
from matplotlib.artist import Artist

from agents.gen import market_pb2
from agents.gen import market_pb2_grpc

# ──────────────────────────────────────────────────────────────────────────────
# Colour palette (dark theme)
# ──────────────────────────────────────────────────────────────────────────────
C_BG        = "#1a1a2e"   # window background
C_AX        = "#0d0d1a"   # axes background
C_BULL      = "#26a69a"   # bullish candle / bid bar  (teal)
C_BEAR      = "#ef5350"   # bearish candle / ask bar  (red)
C_TEXT      = "#c8c8e0"   # axis labels / tick labels
C_GRID      = "#1e1e3a"   # grid lines
C_SPINE     = "#30304a"   # axis spines
C_LAST      = "#ffd54f"   # last-price horizontal line (amber)
C_SPREAD    = "#80cbc4"   # spread annotation

# ──────────────────────────────────────────────────────────────────────────────
# Data containers
# ──────────────────────────────────────────────────────────────────────────────

@dataclass
class Candle:
    start_tick: int
    open:  float
    high:  float
    low:   float
    close: float
    volume: int


@dataclass
class _LiveCandle:
    """Mutable candle being built from incoming trades."""
    start_tick: int
    open:   float | None = None
    high:   float        = field(default_factory=lambda: float("-inf"))
    low:    float        = field(default_factory=lambda: float("inf"))
    close:  float | None = None
    volume: int          = 0

    def add_trade(self, price: float, qty: int) -> None:
        if self.open is None:
            self.open = price
        if price > self.high:
            self.high = price
        if price < self.low:
            self.low = price
        self.close = price
        self.volume += qty

    def to_candle(self) -> Candle | None:
        if self.open is None:
            return None
        return Candle(self.start_tick, self.open, self.high, self.low,
                      self.close or self.open, self.volume)


class MarketState:
    """Thread-safe store updated by background gRPC threads."""

    def __init__(self, candle_ticks: int = 5) -> None:
        self._lock = threading.Lock()
        self.candle_ticks = candle_ticks

        self._candles:    list[Candle] = []
        self._live:       _LiveCandle | None = None

        self.bids: list[tuple[float, int]] = []   # (price $, qty)
        self.asks: list[tuple[float, int]] = []

        self.recent_trades: Deque[tuple[int, float, int]] = \
            collections.deque(maxlen=40)           # (tick, price $, qty)

        self.last_tick:  int   = 0
        self.last_price: float | None = None
        self.connected:  bool  = False

    # ── write helpers (called from background threads) ──────────────────────

    def on_trade(self, tick: int, price_ticks: int, qty: int) -> None:
        price = price_ticks / 100
        period_start = (tick // self.candle_ticks) * self.candle_ticks

        with self._lock:
            self.last_price = price
            self.recent_trades.appendleft((tick, price, qty))

            if self._live is None:
                self._live = _LiveCandle(start_tick=period_start)
            elif period_start > self._live.start_tick:
                # Close the previous period and open a new one.
                closed = self._live.to_candle()
                if closed:
                    self._candles.append(closed)
                self._live = _LiveCandle(start_tick=period_start)

            self._live.add_trade(price, qty)

    def on_tick(self, tick: int) -> None:
        period_start = (tick // self.candle_ticks) * self.candle_ticks
        with self._lock:
            self.last_tick = tick
            self.connected = True
            # Close the live candle if its period has passed.
            if self._live is not None and period_start > self._live.start_tick:
                closed = self._live.to_candle()
                if closed:
                    self._candles.append(closed)
                self._live = None

    def on_server_candle(self, c: "market_pb2.CandleClosed") -> None:  # type: ignore[name-defined]
        """Prefer server-authoritative candles when the exchange emits them."""
        candle = Candle(
            start_tick=c.start_tick,
            open=c.open / 100,
            high=c.high / 100,
            low=c.low / 100,
            close=c.close / 100,
            volume=c.volume,
        )
        with self._lock:
            # Replace a matching client-built candle if there is one.
            for i, existing in enumerate(self._candles):
                if existing.start_tick == candle.start_tick:
                    self._candles[i] = candle
                    return
            self._candles.append(candle)

    def on_order_book(self, bids: list, asks: list) -> None:
        with self._lock:
            self.bids = [(b.price_ticks / 100, b.qty) for b in bids]
            self.asks = [(a.price_ticks / 100, a.qty) for a in asks]

    # ── read helper (called from animation thread) ───────────────────────────

    def snapshot(self, max_candles: int = 60) -> dict:
        with self._lock:
            candles = list(self._candles[-max_candles:])
            live    = self._live.to_candle() if self._live else None
            bids    = list(self.bids)
            asks    = list(self.asks)
            trades  = list(self.recent_trades)
            return dict(
                candles=candles,
                live=live,
                bids=bids,
                asks=asks,
                trades=trades,
                tick=self.last_tick,
                price=self.last_price,
                connected=self.connected,
            )


# ──────────────────────────────────────────────────────────────────────────────
# Background gRPC threads
# ──────────────────────────────────────────────────────────────────────────────

def _run_market_stream(
    host: str,
    port: int,
    symbol: str,
    state: MarketState,
    stop: threading.Event,
) -> None:
    """Consume StreamMarketData: ticks, order-book updates, and server candles."""
    while not stop.is_set():
        channel = grpc.insecure_channel(f"{host}:{port}")
        try:
            stub    = market_pb2_grpc.ExchangeServiceStub(channel)
            request = market_pb2.MarketDataRequest(
                symbol=symbol,
                include_order_book=True,
                include_candles=True,
            )
            for event in stub.StreamMarketData(request):
                if stop.is_set():
                    break
                payload = event.WhichOneof("payload")
                if payload == "tick":
                    state.on_tick(event.tick.tick)
                elif payload == "order_book":
                    ob = event.order_book
                    state.on_order_book(list(ob.bids), list(ob.asks))
                elif payload == "candle":
                    state.on_server_candle(event.candle)
        except grpc.RpcError:
            if not stop.is_set():
                import time
                time.sleep(1)          # brief back-off before reconnect
        finally:
            channel.close()


def _run_trade_stream(
    host: str,
    port: int,
    symbol: str,
    state: MarketState,
    stop: threading.Event,
) -> None:
    """Consume StreamTrades and feed trade events into MarketState."""
    while not stop.is_set():
        channel = grpc.insecure_channel(f"{host}:{port}")
        try:
            stub    = market_pb2_grpc.ExchangeServiceStub(channel)
            request = market_pb2.TradeStreamRequest(symbol=symbol)
            for event in stub.StreamTrades(request):
                if stop.is_set():
                    break
                state.on_trade(event.tick, event.price_ticks, event.qty)
        except grpc.RpcError:
            if not stop.is_set():
                import time
                time.sleep(1)
        finally:
            channel.close()


# ──────────────────────────────────────────────────────────────────────────────
# Matplotlib visualizer
# ──────────────────────────────────────────────────────────────────────────────

def _style_ax(ax: matplotlib.axes.Axes, ylabel: str = "", xlabel: str = "") -> None:
    ax.set_facecolor(C_AX)
    ax.tick_params(colors=C_TEXT, labelsize=8)
    for spine in ax.spines.values():
        spine.set_edgecolor(C_SPINE)
    ax.grid(color=C_GRID, linewidth=0.5, alpha=0.6)
    if ylabel:
        ax.set_ylabel(ylabel, color=C_TEXT, fontsize=8)
    if xlabel:
        ax.set_xlabel(xlabel, color=C_TEXT, fontsize=8)


def _draw_candles(ax: matplotlib.axes.Axes, candles: list[Candle], live: Candle | None) -> None:
    ax.cla()
    _style_ax(ax, ylabel="Price ($)")
    plt.setp(ax.get_xticklabels(), visible=False)

    all_c = candles + ([live] if live else [])
    if not all_c:
        ax.text(0.5, 0.5, "Waiting for trades…",
                transform=ax.transAxes, color=C_TEXT,
                ha="center", va="center", fontsize=10)
        return

    body_w = 0.55
    last_price: float | None = None

    for i, c in enumerate(all_c):
        is_bull   = c.close >= c.open
        alpha     = 1.0 if (live is None or i < len(all_c) - 1) else 0.55
        color     = C_BULL if is_bull else C_BEAR
        body_lo   = min(c.open, c.close)
        body_hi   = max(c.open, c.close)
        body_h    = max(body_hi - body_lo, 0.005)   # minimum visible height

        # High-low wick
        ax.plot([i, i], [c.low, c.high],
                color=color, linewidth=0.9, alpha=alpha, zorder=1)

        # Body rectangle
        rect = mpatches.FancyBboxPatch(
            (i - body_w / 2, body_lo), body_w, body_h,
            boxstyle="square,pad=0",
            facecolor=color, edgecolor=color,
            linewidth=0.4, alpha=alpha, zorder=2,
        )
        ax.add_patch(rect)
        last_price = c.close

    # Last-price horizontal line
    if last_price is not None:
        ax.axhline(last_price, color=C_LAST, linewidth=0.7,
                   linestyle="--", alpha=0.75, zorder=3)

    # Axes limits
    prices = [v for c in all_c for v in (c.low, c.high)]
    lo, hi = min(prices), max(prices)
    pad = max((hi - lo) * 0.12, 0.10)
    ax.set_ylim(lo - pad, hi + pad)
    ax.set_xlim(-0.8, len(all_c) - 0.2)


def _draw_volume(ax: matplotlib.axes.Axes, candles: list[Candle], live: Candle | None) -> None:
    ax.cla()
    _style_ax(ax, ylabel="Volume", xlabel="← candles (oldest to newest) →")
    ax.tick_params(labelbottom=False)

    all_c = candles + ([live] if live else [])
    if not all_c:
        return

    xs     = list(range(len(all_c)))
    vols   = [c.volume for c in all_c]
    colors = [C_BULL if c.close >= c.open else C_BEAR for c in all_c]
    alphas = [1.0] * len(all_c)
    if live:
        alphas[-1] = 0.55

    bars = ax.bar(xs, vols, color=colors, width=0.55, align="center")
    for bar, alpha in zip(bars, alphas):
        bar.set_alpha(alpha)

    ax.set_xlim(-0.8, len(all_c) - 0.2)
    ax.set_ylim(0, max(vols) * 1.15 if vols else 1)


def _draw_book(ax: matplotlib.axes.Axes, bids: list, asks: list) -> None:
    ax.cla()
    _style_ax(ax, ylabel="Price ($)", xlabel="Quantity")
    ax.set_title("Order Book", color=C_TEXT, fontsize=9, pad=4)

    if not bids and not asks:
        ax.text(0.5, 0.5, "No book data",
                transform=ax.transAxes, color=C_TEXT,
                ha="center", va="center", fontsize=9)
        return

    bar_h = 0.08   # height of each horizontal bar in price units

    if bids:
        bprices = [p for p, _ in bids]
        bqtys   = [q for _, q in bids]
        ax.barh(bprices, bqtys, height=bar_h, color=C_BULL, alpha=0.80)

    if asks:
        aprices = [p for p, _ in asks]
        aqtys   = [q for _, q in asks]
        ax.barh(aprices, aqtys, height=bar_h, color=C_BEAR, alpha=0.80)

    # Spread annotation
    if bids and asks:
        best_bid = max(p for p, _ in bids)
        best_ask = min(p for p, _ in asks)
        spread   = best_ask - best_bid
        mid      = (best_bid + best_ask) / 2
        ax.axhline(best_bid, color=C_BULL, linewidth=0.6, linestyle=":")
        ax.axhline(best_ask, color=C_BEAR, linewidth=0.6, linestyle=":")
        ax.text(ax.get_xlim()[1] * 0.05, mid,
                f"spread ${spread:.2f}",
                color=C_SPREAD, fontsize=7, va="center")

    all_prices = [p for p, _ in (bids + asks)]
    if all_prices:
        lo, hi = min(all_prices), max(all_prices)
        pad = max((hi - lo) * 0.15, 0.10)
        ax.set_ylim(lo - pad, hi + pad)

    # Legend squibs
    ax.legend(
        handles=[
            mpatches.Patch(color=C_BULL, label="Bids"),
            mpatches.Patch(color=C_BEAR, label="Asks"),
        ],
        fontsize=7, loc="lower right",
        facecolor=C_AX, edgecolor=C_SPINE,
        labelcolor=C_TEXT,
    )


# ──────────────────────────────────────────────────────────────────────────────
# Main entry point
# ──────────────────────────────────────────────────────────────────────────────

def build_parser() -> argparse.ArgumentParser:
    p = argparse.ArgumentParser(description="Real-time market visualizer")
    p.add_argument("--host",         default="127.0.0.1")
    p.add_argument("--port",         type=int, default=50051)
    p.add_argument("--symbol",       default="SIM")
    p.add_argument("--candle-ticks", type=int, default=5,
                   help="Exchange ticks per candle (default 5 = 1 s at 200 ms/tick)")
    p.add_argument("--max-candles",  type=int, default=60,
                   help="Maximum candles shown on chart (default 60)")
    p.add_argument("--refresh-ms",   type=int, default=400,
                   help="Chart refresh interval in ms (default 400)")
    return p


def main() -> None:
    args   = build_parser().parse_args()
    state  = MarketState(candle_ticks=args.candle_ticks)
    stop   = threading.Event()
    max_c  = args.max_candles

    # ── start background streams ──────────────────────────────────────────────
    for target in (_run_market_stream, _run_trade_stream):
        t = threading.Thread(
            target=target,
            args=(args.host, args.port, args.symbol, state, stop),
            daemon=True,
        )
        t.start()

    # ── build figure ──────────────────────────────────────────────────────────
    fig = plt.figure(figsize=(16, 9), facecolor=C_BG)
    fig.canvas.manager.set_window_title(f"Market Visualizer — {args.symbol}")  # type: ignore[union-attr]

    gs = gridspec.GridSpec(
        4, 3, figure=fig,
        hspace=0.08, wspace=0.32,
        left=0.06, right=0.98, top=0.92, bottom=0.06,
    )
    ax_price = fig.add_subplot(gs[0:3, 0:2])
    ax_vol   = fig.add_subplot(gs[3,   0:2])
    ax_book  = fig.add_subplot(gs[0:4, 2])

    title_text = fig.suptitle(
        f"{args.symbol}   connecting to {args.host}:{args.port}…",
        color=C_TEXT, fontsize=13, fontweight="bold", y=0.97,
    )

    # ── animation callback ────────────────────────────────────────────────────
    def update(_frame: int) -> list[Artist]:
        snap = state.snapshot(max_candles=max_c)

        candles: list[Candle] = snap["candles"]
        live:    Candle | None = snap["live"]
        bids:    list          = snap["bids"]
        asks:    list          = snap["asks"]
        tick:    int           = snap["tick"]
        price:   float | None  = snap["price"]

        _draw_candles(ax_price, candles, live)
        _draw_volume(ax_vol,   candles, live)
        _draw_book(ax_book, bids, asks)

        # Update title
        price_str = f"${price:.2f}" if price is not None else "—"
        bid_str   = f"${max(p for p,_ in bids):.2f}" if bids else "—"
        ask_str   = f"${min(p for p,_ in asks):.2f}" if asks else "—"
        title_text.set_text(
            f"{args.symbol}   tick {tick:,}   last {price_str}   "
            f"bid {bid_str}   ask {ask_str}"
        )
        return []

    anim = FuncAnimation(  # noqa: F841  – must stay referenced to keep alive
        fig, update,
        interval=args.refresh_ms,
        cache_frame_data=False,
    )

    try:
        plt.show()
    finally:
        stop.set()


if __name__ == "__main__":
    main()
