#!/usr/bin/env pwsh
Set-StrictMode -Version Latest
$ErrorActionPreference = 'Stop'

$Repo = 'mindsdb/yolocoder'
$InstallDir = if ($env:YOLOCODER_INSTALL_DIR) { $env:YOLOCODER_INSTALL_DIR } else { Join-Path $env:LOCALAPPDATA 'Programs\YoloCoder' }
$BinName = 'yolocoder.exe'

$architecture = [System.Runtime.InteropServices.RuntimeInformation]::ProcessArchitecture
$releaseArch = switch ($architecture) {
    'Arm64' { 'arm64' }
    'X64' { 'x86_64' }
    default { throw "Unsupported architecture: $architecture" }
}

$latestUrl = "https://github.com/$Repo/releases/latest"
$response = Invoke-WebRequest -Uri $latestUrl -MaximumRedirection 0 -SkipHttpErrorCheck
$location = $response.Headers.Location
if (-not $location) { throw 'Could not determine the latest release' }
$version = Split-Path $location -Leaf
$asset = "yolocoder_Windows_$releaseArch.zip"
$baseUrl = "https://github.com/$Repo/releases/download/$version"
$tempDir = Join-Path ([System.IO.Path]::GetTempPath()) ([System.Guid]::NewGuid())

try {
    New-Item -ItemType Directory -Path $tempDir | Out-Null
    $archivePath = Join-Path $tempDir $asset
    $checksumsPath = Join-Path $tempDir 'checksums.txt'
    Write-Host "Installing YoloCoder $version for Windows/$releaseArch..."
    Invoke-WebRequest -Uri "$baseUrl/$asset" -OutFile $archivePath
    Invoke-WebRequest -Uri "$baseUrl/checksums.txt" -OutFile $checksumsPath
    $checksumLine = Get-Content $checksumsPath | Where-Object { $_ -match "\s+$([regex]::Escape($asset))$" } | Select-Object -First 1
    if (-not $checksumLine) { throw "$asset is missing from checksums.txt" }
    $expected = ($checksumLine -split '\s+')[0].ToLowerInvariant()
    $actual = (Get-FileHash -Algorithm SHA256 $archivePath).Hash.ToLowerInvariant()
    if ($actual -ne $expected) { throw "Checksum mismatch for $asset" }
    Expand-Archive -Path $archivePath -DestinationPath $tempDir -Force
    New-Item -ItemType Directory -Path $InstallDir -Force | Out-Null
    Copy-Item (Join-Path $tempDir $BinName) (Join-Path $InstallDir $BinName) -Force
    $userPath = [Environment]::GetEnvironmentVariable('Path', 'User')
    if (($userPath -split ';') -notcontains $InstallDir) {
        [Environment]::SetEnvironmentVariable('Path', (($userPath.TrimEnd(';') + ';' + $InstallDir).TrimStart(';')), 'User')
        Write-Host 'Added the install directory to your user PATH. Open a new terminal to use it.'
    }
    Write-Host "Installed $BinName to $InstallDir"
} finally {
    Remove-Item $tempDir -Recurse -Force -ErrorAction SilentlyContinue
}
