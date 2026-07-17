$ErrorActionPreference = 'Stop'

$skillDir = Split-Path -Parent $PSScriptRoot
$rootDir = Split-Path -Parent $skillDir
$skillFile = Join-Path $skillDir 'SKILL.md'
$legacyFile = Join-Path $rootDir 'review.md'
$requiredDirs = @('scripts', 'references', 'assets')

$failures = New-Object System.Collections.Generic.List[string]

if (-not (Test-Path -LiteralPath $skillDir -PathType Container)) {
  $failures.Add("Missing skill directory: $skillDir")
}

if (-not (Test-Path -LiteralPath $skillFile -PathType Leaf)) {
  $failures.Add("Missing skill file: $skillFile")
} else {
  $content = Get-Content -LiteralPath $skillFile -Raw
  if ([string]::IsNullOrWhiteSpace($content)) {
    $failures.Add("Skill file is empty: $skillFile")
  }
  if ($content -notmatch '(?m)^#\s+Review Skill\s*$') {
    $failures.Add("Skill file is missing the expected title: $skillFile")
  }
}

if (Test-Path -LiteralPath $legacyFile -PathType Leaf) {
  $failures.Add("Legacy skill file should be removed after migration: $legacyFile")
}

foreach ($name in $requiredDirs) {
  $path = Join-Path $skillDir $name
  if (-not (Test-Path -LiteralPath $path -PathType Container)) {
    $failures.Add("Missing required directory: $path")
  }
}

if ($failures.Count -gt 0) {
  $failures | ForEach-Object { Write-Error $_ }
  exit 1
}

Write-Output "Skill verified: $skillDir"
