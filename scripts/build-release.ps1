# Build multi-platform release archives for GitHub Releases (Windows host).
# Requires Python, Git, Rust >=1.98 and target stdlibs/C linkers for ALL targets.
# Prefer native per-platform scripts/build-computeruse.sh builds (as CI does).
#
# Usage:
#   .\scripts\build-release.ps1 -Version v0.1.0
#
# Output under .\dist matching install.sh / install.ps1 asset names.

[CmdletBinding()]
param(
    [Parameter(Mandatory = $true)]
    [string]$Version
)

$ErrorActionPreference = "Stop"
$Root = Split-Path -Parent (Split-Path -Parent $MyInvocation.MyCommand.Path)
Set-Location $Root

$Dist = Join-Path $Root "dist"
if (Test-Path $Dist) { Remove-Item -Recurse -Force $Dist }
New-Item -ItemType Directory -Path $Dist | Out-Null

$env:CGO_ENABLED = "0"
$binVersion = if ($env:LDFLAGS_VERSION) { $env:LDFLAGS_VERSION } else { $Version }
$ldflags = "-s -w -X main.version=$binVersion"

$targets = @(
    @{ GOOS = "linux";   GOARCH = "amd64"; Pack = "tar.gz" },
    @{ GOOS = "linux";   GOARCH = "arm64"; Pack = "tar.gz" },
    @{ GOOS = "darwin";  GOARCH = "amd64"; Pack = "tar.gz" },
    @{ GOOS = "darwin";  GOARCH = "arm64"; Pack = "tar.gz" },
    @{ GOOS = "windows"; GOARCH = "amd64"; Pack = "zip" },
    @{ GOOS = "windows"; GOARCH = "arm64"; Pack = "zip" }
)

$Python = if (Get-Command python3 -ErrorAction SilentlyContinue) { "python3" } else { "python" }
Write-Host "Building solcode $Version ..."
try {
    foreach ($t in $targets) {
        $outName = if ($t.GOOS -eq "windows") { "solcode.exe" } else { "solcode" }
        $stage = Join-Path $Dist ("stage_{0}_{1}" -f $t.GOOS, $t.GOARCH)
        New-Item -ItemType Directory -Path $stage | Out-Null
        $binPath = Join-Path $stage $outName
        $artifact = Join-Path $Dist ("solcode_{0}_{1}_{2}.{3}" -f $Version, $t.GOOS, $t.GOARCH, $t.Pack)

        Write-Host ("  -> {0}/{1}" -f $t.GOOS, $t.GOARCH)
        & $Python scripts/prepare-computer-use.py --os $t.GOOS --arch $t.GOARCH
        if ($LASTEXITCODE -ne 0) { throw "ComputerUse preparation failed" }
        & $Python scripts/prepare-codegraph.py --os $t.GOOS --arch $t.GOARCH
        if ($LASTEXITCODE -ne 0) { throw "CodeGraph preparation failed" }
        Copy-Item -Recurse internal/computeruse/bundle/notices (Join-Path $stage "open-computer-use-notices")
        Copy-Item -Recurse internal/codegraphruntime/bundle/notices (Join-Path $stage "codegraph-notices")
        $env:GOOS = $t.GOOS
        $env:GOARCH = $t.GOARCH
        go build -trimpath -ldflags $ldflags -o $binPath ./cmd/solcode
        if ($LASTEXITCODE -ne 0) { throw "go build failed for $($t.GOOS)/$($t.GOARCH)" }

        if ($t.Pack -eq "zip") {
            Compress-Archive -Path (Join-Path $stage "*") -DestinationPath $artifact -Force
        } else {
            # tar is available on Windows 10+
            Push-Location $stage
            try {
                tar -czf $artifact $outName open-computer-use-notices codegraph-notices
                if ($LASTEXITCODE -ne 0) { throw "tar failed for $artifact" }
            } finally {
                Pop-Location
            }
        }
        Remove-Item -Recurse -Force $stage
    }
} finally {
    Remove-Item Env:GOOS -ErrorAction SilentlyContinue
    Remove-Item Env:GOARCH -ErrorAction SilentlyContinue
    foreach ($runtime in @("computeruse", "codegraphruntime")) {
        Remove-Item "internal/$runtime/bundle/assets/runtime.archive", "internal/$runtime/bundle/assets/target.json" -ErrorAction SilentlyContinue
    }
}

# checksums
$sums = Join-Path $Dist "checksums.txt"
Get-ChildItem $Dist -File | Where-Object { $_.Name -like "solcode_*" } | ForEach-Object {
    $hash = (Get-FileHash -Algorithm SHA256 $_.FullName).Hash.ToLower()
    "{0}  {1}" -f $hash, $_.Name
} | Set-Content -Path $sums -Encoding utf8

Write-Host ""
Write-Host "Artifacts in $Dist :"
Get-ChildItem $Dist | Format-Table Name, Length
Write-Host "Upload these files to a GitHub Release tagged $Version."
