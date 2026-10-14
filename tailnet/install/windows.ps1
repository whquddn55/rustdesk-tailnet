# Install or update rustdesk-tailnet on Windows x64. In PowerShell:
#   irm https://raw.githubusercontent.com/whquddn55/rustdesk-tailnet/tailnet/tailnet/install/windows.ps1 | iex
# Files downloaded by PowerShell carry no Mark of the Web, so SmartScreen does not stop
# the unsigned installer.
$ErrorActionPreference = 'Stop'
$repo = 'whquddn55/rustdesk-tailnet'
$rel = Invoke-RestMethod "https://api.github.com/repos/$repo/releases?per_page=30" |
    Where-Object { $_.tag_name -match '^tailnet-b\d+$' -and -not $_.draft } | Select-Object -First 1
if (-not $rel) { throw 'no tailnet-b release found' }
$asset = $rel.assets | Where-Object { $_.name -like '*x86_64.exe' } | Select-Object -First 1
$file = Join-Path $env:TEMP $asset.name
Write-Host "rustdesk-tailnet $($rel.tag_name)"
Invoke-WebRequest $asset.browser_download_url -OutFile $file -UseBasicParsing
Start-Process -FilePath $file -ArgumentList '--silent-install' -Verb RunAs -Wait
Write-Host "installed rustdesk-tailnet $($rel.tag_name)"
