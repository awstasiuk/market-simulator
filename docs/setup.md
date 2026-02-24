Setup Notes

Proto Generation
- The script in scripts/gen_proto.ps1 generates Go stubs into exchange/gen and Python stubs into agents/gen.
- Ensure protoc and Go plugins are on PATH.
- For Python, install dependencies from requirements.txt in your active environment.

Python Environment
- Create and activate a venv.
- Install requirements: pip install -r .\requirements.txt

Running the Exchange
- The exchange server listens on :50051 by default.
- Tick duration is configurable via --tick-ms.

Go Module Dependencies (go.mod)
- google.golang.org/grpc v1.62.1: gRPC runtime used by the exchange server.
- google.golang.org/protobuf v1.32.0: protobuf runtime used by generated Go stubs.
- Indirect dependencies (golang.org/x/*, google.golang.org/genproto, github.com/golang/protobuf) are pulled in by the gRPC and protobuf libraries during go mod tidy.
- These entries are expected and should remain in sync with the generated stubs and gRPC server implementation.

Client Validation
- Use agents/market_client.py to stream ticks and submit orders.
- Expect tick events every 200ms when streaming.
