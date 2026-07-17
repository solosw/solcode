# PostToolUse — hard-trim oversized tool results
# command:
#   powershell -NoProfile -ExecutionPolicy Bypass -File examples/hooks/powershell/post-tool-trim.ps1

$ErrorActionPreference = 'Stop'
$maxChars = 12000
if ($env:SOLCODE_HOOK_TRIM_CHARS) {
    $maxChars = [int]$env:SOLCODE_HOOK_TRIM_CHARS
}

$raw = [Console]::In.ReadToEnd()
if ([string]::IsNullOrWhiteSpace($raw)) {
    Write-Output '{"decision":"allow"}'
    exit 0
}

$event = $raw | ConvertFrom-Json
$block = $event.tool_result
if ($null -eq $block -or $block.type -eq 'image' -or $block.is_error) {
    Write-Output '{"decision":"allow"}'
    exit 0
}

$text = ''
if ($null -ne $block.text) { $text = [string]$block.text }
if ($text.Length -le $maxChars) {
    Write-Output '{"decision":"allow"}'
    exit 0
}

$head = [int][Math]::Floor($maxChars * 0.7)
$tail = [Math]::Max($maxChars - $head - 80, 0)
$omitted = $text.Length - $maxChars
$trimmed = $text.Substring(0, $head) +
    "`n`n... [post-tool-trim: omitted $omitted chars] ...`n`n"
if ($tail -gt 0) {
    $trimmed += $text.Substring($text.Length - $tail)
}

$result = @{
    decision = 'modify'
    message  = "trimmed tool result $($text.Length)→$($trimmed.Length) chars"
    modified_result = @{
        type     = 'text'
        text     = $trimmed
        is_error = [bool]$block.is_error
    }
}
Write-Output ($result | ConvertTo-Json -Compress -Depth 6)
