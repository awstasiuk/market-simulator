from __future__ import annotations

import argparse

from agents.core.runner import AgentRunner, RunnerConfig
from agents.strategies.random_trader import RandomTrader


def build_parser() -> argparse.ArgumentParser:
    parser = argparse.ArgumentParser(description="Run a fleet of random traders")
    parser.add_argument("--host", default="127.0.0.1", help="Exchange host")
    parser.add_argument("--port", type=int, default=50051, help="Exchange port")
    parser.add_argument("--symbol", default="SIM", help="Symbol to trade")
    parser.add_argument("--count", type=int, default=5, help="Number of agents")
    parser.add_argument("--min-price", type=int, default=95, help="Minimum price ticks")
    parser.add_argument("--max-price", type=int, default=105, help="Maximum price ticks")
    parser.add_argument("--min-qty", type=int, default=1, help="Minimum order size")
    parser.add_argument("--max-qty", type=int, default=3, help="Maximum order size")
    parser.add_argument("--seed", type=int, default=7, help="Base RNG seed")
    parser.add_argument(
        "--min-tick-gap",
        type=int,
        default=20,
        help="Minimum ticks between orders for random agents",
    )
    return parser


def main() -> int:
    args = build_parser().parse_args()
    agents = []

    for i in range(args.count):
        agents.append(
            RandomTrader(
                agent_id=f"random-{i+1}",
                symbol=args.symbol,
                min_price=args.min_price,
                max_price=args.max_price,
                min_qty=args.min_qty,
                max_qty=args.max_qty,
                seed=args.seed + i,
                min_tick_gap=args.min_tick_gap,
            )
        )

    runner = AgentRunner(
        RunnerConfig(
            host=args.host,
            port=args.port,
            symbol=args.symbol,
        ),
        agents,
    )
    runner.run()
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
