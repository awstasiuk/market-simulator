$ErrorActionPreference = "Stop"
$env:PYTHONUNBUFFERED = "1"

$root = Split-Path -Parent $PSScriptRoot
$exchangeRoot = Join-Path $root "exchange"
$client = Join-Path $root "agents\market_client.py"

function Get-FreePort {
  $listener = [System.Net.Sockets.TcpListener]::new([System.Net.IPAddress]::Loopback, 0)
  $listener.Start()
  $port = $listener.LocalEndpoint.Port
  $listener.Stop()
  return $port
}

Write-Host "Generating protobuf stubs..."
& "$root\scripts\gen_proto.ps1"

Write-Host "Starting exchange server..."
$port = Get-FreePort
Push-Location $exchangeRoot
$serverJob = Start-Job -ScriptBlock {
  param($cwd, $listenPort)
  Set-Location $cwd
  go run .\cmd\exchange --listen (":" + $listenPort) --tick-ms 200
} -ArgumentList $exchangeRoot, $port
Pop-Location

Start-Sleep -Seconds 2

if ($serverJob.State -ne "Running") {
  Write-Host "Exchange server failed to start."
  Receive-Job $serverJob | ForEach-Object { Write-Host $_ }
  throw "Exchange server failed"
}

$ready = $false
for ($i = 0; $i -lt 10; $i++) {
  $conn = Test-NetConnection -ComputerName 127.0.0.1 -Port $port -WarningAction SilentlyContinue
  if ($conn.TcpTestSucceeded) {
    $ready = $true
    break
  }
  Start-Sleep -Milliseconds 300
}

if (-not $ready) {
  Write-Host ("Exchange server did not open port " + $port + ".")
  Receive-Job $serverJob | ForEach-Object { Write-Host $_ }
  throw "Exchange server did not open port"
}

Write-Host "Starting trade stream listener..."
$tradeOut = Join-Path $root "scripts\trade_stream.log"
if (Test-Path $tradeOut) {
  Remove-Item $tradeOut -Force
}

$tradeProc = Start-Process -FilePath python -ArgumentList @("-u", $client, "--port", $port, "stream-trades", "--symbol", "SIM") -RedirectStandardOutput $tradeOut -NoNewWindow -PassThru

Write-Host "Submitting test order..."
python $client --port $port submit-order --agent-id smoke --symbol SIM --side BUY --type LIMIT --qty 1 --price-ticks 100

Write-Host "Submitting crossing order to generate a trade..."
python $client --port $port submit-order --agent-id smoke --symbol SIM --side SELL --type LIMIT --qty 1 --price-ticks 100

Write-Host "Streaming two ticks..."
$tickLines = python -u $client --port $port stream-ticks --symbol SIM | Select-Object -First 2
$tickLines | ForEach-Object { Write-Host $_ }

Write-Host "Waiting for one trade..."
$tradeLine = $null
 for ($i = 0; $i -lt 20; $i++) {
  if (Test-Path $tradeOut) {
    $tradeLine = Get-Content $tradeOut -TotalCount 1
    if ($tradeLine) {
      break
    }
  }
  Start-Sleep -Milliseconds 200
}

if ($tradeLine) {
  Write-Host $tradeLine
} else {
  Write-Host "No trade received in time."
}

if (-not $tradeProc.HasExited) {
  Stop-Process -Id $tradeProc.Id -Force
}

Write-Host "Stopping exchange server..."
Stop-Job $serverJob -ErrorAction SilentlyContinue
Wait-Job $serverJob -Timeout 5 | Out-Null
Remove-Job $serverJob -ErrorAction SilentlyContinue

Write-Host "Smoke test complete."
