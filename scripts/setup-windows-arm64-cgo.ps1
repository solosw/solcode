# Install a native aarch64 MinGW toolchain for CGO on Windows ARM64.
#
# GitHub's windows-11-arm image (and many ARM Windows boxes) expose an *x86_64*
# MinGW gcc via emulation. That assembler cannot compile runtime/cgo/gcc_arm64.S
# (`stp x29,x30,[sp,…]` → "no such instruction"). robotgo also needs a
# GCC-compatible Windows toolchain, so MSVC cl.exe is not a substitute.
#
# This script installs llvm-mingw (ucrt, aarch64 host) and exports
#   CC=aarch64-w64-mingw32-clang
# for subsequent steps (GITHUB_ENV / GITHUB_PATH on Actions; process env locally).
#
# Usage:
#   pwsh ./scripts/setup-windows-arm64-cgo.ps1
#   $env:LLVM_MINGW_VERSION = "20241030"; pwsh ./scripts/setup-windows-arm64-cgo.ps1
#
# Env:
#   LLVM_MINGW_VERSION  optional tag (default: 20241030)
#   LLVM_MINGW_PREFIX   install root (default: %USERPROFILE%\llvm-mingw)

[CmdletBinding()]
param()

$ErrorActionPreference = "Stop"

$Prefix = if ($env:LLVM_MINGW_PREFIX) {
    $env:LLVM_MINGW_PREFIX
} else {
    Join-Path $env:USERPROFILE "llvm-mingw"
}

$headers = @{
    "User-Agent" = "solcode-ci"
}

$clangName = "aarch64-w64-mingw32-clang.exe"
New-Item -ItemType Directory -Force -Path $Prefix | Out-Null

$bin = Get-ChildItem -Path $Prefix -Recurse -Directory -Filter bin -ErrorAction SilentlyContinue |
    Where-Object { Test-Path -LiteralPath (Join-Path $_.FullName $clangName) } |
    Select-Object -First 1

if ($bin) {
    Write-Host "Reusing existing llvm-mingw at $($bin.FullName)"
} else {
    $ver = if ($env:LLVM_MINGW_VERSION) { $env:LLVM_MINGW_VERSION.Trim() } else { "20241030" }
    $assetName = "llvm-mingw-$ver-ucrt-aarch64.zip"
    $url = "https://github.com/mstorsjo/llvm-mingw/releases/download/$ver/$assetName"
    Write-Host "Installing llvm-mingw $ver ($assetName)"

    $zip = Join-Path $env:TEMP $assetName
    Write-Host "Downloading $url ..."
    Invoke-WebRequest -Uri $url -OutFile $zip -Headers $headers -UseBasicParsing

    Write-Host "Extracting to $Prefix ..."
    Expand-Archive -Path $zip -DestinationPath $Prefix -Force

    $bin = Get-ChildItem -Path $Prefix -Recurse -Directory -Filter bin -ErrorAction SilentlyContinue |
        Where-Object { Test-Path -LiteralPath (Join-Path $_.FullName $clangName) } |
        Select-Object -First 1
    if (-not $bin) {
        throw "extracted llvm-mingw but did not find bin\$clangName under $Prefix"
    }
}

$cc = Join-Path $bin.FullName $clangName
Write-Host "C compiler: $cc"
$machine = & $cc -dumpmachine
Write-Host "dumpmachine: $machine"
if ($machine -notmatch 'aarch64|arm64') {
    throw "expected an aarch64 toolchain, got dumpmachine=$machine"
}

if ($env:GITHUB_PATH) {
    Add-Content -Path $env:GITHUB_PATH -Value $bin.FullName
} else {
    $env:Path = "$($bin.FullName);$env:Path"
}

$ccName = "aarch64-w64-mingw32-clang"
$cxxName = "aarch64-w64-mingw32-clang++"
if ($env:GITHUB_ENV) {
    Add-Content -Path $env:GITHUB_ENV -Value "CC=$ccName"
    Add-Content -Path $env:GITHUB_ENV -Value "CXX=$cxxName"
} else {
    $env:CC = $ccName
    $env:CXX = $cxxName
}

Write-Host "Exported CC=$ccName CXX=$cxxName"
Write-Host "PATH prepended: $($bin.FullName)"
