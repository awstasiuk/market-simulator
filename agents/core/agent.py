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
class CandleEvent:
    symbol: str
    interval_ms: int
    start_tick: int
    open: int
    high: int
    low: int
    close: int
    volume: int


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
    # client_order_id is echoed back in SubmitOrderResponse so each agent can
    # correlate a response to its own internal order without inspecting order_id.
    client_order_id: int = 0


class Agent(Protocol):
    agent_id: str

    def on_tick(self, event: MarketEvent) -> Sequence[OrderIntent]:
        ...

    def on_trade(self, event: TradeEvent) -> None:
        ...

    def on_order_book(self, event: OrderBookUpdate) -> None:
        ...

    def on_candle(self, event: CandleEvent) -> None:
        ...
