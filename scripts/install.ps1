# Installs zharf, the command-line tool for platform.zharf.ai, on Windows.
#
#   irm https://platform.zharf.ai/cli/install.ps1 | iex
#
# Settings (environment):
#   ZHARF_INSTALL_DIR   where to put zharf.exe (default: %LOCALAPPDATA%\zharf)
#   ZHARF_BASE_URL      where to download from (default: https://platform.zharf.ai)
#   HTTPS_PROXY         a proxy for the download, if you need one

$ErrorActionPreference = 'Stop'
$ProgressPreference = 'SilentlyContinue'
[Net.ServicePointManager]::SecurityProtocol = [Net.SecurityProtocolType]::Tls12

$base = if ($env:ZHARF_BASE_URL) { $env:ZHARF_BASE_URL.TrimEnd('/') } else { 'https://platform.zharf.ai' }
if ($base.EndsWith('/v1')) { $base = $base.Substring(0, $base.Length - 3) }

# Windows on ARM runs the x64 build.
$arch = 'amd64'

$dir = if ($env:ZHARF_INSTALL_DIR) { $env:ZHARF_INSTALL_DIR } else { Join-Path $env:LOCALAPPDATA 'zharf' }
New-Item -ItemType Directory -Force -Path $dir | Out-Null
$exe = Join-Path $dir 'zharf.exe'
# The download must end in .exe, or PowerShell will not run it for the version check.
$tmp = Join-Path $dir 'zharf.new.exe'
$url = "$base/cli/zharf-windows-$arch.exe"

$proxy = if ($env:HTTPS_PROXY) { $env:HTTPS_PROXY } elseif ($env:HTTP_PROXY) { $env:HTTP_PROXY } else { $null }
$request = @{ Uri = $url; OutFile = $tmp; UseBasicParsing = $true }
if ($proxy) { $request.Proxy = $proxy }

Write-Host "Downloading zharf for windows/$arch from $url"
try {
    Invoke-WebRequest @request
} catch {
    Remove-Item -Force -ErrorAction SilentlyContinue $tmp
    throw "zharf install: download failed: $($_.Exception.Message)"
}

$size = (Get-Item $tmp).Length
if ($size -lt 1000000) {
    Remove-Item -Force $tmp
    throw "zharf install: the download is too small ($size bytes); is $base reachable?"
}

$version = & $tmp version 2>$null
if ($LASTEXITCODE -ne 0) {
    Remove-Item -Force $tmp
    throw 'zharf install: the downloaded program does not run on this system'
}

if (Test-Path $exe) {
    # A running zharf.exe cannot be overwritten, but it can be renamed.
    Remove-Item -Force -ErrorAction SilentlyContinue "$exe.old"
    Rename-Item -Force $exe "$exe.old"
}
Move-Item -Force $tmp $exe
Write-Host "Installed $version to $exe"

$userPath = [Environment]::GetEnvironmentVariable('Path', 'User')
$parts = if ($userPath) { $userPath.Split(';') } else { @() }
if ($parts -notcontains $dir) {
    $newPath = if ($userPath) { "$userPath;$dir" } else { $dir }
    [Environment]::SetEnvironmentVariable('Path', $newPath, 'User')
    $env:Path = "$env:Path;$dir"
    Write-Host ""
    Write-Host "Added $dir to your user PATH. Open a new terminal for other windows to see it."
}

Write-Host ""
Write-Host 'Next:  zharf login'
Write-Host 'Then:  zharf run qwen3.5-9b "Hello"'
