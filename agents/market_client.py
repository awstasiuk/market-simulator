import argparse
import time

import grpc

from agents.gen import market_pb2
from agents.gen import market_pb2_grpc


def stream_ticks(stub: market_pb2_grpc.ExchangeServiceStub, symbol: str) -> None:
    request = market_pb2.MarketDataRequest(
        symbol=symbol,
        include_order_book=False,
        include_candles=False,
    )

    for event in stub.StreamMarketData(request):
        payload = event.WhichOneof("payload")
        if payload == "tick":
            tick = event.tick
            print(f"tick={tick.tick} server_time_ms={tick.server_time_unix_ms}")
        else:
            print(f"event={payload}")


def stream_trades(stub: market_pb2_grpc.ExchangeServiceStub, symbol: str) -> None:
    request = market_pb2.TradeStreamRequest(symbol=symbol)

    for event in stub.StreamTrades(request):
        print(
            "trade_id={} symbol={} price_ticks={} qty={} buy_order_id={} sell_order_id={} tick={}".format(
                event.trade_id,
                event.symbol,
                event.price_ticks,
                event.qty,
                event.buy_order_id,
                event.sell_order_id,
                event.tick,
            )
        )


def submit_order(
    stub: market_pb2_grpc.ExchangeServiceStub,
    agent_id: str,
    symbol: str,
    side: str,
    order_type: str,
    qty: int,
    price_ticks: int | None,
    client_order_id: int,
) -> None:
    side_enum = market_pb2.OrderSide.Value(f"ORDER_SIDE_{side}")
    type_enum = market_pb2.OrderType.Value(f"ORDER_TYPE_{order_type}")

    request = market_pb2.SubmitOrderRequest(
        agent_id=agent_id,
        symbol=symbol,
        side=side_enum,
        type=type_enum,
        qty=qty,
        client_order_id=client_order_id,
    )

    if price_ticks is not None:
        request.price_ticks = price_ticks

    response = stub.SubmitOrder(request)
    print(
        f"order_id={response.order_id} status={market_pb2.OrderStatus.Name(response.status)} "
        f"reason={response.reason}"
    )


def create_account(
    stub: market_pb2_grpc.ExchangeServiceStub,
    agent_id: str,
    cash_ticks: int,
    positions: list[tuple[str, int]],
) -> None:
    pos_entries = [
        market_pb2.PositionEntry(symbol=sym, qty=qty) for sym, qty in positions
    ]
    request = market_pb2.CreateAccountRequest(
        agent_id=agent_id,
        cash_ticks=cash_ticks,
        positions=pos_entries,
    )
    response = stub.CreateAccount(request)
    if response.ok:
        print(f"account created: agent_id={agent_id} cash_ticks={cash_ticks}")
    else:
        print(f"create_account failed: {response.reason}")


def get_portfolio(
    stub: market_pb2_grpc.ExchangeServiceStub,
    agent_id: str,
) -> None:
    request = market_pb2.GetPortfolioRequest(agent_id=agent_id)
    response = stub.GetPortfolio(request)
    if not response.found:
        print(f"no account found for agent_id={agent_id}")
        return
    available = response.cash - response.reserved_cash
    print(
        f"agent_id={agent_id}  cash={response.cash}  "
        f"reserved_cash={response.reserved_cash}  available_cash={available}"
    )
    for pos in response.positions:
        reserved = next(
            (r.qty for r in response.reserved_shares if r.symbol == pos.symbol), 0
        )
        print(
            f"  {pos.symbol}: total={pos.qty}  reserved={reserved}  available={pos.qty - reserved}"
        )


def main() -> int:
    parser = argparse.ArgumentParser(
        description="Market simulator client for exchange API testing"
    )
    parser.add_argument("--host", default="127.0.0.1", help="Exchange host")
    parser.add_argument("--port", type=int, default=50051, help="Exchange port")

    subparsers = parser.add_subparsers(dest="command", required=True)

    stream_parser = subparsers.add_parser("stream-ticks", help="Stream tick events")
    stream_parser.add_argument("--symbol", default="SIM", help="Symbol to subscribe")

    trades_parser = subparsers.add_parser("stream-trades", help="Stream trade events")
    trades_parser.add_argument("--symbol", default="SIM", help="Symbol to subscribe")

    order_parser = subparsers.add_parser("submit-order", help="Submit a test order")
    order_parser.add_argument("--agent-id", default="agent-1", help="Agent id")
    order_parser.add_argument("--symbol", default="SIM", help="Symbol")
    order_parser.add_argument(
        "--side",
        choices=["BUY", "SELL"],
        default="BUY",
        help="Order side",
    )
    order_parser.add_argument(
        "--type",
        choices=["LIMIT", "MARKET"],
        default="LIMIT",
        help="Order type",
    )
    order_parser.add_argument("--qty", type=int, default=1, help="Quantity")
    order_parser.add_argument(
        "--price-ticks",
        type=int,
        default=None,
        help="Price ticks for limit orders",
    )
    order_parser.add_argument(
        "--client-order-id",
        type=int,
        default=1,
        help="Client-side order id",
    )

    acct_parser = subparsers.add_parser("create-account", help="Create an agent account")
    acct_parser.add_argument("--agent-id", required=True, help="Agent id")
    acct_parser.add_argument(
        "--cash-ticks", type=int, default=0,
        help="Initial cash balance in price ticks (e.g. 1000000 = $10,000)"
    )
    acct_parser.add_argument(
        "--position", action="append", default=[], metavar="SYMBOL:QTY",
        help="Initial stock position, e.g. SIM:500. May repeat.",
    )

    pf_parser = subparsers.add_parser("get-portfolio", help="Print an agent's portfolio")
    pf_parser.add_argument("--agent-id", required=True, help="Agent id")

    args = parser.parse_args()

    address = f"{args.host}:{args.port}"
    channel = grpc.insecure_channel(address)
    stub = market_pb2_grpc.ExchangeServiceStub(channel)

    try:
        if args.command == "stream-ticks":
            stream_ticks(stub, args.symbol)
        elif args.command == "stream-trades":
            stream_trades(stub, args.symbol)
        elif args.command == "submit-order":
            submit_order(
                stub,
                agent_id=args.agent_id,
                symbol=args.symbol,
                side=args.side,
                order_type=args.type,
                qty=args.qty,
                price_ticks=args.price_ticks,
                client_order_id=args.client_order_id,
            )
        elif args.command == "create-account":
            positions = []
            for p in args.position:
                sym, _, qty_str = p.partition(":")
                if not sym or not qty_str:
                    print(f"invalid position format {p!r} (expected SYMBOL:QTY)")
                    return 1
                positions.append((sym, int(qty_str)))
            create_account(stub, args.agent_id, args.cash_ticks, positions)
        elif args.command == "get-portfolio":
            get_portfolio(stub, args.agent_id)
    except KeyboardInterrupt:
        print("stopping client")
        time.sleep(0.1)
        return 0

    return 0


if __name__ == "__main__":
    raise SystemExit(main())
