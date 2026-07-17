# UserPromptSubmit — prefix prompts
# command:
#   powershell -NoProfile -ExecutionPolicy Bypass -File examples/hooks/powershell/user-prompt-prefix.ps1

$ErrorActionPreference = 'Stop'
$prefix = $env:SOLCODE_PROMPT_PREFIX
if ([string]::IsNullOrEmpty($prefix)) {
    $prefix = "[project note: prefer small diffs; run tests after edits]`n`n"
}

$raw = [Console]::In.ReadToEnd()
if ([string]::IsNullOrWhiteSpace($raw)) {
    Write-Output '{"decision":"allow"}'
    exit 0
}

$event = $raw | ConvertFrom-Json
$prompt = [string]($event.prompt)
if ([string]::IsNullOrEmpty($prompt) -or $prompt.StartsWith($prefix.Trim())) {
    Write-Output '{"decision":"allow"}'
    exit 0
}

$result = @{
    decision        = 'modify'
    modified_prompt = $prefix + $prompt
    message         = 'prefixed user prompt'
}
Write-Output ($result | ConvertTo-Json -Compress)
