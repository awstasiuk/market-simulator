"""Run a simulation of 100 market-following traders against the exchange.

Each trader:
- Opens with a personal reference price near 5000 ticks ($50) ± 5 %.
- Checks for new trades via the trade stream and re-anchors to the last
  traded price.
- Places limit orders at random intervals (every 5–50 ticks) at the
  current anchor price ± up to 2 %, on a randomly chosen BUY or SELL side.

Each trader's portfolio is seeded via the CreateAccount RPC before trading
begins. The exchange enforces solvency: orders that exceed the agent's cash
or share balance are rejected.

Usage (exchange must be running first):
    python -m agents.examples.run_sim_100
    python -m agents.examples.run_sim_100 --host 127.0.0.1 --port 50051
"""

from __future__ import annotations

import argparse

import grpc

from agents.core.runner import AgentRunner, RunnerConfig
from agents.gen import market_pb2, market_pb2_grpc
from agents.strategies.market_following_trader import MarketFollowingTrader

_NUM_TRADERS = 100
_SYMBOL = "SIM"
_BASE_PRICE = 5000           # ticks — centre of the $50 price range
_INITIAL_SPREAD_PCT = 0.05   # each trader's opening reference ± 5 %
_PERTURB_PCT = 0.02          # per-order price perturbation ± 2 %
_MIN_QTY = 1
_MAX_QTY = 10
_MIN_TICK_GAP = 5            # minimum ticks between orders per trader
_MAX_TICK_GAP = 50           # maximum ticks between orders per trader
_BASE_SEED = 42              # deterministic: trader i uses seed BASE_SEED + i
# Initial funding per trader
_INITIAL_CASH_TICKS = 500_000   # 500 000 ticks ≈ $5 000 at 100 ticks/$
_INITIAL_SHARES = 200           # shares of the symbol each trader starts with


def build_parser() -> argparse.ArgumentParser:
    parser = argparse.ArgumentParser(
        description="Run 100 market-following random traders against the exchange."
    )
    parser.add_argument("--host", default="127.0.0.1", help="Exchange gRPC host")
    parser.add_argument("--port", type=int, default=50051, help="Exchange gRPC port")
    parser.add_argument("--symbol", default=_SYMBOL, help="Symbol to trade")
    parser.add_argument("--count", type=int, default=_NUM_TRADERS, help="Number of traders")
    parser.add_argument("--base-price", type=int, default=_BASE_PRICE,
                        help="Centre price in ticks (default 5000 ≈ $50)")
    parser.add_argument("--initial-spread-pct", type=float, default=_INITIAL_SPREAD_PCT,
                        help="Initial opening price spread as a fraction (default 0.05 = ±5%%)")
    parser.add_argument("--perturb-pct", type=float, default=_PERTURB_PCT,
                        help="Per-order price perturbation as a fraction (default 0.02 = ±2%%)")
    parser.add_argument("--min-qty", type=int, default=_MIN_QTY)
    parser.add_argument("--max-qty", type=int, default=_MAX_QTY)
    parser.add_argument("--min-tick-gap", type=int, default=_MIN_TICK_GAP,
                        help="Minimum ticks between orders per trader")
    parser.add_argument("--max-tick-gap", type=int, default=_MAX_TICK_GAP,
                        help="Maximum ticks between orders per trader")
    parser.add_argument("--base-seed", type=int, default=_BASE_SEED,
                        help="Base RNG seed; trader i uses base_seed + i")
    parser.add_argument("--initial-cash", type=int, default=_INITIAL_CASH_TICKS,
                        help="Starting cash per trader in price ticks (default 500000 ≈ $5000)")
    parser.add_argument("--initial-shares", type=int, default=_INITIAL_SHARES,
                        help="Starting share count per trader (default 200)")
    return parser


def seed_accounts(
    stub: market_pb2_grpc.ExchangeServiceStub,
    agent_ids: list[str],
    symbol: str,
    cash_ticks: int,
    shares: int,
) -> None:
    """Call CreateAccount for each trader. Silently skips accounts that already exist."""
    created = skipped = 0
    for agent_id in agent_ids:
        positions = (
            [market_pb2.PositionEntry(symbol=symbol, qty=shares)] if shares > 0 else []
        )
        resp = stub.CreateAccount(
            market_pb2.CreateAccountRequest(
                agent_id=agent_id,
                cash_ticks=cash_ticks,
                positions=positions,
            )
        )
        if resp.ok:
            created += 1
        else:
            skipped += 1
    print(f"Accounts: {created} created, {skipped} already existed.")


def main() -> int:
    args = build_parser().parse_args()

    traders = [
        MarketFollowingTrader(
            agent_id=f"mf-trader-{i + 1:03d}",
            symbol=args.symbol,
            base_price=args.base_price,
            initial_spread_pct=args.initial_spread_pct,
            perturb_pct=args.perturb_pct,
            min_qty=args.min_qty,
            max_qty=args.max_qty,
            min_tick_gap=args.min_tick_gap,
            max_tick_gap=args.max_tick_gap,
            seed=args.base_seed + i,
        )
        for i in range(args.count)
    ]

    # Seed agent accounts before starting the runner so each trader has
    # funded balances when the exchange enforces solvency checks.
    address = f"{args.host}:{args.port}"
    with grpc.insecure_channel(address) as channel:
        stub = market_pb2_grpc.ExchangeServiceStub(channel)
        seed_accounts(
            stub,
            agent_ids=[t.agent_id for t in traders],
            symbol=args.symbol,
            cash_ticks=args.initial_cash,
            shares=args.initial_shares,
        )

    print(
        f"Starting {len(traders)} market-following traders on {address} "
        f"symbol={args.symbol} base_price={args.base_price} "
        f"perturb=±{args.perturb_pct * 100:.0f}%  "
        f"cash_ticks={args.initial_cash}  initial_shares={args.initial_shares}"
    )

    runner = AgentRunner(
        RunnerConfig(
            host=args.host,
            port=args.port,
            symbol=args.symbol,
            include_order_book=False,
            include_candles=False,
            include_trades=True,
        ),
        traders,
    )
    runner.run()
    return 0


if __name__ == "__main__":
    raise SystemExit(main())