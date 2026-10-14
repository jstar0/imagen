# Windows counterpart of install.sh. Same layout under the user profile:
#   $HOME\.local\share\imagen\bin\imagen.exe   (runtime build)
#   $HOME\.local\bin\imagen.exe                (copy; symlinks need extra privileges on Windows)
#   $HOME\.config\imagen\config.json           (never overwritten)
# RUNTIME_OUT, BIN_DIR, CONFIG_DIR, BACKUP_DIR and IMAGEN_CONFIG_SOURCE may be set as
# environment variables, exactly as for install.sh.
$ErrorActionPreference = 'Stop'
$Source = Split-Path -Parent $PSScriptRoot
function Get-Setting($Name, $Default) { $v = [Environment]::GetEnvironmentVariable($Name); if ($v) { $v } else { $Default } }
$RuntimeOut = Get-Setting 'RUNTIME_OUT' (Join-Path $HOME '.local\share\imagen\bin')
$BinDir = Get-Setting 'BIN_DIR' (Join-Path $HOME '.local\bin')
$ConfigDir = Get-Setting 'CONFIG_DIR' (Join-Path $HOME '.config\imagen')
$BackupDir = Get-Setting 'BACKUP_DIR' (Join-Path $HOME (".local\state\imagen\install-backups\" + (Get-Date -Format 'yyyyMMddTHHmmss')))
$ConfigSource = $env:IMAGEN_CONFIG_SOURCE
if ($ConfigSource -and -not (Test-Path -LiteralPath $ConfigSource -PathType Leaf)) { throw 'IMAGEN_CONFIG_SOURCE is not a file' }

foreach ($dir in @($RuntimeOut, $BinDir, $ConfigDir, $BackupDir)) { New-Item -ItemType Directory -Force -Path $dir | Out-Null }
$index = 0
foreach ($path in @((Join-Path $RuntimeOut 'imagen.exe'), (Join-Path $BinDir 'imagen.exe'), (Join-Path $ConfigDir 'config.json'))) {
  $index++
  if (Test-Path -LiteralPath $path) {
    $parent = Split-Path -Leaf (Split-Path -Parent $path)
    Copy-Item -LiteralPath $path -Destination (Join-Path $BackupDir "$index-$parent-$(Split-Path -Leaf $path)")
  }
}

Push-Location $Source
try {
  $revision = (git rev-parse --short HEAD 2>$null)
  if ($revision -and (git status --porcelain)) { $revision = "$revision-dirty" }
  $temporary = Join-Path $RuntimeOut (".imagen-" + [guid]::NewGuid().ToString('N') + '.exe')
  try {
    go build -trimpath "-ldflags=-s -w -X github.com/jstar0/imagen/internal/imagen.BuildCommit=$revision" -o $temporary ./cmd/imagen
    if ($LASTEXITCODE -ne 0) { throw "go build failed ($LASTEXITCODE)" }
    Move-Item -Force -LiteralPath $temporary -Destination (Join-Path $RuntimeOut 'imagen.exe')
  } finally {
    if (Test-Path -LiteralPath $temporary) { Remove-Item -Force -LiteralPath $temporary }
  }
} finally { Pop-Location }
Copy-Item -Force -LiteralPath (Join-Path $RuntimeOut 'imagen.exe') -Destination (Join-Path $BinDir 'imagen.exe')

if ($ConfigSource) {
  $target = Join-Path $ConfigDir 'config.json'
  if (Test-Path -LiteralPath $target) { Remove-Item -Force -LiteralPath $target }
  try { New-Item -ItemType SymbolicLink -Path $target -Target (Resolve-Path -LiteralPath $ConfigSource) | Out-Null }
  catch { New-Item -ItemType HardLink -Path $target -Target (Resolve-Path -LiteralPath $ConfigSource) | Out-Null }
}

"binary=$(Join-Path $BinDir 'imagen.exe')"
"source=$Source"
"revision=$revision"
"backup=$BackupDir"
$userPath = [Environment]::GetEnvironmentVariable('Path', 'User')
if (-not (($userPath -split ';') -contains $BinDir)) {
  "note: $BinDir is not on your user PATH; add it to run 'imagen' directly"
}
