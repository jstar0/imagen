param(
    [string]$RuntimeOut = (Join-Path $env:USERPROFILE '.local/share/imagen/bin'),
    [string]$BinDir = (Join-Path $env:USERPROFILE '.local/bin'),
    [string]$ConfigDir = (Join-Path $env:USERPROFILE '.config/imagen'),
    [string]$BackupDir = (Join-Path $env:USERPROFILE ('.local/state/imagen/install-backups/' + (Get-Date -Format 'yyyyMMddTHHmmss')))
)
$ErrorActionPreference = 'Stop'
$sourceRoot = Split-Path $PSScriptRoot -Parent
if ($env:IMAGEN_CONFIG_SOURCE -and !(Test-Path -LiteralPath $env:IMAGEN_CONFIG_SOURCE -PathType Leaf)) {
    throw 'IMAGEN_CONFIG_SOURCE is not a file'
}
foreach ($directory in @($RuntimeOut, $BinDir, $ConfigDir, $BackupDir)) {
    New-Item -ItemType Directory -Path $directory -Force | Out-Null
}
$runtime = Join-Path $RuntimeOut 'imagen.exe'
$installed = Join-Path $BinDir 'imagen.exe'
$config = Join-Path $ConfigDir 'config.json'
$index = 0
foreach ($path in @($runtime, $installed, $config)) {
    $index++
    if (Test-Path -LiteralPath $path) {
        Copy-Item -LiteralPath $path -Destination (Join-Path $BackupDir ($index.ToString() + '-' + (Split-Path $path -Leaf)))
    }
}
$temporary = Join-Path $RuntimeOut ('.imagen-' + [guid]::NewGuid().ToString('N') + '.exe')
Push-Location $sourceRoot
try {
    $revision = (& git rev-parse --short HEAD 2>$null)
    if ($revision -and (& git status --porcelain)) { $revision += '-dirty' }
    & go build -trimpath "-ldflags=-s -w -X github.com/jstar0/imagen/internal/imagen.BuildCommit=$revision" -o $temporary ./cmd/imagen
    if ($LASTEXITCODE -ne 0) { throw 'go build failed' }
    Move-Item -LiteralPath $temporary -Destination $runtime -Force
    # 复制可执行文件，避免 Windows 符号链接权限要求。
    if ([IO.Path]::GetFullPath($runtime) -ne [IO.Path]::GetFullPath($installed)) {
        Copy-Item -LiteralPath $runtime -Destination $installed -Force
    }
    # 保留现有配置；仅在用户显式指定来源时复制配置。
    if ($env:IMAGEN_CONFIG_SOURCE -and [IO.Path]::GetFullPath($env:IMAGEN_CONFIG_SOURCE) -ne [IO.Path]::GetFullPath($config)) {
        Copy-Item -LiteralPath $env:IMAGEN_CONFIG_SOURCE -Destination $config -Force
    }
    Write-Output "binary=$installed"
    Write-Output "source=$sourceRoot"
    Write-Output "revision=$revision"
    Write-Output "backup=$BackupDir"
} finally {
    Pop-Location
    if (Test-Path -LiteralPath $temporary) { Remove-Item -LiteralPath $temporary }
}
