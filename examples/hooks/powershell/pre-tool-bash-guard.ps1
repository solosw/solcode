# PreToolUse — Bash safety guard (PowerShell)
# settings command (Windows):
#   powershell -NoProfile -ExecutionPolicy Bypass -File examples/hooks/powershell/pre-tool-bash-guard.ps1

$ErrorActionPreference = 'Stop'
$raw = [Console]::In.ReadToEnd()
if ([string]::IsNullOrWhiteSpace($raw)) {
    Write-Output '{"decision":"allow"}'
    exit 0
}

$event = $raw | ConvertFrom-Json
$command = ''
if ($null -ne $event.tool_input -and $null -ne $event.tool_input.command) {
    $command = [string]$event.tool_input.command
}

$deny = @(
    'curl',
    'wget',
    'Invoke-WebRequest',
    'rm\s+(-[a-zA-Z]*f[a-zA-Z]*\s+)?/',
    'git\s+push\s+.*--force',
    'drop\s+database',
    'format\s+[a-z]:'
)

foreach ($pat in $deny) {
    if ($command -match $pat) {
        $msg = "Bash command blocked by pre-tool-bash-guard: $command"
        $out = @{ decision = 'block'; message = $msg } | ConvertTo-Json -Compress
        Write-Output $out
        exit 0
    }
}

Write-Output '{"decision":"allow"}'
