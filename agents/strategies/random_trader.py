from __future__ import annotations

import random
from dataclasses import dataclass

from agents.core.agent import Agent, MarketEvent, OrderIntent, OrderBookUpdate, TradeEvent


@dataclass
class RandomTrader:
    agent_id: str
    symbol: str
    min_price: int
    max_price: int
    min_qty: int = 1
    max_qty: int = 5
    seed: int = 7
    min_tick_gap: int = 20

    def __post_init__(self) -> None:
        self._rng = random.Random(self.seed)
        self._last_order_tick = -self.min_tick_gap

    def on_tick(self, event: MarketEvent) -> list[OrderIntent]:
        if event.tick - self._last_order_tick < self.min_tick_gap:
            return []

        side = self._rng.choice(["BUY", "SELL"])
        price = self._rng.randint(self.min_price, self.max_price)
        qty = self._rng.randint(self.min_qty, self.max_qty)
        self._last_order_tick = event.tick
        return [
            OrderIntent(
                symbol=self.symbol,
                side=side,
                order_type="LIMIT",
                qty=qty,
                price_ticks=price,
            )
        ]

    def on_trade(self, event: TradeEvent) -> None:
        return None

    def on_order_book(self, event: OrderBookUpdate) -> None:
        return None

    def on_candle(self, event: object) -> None:
        return None
