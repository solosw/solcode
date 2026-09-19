# Install solcode from the rolling "master" GitHub Release (no Go required).
#
# CI publishes binaries on every push to master under release tag "master".
# There is no "latest" version channel — install tracks master unless -Version is set.
#
# Usage (PowerShell):
#   irm https://raw.githubusercontent.com/solosw/solcode/master/scripts/install.ps1 | iex
#   & .\scripts\install.ps1 -InstallDir "$env:USERPROFILE\bin"
#   & .\scripts\install.ps1 -ComputerUse              # CGO + robotgo build
#   & .\scripts\install.ps1 -Version v0.1.0           # optional pinned tag
#
# Env:
#   SOLCODE_REPO, SOLCODE_VERSION, SOLCODE_INSTALL_DIR, SOLCODE_COMPUTER_USE, GITHUB_TOKEN

[CmdletBinding()]
param(
    [string]$Version = $(if ($env:SOLCODE_VERSION) { $env:SOLCODE_VERSION } else { "master" }),
    [string]$Repo = $(if ($env:SOLCODE_REPO) { $env:SOLCODE_REPO } else { "solosw/solcode" }),
    [string]$InstallDir = $(if ($env:SOLCODE_INSTALL_DIR) { $env:SOLCODE_INSTALL_DIR } else { Join-Path $env:LOCALAPPDATA "solcode\bin" }),
    [switch]$ComputerUse,
    [switch]$NoPath
)

$ErrorActionPreference = "Stop"
$BinaryName = "solcode.exe"
$GitHubBase = if ($env:GITHUB_BASE) { $env:GITHUB_BASE } else { "https://github.com" }
$WantComputerUse = $ComputerUse -or ($env:SOLCODE_COMPUTER_USE -eq "1")

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
if ($WantComputerUse) {
    $candidates = @(
        "solcode_${tag}_${os}_${arch}_computeruse.zip",
        "solcode_$($tag.TrimStart('v'))_${os}_${arch}_computeruse.zip"
    ) | Select-Object -Unique
}

Write-Host "Channel/tag: $tag"
Write-Host "Repo:        $Repo"
Write-Host "Target:      $os/$arch"
if ($WantComputerUse) {
    Write-Host "Flavor:      computer-use (CGO + robotgo)"
}

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
        $extra = if ($WantComputerUse) {
@"

The *_computeruse asset is only published for platforms CI can build with CGO
(currently linux/windows/darwin amd64+arm64). Try again without -ComputerUse, or
build locally with: CGO_ENABLED=1 go build -tags computeruse -o solcode.exe ./cmd/solcode
"@
        } else {
@"

Hint: push to master so CI publishes the rolling "master" release,
      or pass -Version <tag> for a versioned release.
"@
        }
        throw @"
Failed to download release asset for $os/$arch (tag $tag, repo $Repo).
Tried: $($candidates -join ', ')
$extra
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
        $parts = @($userPath -split ';' | Where-Object { $_ -ne "" })
        $normalizedInstall = [System.IO.Path]::GetFullPath($InstallDir).TrimEnd('\')
        $already = $false
        foreach ($p in $parts) {
            try {
                if ([System.IO.Path]::GetFullPath($p).TrimEnd('\') -eq $normalizedInstall) {
                    $already = $true
                    break
                }
            } catch {
                if ($p -eq $InstallDir) { $already = $true; break }
            }
        }

        if (-not $already) {
            $newPath = if ($userPath.Trim().TrimEnd(';')) { "$($userPath.TrimEnd(';'));$InstallDir" } else { $InstallDir }
            [Environment]::SetEnvironmentVariable("Path", $newPath, "User")
            Write-Host "Added to user PATH: $InstallDir"
        } else {
            Write-Host "User PATH already includes $InstallDir"
        }

        # Current session
        $sessionParts = @($env:Path -split ';' | Where-Object { $_ -ne "" })
        $inSession = $false
        foreach ($p in $sessionParts) {
            try {
                if ([System.IO.Path]::GetFullPath($p).TrimEnd('\') -eq $normalizedInstall) {
                    $inSession = $true
                    break
                }
            } catch {
                if ($p -eq $InstallDir) { $inSession = $true; break }
            }
        }
        if (-not $inSession) {
            $env:Path = "$InstallDir;$env:Path"
            Write-Host "Updated PATH for current PowerShell session"
        }

        # Notify running apps (Explorer etc.) that User PATH changed.
        try {
            Add-Type -Namespace Win32 -Name Native -MemberDefinition @'
[DllImport("user32.dll", SetLastError=true, CharSet=CharSet.Auto)]
public static extern IntPtr SendMessageTimeout(
    IntPtr hWnd, uint Msg, UIntPtr wParam, string lParam,
    uint fuFlags, uint uTimeout, out UIntPtr lpdwResult);
'@ -ErrorAction SilentlyContinue
            $HWND_BROADCAST = [IntPtr]0xffff
            $WM_SETTINGCHANGE = 0x1a
            $result = [UIntPtr]::Zero
            [void][Win32.Native]::SendMessageTimeout(
                $HWND_BROADCAST, $WM_SETTINGCHANGE, [UIntPtr]::Zero, "Environment",
                2, 5000, [ref]$result)
        } catch {
            # Non-fatal: new terminals still pick up User PATH.
        }

        Write-Host "New terminals will find 'solcode' automatically."
        Write-Host "If this window still cannot, open a new terminal or run:"
        Write-Host "  `$env:Path = '$InstallDir;' + `$env:Path"
    } else {
        Write-Host "Skipped PATH update (-NoPath)."
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
