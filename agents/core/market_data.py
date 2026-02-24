from __future__ import annotations

from dataclasses import dataclass
from typing import Iterator, Optional

import grpc

from agents.gen import market_pb2
from agents.gen import market_pb2_grpc
from agents.core.agent import MarketEvent, OrderBookUpdate, PriceLevel


@dataclass(frozen=True)
class MarketDataConfig:
    host: str
    port: int
    symbol: str
    include_order_book: bool
    include_candles: bool


class MarketDataStream:
    def __init__(self, config: MarketDataConfig) -> None:
        self._config = config
        self._channel = grpc.insecure_channel(f"{config.host}:{config.port}")
        self._stub = market_pb2_grpc.ExchangeServiceStub(self._channel)

    def events(self) -> Iterator[MarketEvent | OrderBookUpdate]:
        request = market_pb2.MarketDataRequest(
            symbol=self._config.symbol,
            include_order_book=self._config.include_order_book,
            include_candles=self._config.include_candles,
        )

        for event in self._stub.StreamMarketData(request):
            payload = event.WhichOneof("payload")
            if payload == "tick":
                tick = event.tick
                yield MarketEvent(tick=tick.tick, server_time_ms=tick.server_time_unix_ms)
            elif payload == "order_book":
                update = event.order_book
                bids = [PriceLevel(price_ticks=b.price_ticks, qty=b.qty) for b in update.bids]
                asks = [PriceLevel(price_ticks=a.price_ticks, qty=a.qty) for a in update.asks]
                yield OrderBookUpdate(symbol=update.symbol, bids=bids, asks=asks)

    def close(self) -> None:
        self._channel.close()


def stream_trades(host: str, port: int, symbol: str) -> Iterator[Optional[market_pb2.TradeEvent]]:
    channel = grpc.insecure_channel(f"{host}:{port}")
    stub = market_pb2_grpc.ExchangeServiceStub(channel)
    request = market_pb2.TradeStreamRequest(symbol=symbol)

    try:
        for event in stub.StreamTrades(request):
            yield event
    finally:
        channel.close()
