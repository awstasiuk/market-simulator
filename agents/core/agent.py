from __future__ import annotations

from dataclasses import dataclass
from typing import Protocol, Sequence


@dataclass(frozen=True)
class MarketEvent:
    tick: int
    server_time_ms: int


@dataclass(frozen=True)
class TradeEvent:
    trade_id: str
    symbol: str
    price_ticks: int
    qty: int
    buy_order_id: str
    sell_order_id: str
    tick: int


@dataclass(frozen=True)
class PriceLevel:
    price_ticks: int
    qty: int


@dataclass(frozen=True)
class OrderBookUpdate:
    symbol: str
    bids: Sequence[PriceLevel]
    asks: Sequence[PriceLevel]


@dataclass(frozen=True)
class OrderIntent:
    symbol: str
    side: str
    order_type: str
    qty: int
    price_ticks: int | None = None


class Agent(Protocol):
    agent_id: str

    def on_tick(self, event: MarketEvent) -> Sequence[OrderIntent]:
        ...

    def on_trade(self, event: TradeEvent) -> None:
        ...

    def on_order_book(self, event: OrderBookUpdate) -> None:
        ...
