from __future__ import annotations

from agents.core.runner import AgentRunner, RunnerConfig
from agents.strategies.random_trader import RandomTrader


def main() -> int:
    agent = RandomTrader(
        agent_id="random-1",
        symbol="SIM",
        min_price=95,
        max_price=105,
        min_tick_gap=20,
    )
    runner = AgentRunner(
        RunnerConfig(
            host="127.0.0.1",
            port=50051,
            symbol="SIM",
        ),
        [agent],
    )
    runner.run()
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
