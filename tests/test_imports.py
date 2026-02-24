from agents.gen import market_pb2
from agents.gen import market_pb2_grpc


def main() -> int:
    assert market_pb2 is not None
    assert market_pb2_grpc is not None
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
