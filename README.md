# Market Simulator

A tick-based market simulator with a Go exchange (gRPC), Python agents, and a graphical UI.

## Quickstart (Local Dev)

### 1) Generate gRPC stubs

Requirements:
- protoc
- Go plugins: protoc-gen-go and protoc-gen-go-grpc
- Python packages: grpcio, grpcio-tools (see requirements.txt)

Python environment:
```
pip install -r .\requirements.txt
```

PowerShell:
```
.\scripts\gen_proto.ps1
```

### 2) Run the exchange server

```
cd exchange

go run .\cmd\exchange --listen :50051 --tick-ms 200
```

### 3) Stream ticks from a Python client

```
python .\agents\market_client.py stream-ticks --symbol SIM
```

### 3b) Stream trades from a Python client

```
python .\agents\market_client.py stream-trades --symbol SIM
```

### 4) Submit a test order

```
python .\agents\market_client.py submit-order --agent-id agent-1 --symbol SIM --side BUY --type LIMIT --qty 5 --price-ticks 100
```

## Repo Layout

- agents/ - Python agent strategies and client tooling
- contracts/ - Protobuf contracts
- exchange/ - Go gRPC exchange server
- simulator/ - Python simulation loop and candle builder (future)
- ui/ - Graphical UI (future)
- scripts/ - Utility scripts (proto generation, smoke tests)
- docs/ - Additional documentation

## Notes

- The exchange is API-first using gRPC.
- The server tick is fixed at 200ms by default.
- Go dependencies are managed in exchange/go.mod; entries added by go mod tidy (grpc/protobuf and indirects) are expected.

## Agent Runner

Run a fleet of random traders:
```
python .\agents\run_agents.py --count 5 --symbol SIM --min-tick-gap 20
```

Run a single random trader:
```
python .\agents\examples\run_single_random.py
```
