@echo off
REM Live Market Scanner launcher. Double-click (or use the desktop / startup
REM shortcut from "run.bat install") to run; run it again to restart.
REM
REM Runtime model: scanner.exe is a GUI-subsystem app with NO console. It
REM shows a system tray icon (right-click: Open scanner / Charts window / Open
REM log file / Quit), logs to %USERPROFILE%\.marketscanner\logs\scanner.log,
REM opens the native main window (scannerwin.exe --pane main) itself once it is
REM listening, and is single-instance (starting it again while it runs just
REM brings the main window forward and exits). The tray icon owns its lifetime.
REM
REM What this script does: stops any running instance gracefully, REBUILDS the
REM web UI + binaries when the sources changed, then starts the scanner and
REM exits so this console disappears. On first launch, paste your Massive API
REM key in the app's settings panel (it's saved for next time).
REM
REM Usage (first argument, case-insensitive):
REM   run.bat            stop, rebuild if stale, start
REM   run.bat build      same, but ALWAYS rebuild
REM   run.bat stop       gracefully stop the scanner and shell windows, then exit
REM   run.bat logs       live-tail the log file (the old scrolling console)
REM   run.bat install    create the Desktop + Startup shortcuts
REM   run.bat uninstall  remove those shortcuts
cd /d "%~dp0"

if /I "%~1"=="install" goto doinstall
if /I "%~1"=="uninstall" goto douninstall
if /I "%~1"=="stop" goto dostop
if /I "%~1"=="logs" goto dologs

call :stop

REM Freshness check: the build is FRESH when scanner.exe and scannerwin.exe
REM both already exist and nothing under the watched source paths (the web
REM app's sources/config, the Go module, cmd/, internal/, assets/, tools/) is
REM newer than scanner.exe. When fresh, the three build steps below are skipped
REM so a plain re-launch is instant instead of re-running npm + two go builds
REM every time. "run.bat build" (first argument, case-insensitive) always
REM forces a rebuild regardless of file times.
REM
REM One PowerShell one-liner does the whole comparison. It is written with
REM ONLY single quotes for its own strings (never double quotes, and never
REM % or !) so it nests cleanly inside the double-quoted -Command argument
REM below without cmd misreading where that argument ends - the parentheses,
REM braces and the pipe inside are all safely inside that one pair of double
REM quotes as far as cmd's own parser is concerned.
for /f "delims=" %%F in ('powershell -NoProfile -Command "if ((Test-Path 'scanner.exe') -and (Test-Path 'scannerwin.exe')) { $ref=(Get-Item 'scanner.exe').LastWriteTime; $paths='web\src','web\index.html','web\package.json','web\vite.config.ts','cmd','internal','assets','tools','go.mod','go.sum'; $newer=Get-ChildItem -Path $paths -Recurse -File -ErrorAction SilentlyContinue | Where-Object { $_.LastWriteTime -gt $ref }; if ($newer) { 'stale' } else { 'fresh' } } else { 'stale' }"') do set "FRESHNESS=%%F"
if /I "%~1"=="build" set "FRESHNESS=stale"

if /I "%FRESHNESS%"=="fresh" (
  echo Sources unchanged since the last build - starting without rebuilding ^(run.bat build forces a rebuild^).
) else (
  echo Rebuilding: sources changed or binaries missing.
)

if not exist "web\node_modules" (
  echo Installing web dependencies ^(first run only^) ...
  pushd web
  call npm install
  popd
)

REM Only the three build steps themselves are skipped on a fresh tree; the
REM node_modules check above always runs regardless.
if /I "%FRESHNESS%"=="fresh" goto skipbuild

echo Building web UI ...
pushd web
call npm run build || ( popd & goto buildfail )
popd

echo Building scanner.exe ...
REM -H windowsgui: scanner.exe has no console window. Its output goes to the
REM log file (%USERPROFILE%\.marketscanner\logs\scanner.log) and the system
REM tray icon owns its lifetime (Quit from the tray menu, or run.bat stop).
"C:\Program Files\Go\bin\go.exe" build -ldflags="-H windowsgui" -o scanner.exe .\cmd\scanner || goto buildfail

echo Building scannerwin.exe ...
REM -H windowsgui: no console window per pane window.
"C:\Program Files\Go\bin\go.exe" build -ldflags="-H windowsgui" -o scannerwin.exe .\cmd\scannerwin || goto buildfail

:skipbuild
echo Starting Live Market Scanner - look for its icon in the system tray.
REM scanner.exe opens its own main window once it is listening, so there is
REM nothing left to do here: launch it detached and let this console close.
start "" "%~dp0scanner.exe"
exit /b 0

:buildfail
REM The shortcuts run this script minimized, so a build failure would be
REM invisible: pop a message box, then keep the console open for the details.
echo.
echo Build failed. See errors above.
powershell -NoProfile -Command "Add-Type -AssemblyName PresentationFramework; [void][System.Windows.MessageBox]::Show('Live Market Scanner: the build failed. The console window in the taskbar has the details.','Live Market Scanner')"
pause
exit /b 1

:doinstall
powershell -NoProfile -ExecutionPolicy Bypass -File "%~dp0tools\install-shortcuts.ps1"
exit /b %errorlevel%

:douninstall
powershell -NoProfile -ExecutionPolicy Bypass -File "%~dp0tools\install-shortcuts.ps1" -Uninstall
exit /b %errorlevel%

:dostop
call :stop
exit /b 0

:dologs
REM Live tail of the log file (Ctrl+C to stop). Single quotes only, so the
REM path nests inside the double-quoted -Command argument.
powershell -NoProfile -Command "Get-Content -Path (Join-Path $env:USERPROFILE '.marketscanner\logs\scanner.log') -Tail 200 -Wait"
exit /b 0

:stop
echo Stopping any running scanner...
REM Graceful first: taskkill WITHOUT /F sends WM_CLOSE, so the scanner shuts
REM down cleanly (flushes, closes its shell windows). Only force-kill what is
REM still alive after ~8 s. The rebuild needs both exes unlocked, so scannerwin
REM (the shell windows) gets the same treatment.
call :stopone scanner.exe
call :stopone scannerwin.exe
exit /b 0

:stopone
REM %~1 = image name. Ask it to close, poll up to ~8 s, then force-kill.
taskkill /IM %~1 >nul 2>&1
for /L %%i in (1,1,8) do (
  tasklist /FI "IMAGENAME eq %~1" 2>nul | find /I "%~1" >nul || exit /b 0
  ping -n 2 127.0.0.1 >nul
)
taskkill /F /IM %~1 >nul 2>&1
exit /b 0
