"""Market-following trader strategy.

Each instance starts with a personal reference price centred near a specified
base price (default 5000 ticks ≈ $50) with a small random spread, then tracks
the most recently observed trade price and re-anchors its orders there.

Order placement is Poisson-like: after each order a random wait interval is
drawn uniformly from [min_tick_gap, max_tick_gap], so the traders naturally
de-synchronise from one another even when seeded off a common base.
"""

from __future__ import annotations

import random
from dataclasses import dataclass, field

from agents.core.agent import Agent, MarketEvent, OrderBookUpdate, OrderIntent, TradeEvent

# Satisfy the structural Agent Protocol so type-checkers confirm the contract.
_: type[Agent] = None  # type: ignore[assignment]


@dataclass
class MarketFollowingTrader:
    """A trader that anchors limit orders to the last trade price ± a random
    perturbation of up to *perturb_pct*, placed at random tick intervals."""

    agent_id: str
    symbol: str

    # ---- price configuration ------------------------------------------------
    # All prices are integers (ticks). Using 5000 gives 1-tick resolution ≈ 0.02%,
    # which makes 2% perturbation (±100 ticks) meaningfully granular.
    base_price: int = 5000
    initial_spread_pct: float = 0.05   # ±5 % randomisation of the opening reference price
    perturb_pct: float = 0.02          # ±2 % perturbation on every order

    # ---- sizing -------------------------------------------------------------
    min_qty: int = 1
    max_qty: int = 10

    # ---- timing (in exchange ticks) ----------------------------------------
    min_tick_gap: int = 5
    max_tick_gap: int = 50

    # ---- RNG seed -----------------------------------------------------------
    seed: int = 0

    # ---- mutable state (not part of the public constructor) ----------------
    _rng: random.Random = field(init=False, repr=False)
    _reference_price: int = field(init=False, repr=False)
    _last_trade_price: int | None = field(init=False, repr=False)
    _next_order_tick: int = field(init=False, repr=False)

    def __post_init__(self) -> None:
        self._rng = random.Random(self.seed)

        # Personal opening reference price: base ± initial_spread_pct
        spread = self.base_price * self.initial_spread_pct
        lo = max(1, round(self.base_price - spread))
        hi = max(lo + 1, round(self.base_price + spread))
        self._reference_price = self._rng.randint(lo, hi)

        self._last_trade_price = None
        # Stagger first order so all 100 traders don't fire on tick 0.
        self._next_order_tick = self._rng.randint(0, self.max_tick_gap)

    # ------------------------------------------------------------------
    # Agent protocol
    # ------------------------------------------------------------------

    def on_tick(self, event: MarketEvent) -> list[OrderIntent]:
        if event.tick < self._next_order_tick:
            return []

        # Anchor to the most recent trade; fall back to personal reference.
        anchor = self._last_trade_price if self._last_trade_price is not None else self._reference_price

        # Apply a uniform random perturbation in [-perturb_pct, +perturb_pct].
        perturbation = self._rng.uniform(-self.perturb_pct, self.perturb_pct)
        raw_price = anchor * (1.0 + perturbation)
        price_ticks = max(1, round(raw_price))

        side = self._rng.choice(["BUY", "SELL"])
        qty = self._rng.randint(self.min_qty, self.max_qty)

        # Schedule next order.
        self._next_order_tick = event.tick + self._rng.randint(self.min_tick_gap, self.max_tick_gap)

        return [
            OrderIntent(
                symbol=self.symbol,
                side=side,
                order_type="LIMIT",
                qty=qty,
                price_ticks=price_ticks,
            )
        ]

    def on_trade(self, event: TradeEvent) -> None:
        """Update the reference price whenever a trade is reported."""
        if event.symbol == self.symbol:
            self._last_trade_price = event.price_ticks

    def on_order_book(self, event: OrderBookUpdate) -> None:
        pass
