<#
Creates (or with -Uninstall removes) the "Live Market Scanner" shortcuts on the
Desktop and in the Startup folder (start with Windows). Both run run.bat
minimized, so its console only shows in the taskbar while it rebuilds. Safe to
re-run: existing shortcuts are overwritten.

  run.bat install      create / refresh the shortcuts
  run.bat uninstall    remove them
#>
param([switch]$Uninstall)

Set-StrictMode -Version Latest
$ErrorActionPreference = 'Stop'

$root = Split-Path -Parent $PSScriptRoot
$name = 'Live Market Scanner.lnk'
$links = @(
    (Join-Path ([Environment]::GetFolderPath('Desktop')) $name),
    (Join-Path ([Environment]::GetFolderPath('Startup')) $name)
)

if ($Uninstall) {
    foreach ($link in $links) {
        if (Test-Path -LiteralPath $link) {
            Remove-Item -LiteralPath $link -Force
            Write-Host "Removed:  $link"
        } else {
            Write-Host "Not found (nothing to remove): $link"
        }
    }
    return
}

$icon = Join-Path $root 'assets\scanner.ico'
if (Test-Path -LiteralPath $icon) {
    $iconLocation = "$icon,0"
} else {
    $iconLocation = "$env:SystemRoot\System32\shell32.dll,12"
    Write-Host "Note: $icon does not exist yet - using a stock icon. Re-run 'run.bat install' after the first build to pick up the real one."
}

$shell = New-Object -ComObject WScript.Shell
foreach ($link in $links) {
    $sc = $shell.CreateShortcut($link)
    $sc.TargetPath = Join-Path $root 'run.bat'
    $sc.WorkingDirectory = $root
    $sc.WindowStyle = 7   # minimized
    $sc.IconLocation = $iconLocation
    $sc.Description = 'Live Market Scanner'
    $sc.Save()
    Write-Host "Created:  $link"
}
