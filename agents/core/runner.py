from __future__ import annotations

import threading
import time
from dataclasses import dataclass
from typing import Iterable

from agents.core.agent import Agent, MarketEvent, OrderIntent, OrderBookUpdate, TradeEvent
from agents.core.market_data import MarketDataConfig, MarketDataStream, stream_trades
from agents.gen import market_pb2
from agents.gen import market_pb2_grpc
import grpc


@dataclass(frozen=True)
class RunnerConfig:
    host: str
    port: int
    symbol: str
    include_order_book: bool = True
    include_candles: bool = False
    include_trades: bool = True


class AgentRunner:
    def __init__(self, config: RunnerConfig, agents: Iterable[Agent]) -> None:
        self._config = config
        self._agents = list(agents)
        self._channel = grpc.insecure_channel(f"{config.host}:{config.port}")
        self._stub = market_pb2_grpc.ExchangeServiceStub(self._channel)

    def run(self) -> None:
        market_config = MarketDataConfig(
            host=self._config.host,
            port=self._config.port,
            symbol=self._config.symbol,
            include_order_book=self._config.include_order_book,
            include_candles=self._config.include_candles,
        )
        market_stream = MarketDataStream(market_config)
        trade_thread = None

        if self._config.include_trades:
            trade_thread = self._start_trade_listener()

        try:
            for event in market_stream.events():
                if isinstance(event, MarketEvent):
                    self._handle_tick(event)
                elif isinstance(event, OrderBookUpdate):
                    self._handle_order_book(event)
        finally:
            market_stream.close()
            self._channel.close()
            if trade_thread is not None:
                trade_thread.join(timeout=1)

    def _handle_tick(self, event: MarketEvent) -> None:
        for agent in self._agents:
            intents = agent.on_tick(event)
            for intent in intents:
                self._submit_order(agent.agent_id, intent)

    def _handle_order_book(self, event: OrderBookUpdate) -> None:
        for agent in self._agents:
            agent.on_order_book(event)

    def _submit_order(self, agent_id: str, intent: OrderIntent) -> None:
        side_enum = market_pb2.OrderSide.Value(f"ORDER_SIDE_{intent.side}")
        type_enum = market_pb2.OrderType.Value(f"ORDER_TYPE_{intent.order_type}")

        request = market_pb2.SubmitOrderRequest(
            agent_id=agent_id,
            symbol=intent.symbol,
            side=side_enum,
            type=type_enum,
            qty=intent.qty,
        )

        if intent.price_ticks is not None:
            request.price_ticks = intent.price_ticks

        self._stub.SubmitOrder(request)

    def _start_trade_listener(self) -> threading.Thread:
        thread = threading.Thread(
            target=trade_listener,
            args=(self._config.host, self._config.port, self._config.symbol, self._agents),
            daemon=True,
        )
        thread.start()
        return thread


def trade_listener(host: str, port: int, symbol: str, agents: Iterable[Agent]) -> None:
    for event in stream_trades(host, port, symbol):
        if event is None:
            continue
        trade = TradeEvent(
            trade_id=event.trade_id,
            symbol=event.symbol,
            price_ticks=event.price_ticks,
            qty=event.qty,
            buy_order_id=event.buy_order_id,
            sell_order_id=event.sell_order_id,
            tick=event.tick,
        )
        for agent in agents:
            agent.on_trade(trade)
        time.sleep(0)
