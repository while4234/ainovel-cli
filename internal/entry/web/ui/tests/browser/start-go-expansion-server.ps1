$ErrorActionPreference = 'Stop'
$go = $env:AINOVEL_GO
if (-not $go) {
  $command = Get-Command go -ErrorAction SilentlyContinue
  if ($command) { $go = $command.Source }
}
if (-not $go) {
  $go = 'C:\Users\RondleLiu\.codex\tools\go1.25.5\go\bin\go.exe'
}
if (-not (Test-Path -LiteralPath $go)) {
  throw 'Go toolchain not found; set AINOVEL_GO to go.exe'
}
$repo = Resolve-Path (Join-Path $PSScriptRoot '..\..\..\..\..\..')
Push-Location $repo
try {
  $layout = Join-Path ([System.IO.Path]::GetTempPath()) 'ainovel-expansion-release-layout'
  New-Item -ItemType Directory -Force -Path $layout | Out-Null
  $auditor = Join-Path $layout 'expansion-auditor.exe'
  $server = Join-Path $layout 'expansion-browser-server.exe'
  & $go build -o $auditor ./cmd/expansion-auditor
  if ($LASTEXITCODE -ne 0) { exit $LASTEXITCODE }
  & $go build -tags acceptance -o $server ./cmd/expansion-browser-server
  if ($LASTEXITCODE -ne 0) { exit $LASTEXITCODE }
  Remove-Item Env:AINOVEL_EXPANSION_AUDITOR -ErrorAction SilentlyContinue
  & $server
  exit $LASTEXITCODE
} finally {
  Pop-Location
}
