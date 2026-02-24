$ErrorActionPreference = "Stop"

$root = Split-Path -Parent $PSScriptRoot
$exchangeRoot = Join-Path $root "exchange"
$proto = Join-Path $root "contracts\market.proto"

$goGenDir = Join-Path $exchangeRoot "gen"
$pyOut = Join-Path $root "agents\gen"

if (Test-Path $goGenDir) {
  Remove-Item -Recurse -Force $goGenDir
}

if (Test-Path $pyOut) {
  Remove-Item -Recurse -Force $pyOut
}

New-Item -ItemType Directory -Force -Path $goGenDir | Out-Null
New-Item -ItemType Directory -Force -Path $pyOut | Out-Null

# Go: requires protoc and protoc-gen-go/protoc-gen-go-grpc on PATH.
protoc `
  --proto_path=$root\contracts `
  --go_out=$exchangeRoot `
  --go_opt=module=market-simulator/exchange `
  --go-grpc_out=$exchangeRoot `
  --go-grpc_opt=module=market-simulator/exchange `
  $proto

# Python: requires grpcio-tools in the active Python environment.
python -m grpc_tools.protoc `
  --proto_path=$root\contracts `
  --python_out=$pyOut `
  --grpc_python_out=$pyOut `
  $proto

New-Item -ItemType File -Force -Path (Join-Path $pyOut "__init__.py") | Out-Null

Write-Host "Proto generation complete."
