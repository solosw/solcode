# Install solcode from the rolling "master" GitHub Release (no Go required).
#
# CI publishes binaries on every push to master under release tag "master".
# There is no "latest" version channel — install tracks master unless -Version is set.
#
# Usage (PowerShell):
#   irm https://raw.githubusercontent.com/solosw/solcode/master/scripts/install.ps1 | iex
#   & .\scripts\install.ps1 -InstallDir "$env:USERPROFILE\bin"
#   & .\scripts\install.ps1 -Version v0.1.0   # optional pinned tag
#
# Env:
#   SOLCODE_REPO, SOLCODE_VERSION, SOLCODE_INSTALL_DIR, GITHUB_TOKEN

[CmdletBinding()]
param(
    [string]$Version = $(if ($env:SOLCODE_VERSION) { $env:SOLCODE_VERSION } else { "master" }),
    [string]$Repo = $(if ($env:SOLCODE_REPO) { $env:SOLCODE_REPO } else { "solosw/solcode" }),
    [string]$InstallDir = $(if ($env:SOLCODE_INSTALL_DIR) { $env:SOLCODE_INSTALL_DIR } else { Join-Path $env:LOCALAPPDATA "solcode\bin" }),
    [switch]$NoPath
)

$ErrorActionPreference = "Stop"
$BinaryName = "solcode.exe"
$GitHubBase = if ($env:GITHUB_BASE) { $env:GITHUB_BASE } else { "https://github.com" }

# No "latest" channel — map to master.
if (-not $Version -or $Version -eq "latest") {
    $Version = "master"
}

function Get-Headers {
    $h = @{
        "User-Agent" = "solcode-install"
        "Accept"     = "application/vnd.github+json"
    }
    if ($env:GITHUB_TOKEN) {
        $h["Authorization"] = "Bearer $($env:GITHUB_TOKEN)"
    }
    return $h
}

function Resolve-Arch {
    switch ($env:PROCESSOR_ARCHITECTURE) {
        "AMD64" { return "amd64" }
        "ARM64" { return "arm64" }
        "x86"   { return "386" }
        default {
            if ([Environment]::Is64BitOperatingSystem) { return "amd64" }
            throw "Unsupported architecture: $($env:PROCESSOR_ARCHITECTURE)"
        }
    }
}

$os = "windows"
$arch = Resolve-Arch
$tag = $Version

$candidates = @(
    "solcode_${tag}_${os}_${arch}.zip",
    "solcode_$($tag.TrimStart('v'))_${os}_${arch}.zip"
) | Select-Object -Unique

Write-Host "Channel/tag: $tag"
Write-Host "Repo:        $Repo"
Write-Host "Target:      $os/$arch"

$tmp = Join-Path ([System.IO.Path]::GetTempPath()) ("solcode-install-" + [guid]::NewGuid().ToString("n"))
New-Item -ItemType Directory -Path $tmp | Out-Null
try {
    $zipPath = $null
    foreach ($asset in $candidates) {
        $url = "$GitHubBase/$Repo/releases/download/$tag/$asset"
        $dest = Join-Path $tmp $asset
        Write-Host "Downloading $asset ..."
        try {
            Invoke-WebRequest -Uri $url -OutFile $dest -Headers (Get-Headers) -UseBasicParsing
            $zipPath = $dest
            break
        } catch {
            Write-Host "  not found: $asset"
        }
    }
    if (-not $zipPath) {
        throw @"
Failed to download release asset for $os/$arch (tag $tag, repo $Repo).
Tried: $($candidates -join ', ')

Hint: push to master so CI publishes the rolling "master" release,
      or pass -Version <tag> for a versioned release.
"@
    }

    Write-Host "Extracting ..."
    Expand-Archive -Path $zipPath -DestinationPath $tmp -Force

    $src = Get-ChildItem -Path $tmp -Recurse -File -Filter $BinaryName | Select-Object -First 1
    if (-not $src) {
        $src = Get-ChildItem -Path $tmp -Recurse -File -Filter "solcode*" | Where-Object { $_.Extension -eq ".exe" } | Select-Object -First 1
    }
    if (-not $src) {
        throw "Binary not found inside archive"
    }

    New-Item -ItemType Directory -Force -Path $InstallDir | Out-Null
    $dest = Join-Path $InstallDir $BinaryName
    Copy-Item -Path $src.FullName -Destination $dest -Force

    Write-Host ""
    Write-Host "Installed: $dest"
    Write-Host "Version:   $tag"

    if (-not $NoPath) {
        $userPath = [Environment]::GetEnvironmentVariable("Path", "User")
        if (-not $userPath) { $userPath = "" }
        $parts = $userPath -split ';' | Where-Object { $_ -ne "" }
        if ($parts -notcontains $InstallDir) {
            $newPath = if ($userPath.TrimEnd(';')) { "$userPath;$InstallDir" } else { $InstallDir }
            [Environment]::SetEnvironmentVariable("Path", $newPath, "User")
            $env:Path = "$env:Path;$InstallDir"
            Write-Host "Added to user PATH: $InstallDir"
            Write-Host "(Open a new terminal if 'solcode' is not found yet.)"
        } else {
            Write-Host "PATH already includes $InstallDir"
            if (($env:Path -split ';') -notcontains $InstallDir) {
                $env:Path = "$env:Path;$InstallDir"
            }
        }
    }

    Write-Host ""
    Write-Host "Next:"
    Write-Host '  $env:ANTHROPIC_API_KEY = "sk-ant-..."'
    Write-Host "  solcode"
    Write-Host ""
    Write-Host "Done."
}
finally {
    Remove-Item -Recurse -Force $tmp -ErrorAction SilentlyContinue
}
