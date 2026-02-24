Exchange API (gRPC)

Services
- ExchangeService
  - SubmitOrder
  - CancelOrder
  - StreamMarketData
  - StreamTrades

Core Messages
- SubmitOrderRequest: agent_id, symbol, side, type, qty, price_ticks, client_order_id
- SubmitOrderResponse: order_id, status, reason
- MarketDataEvent: order book updates, candles, tick events
- TradeEvent: trade id, price, qty, order ids, tick

Notes
- Contracts are defined in contracts/market.proto.
- Generated clients live under agents/gen (Python) and exchange/gen (Go).
