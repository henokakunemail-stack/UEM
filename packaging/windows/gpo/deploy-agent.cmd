@echo off
rem ==========================================================================
rem  Endpoint Management Agent - GPO Computer Startup Script
rem ==========================================================================
rem  Runs at every boot as NT AUTHORITY\SYSTEM (on the network as DOMAIN\PC$).
rem  It installs and enrolls the agent once, and does nothing afterwards: an
rem  installed agent is kept current by Agent Updates in the console, and
rem  re-running the installer would put the share's (possibly older) binary
rem  back over an agent that has already updated itself.
rem
rem  Guide: docs/installation/agent-gpo.md
rem ==========================================================================
setlocal EnableExtensions

rem ---- Configure these four values -----------------------------------------
rem  SHARE  : folder holding EndpointAgent-Setup.exe. Readable by Domain
rem           Computers only: this file carries the enrollment token.
rem  SERVER : exactly the URL agents already use. A different spelling of the
rem           same server (IP vs. name) makes the agent enroll again.
rem  TOKEN  : a Multi-Device enrollment token from the console.
rem  SITE   : optional site label, no spaces (e.g. Jakarta-HQ).
set "SHARE=\\corp.local\UEM$"
set "SERVER=https://uem.corp.local:8443"
set "TOKEN=PASTE_MULTI_DEVICE_TOKEN_HERE"
set "SITE="
rem --------------------------------------------------------------------------

set "SERVICE=endpoint-agent"
set "DATADIR=%ProgramData%\EndpointAgent"
set "LOG=%DATADIR%\gpo-deploy.log"

rem 1. Installed and enrolled: nothing to do. No side effect before this line.
sc.exe query "%SERVICE%" >nul 2>&1
if not errorlevel 1 if exist "%DATADIR%\creds.json" exit /b 0

if not exist "%DATADIR%" mkdir "%DATADIR%"
call :log "starting deployment on %COMPUTERNAME% (server %SERVER%, site %SITE%)"

rem A TOKEN line that lost its "TOKEN=" leaves the variable undefined, and the
rem installer would then install an agent that is never enrolled. (This check
rem must not itself contain the text of the TOKEN line, or an edit that
rem rewrites that line would rewrite this one too.)
if not defined TOKEN (
    call :log "ERROR: TOKEN is empty - check the TOKEN line at the top of deploy-agent.cmd"
    exit /b 0
)
if "%TOKEN%"=="PASTE_MULTI_DEVICE_TOKEN_HERE" (
    call :log "ERROR: TOKEN is not configured in deploy-agent.cmd"
    exit /b 0
)

rem 2. The installer is copied locally first, so a network blip during a slow
rem    boot cannot cut the install off half way.
if not exist "%SHARE%\EndpointAgent-Setup.exe" (
    call :log "ERROR: %SHARE%\EndpointAgent-Setup.exe is not reachable. Check that Domain Computers can read the share and that 'Always wait for the network at computer startup' is enabled."
    exit /b 0
)
set "LOCAL=%SystemRoot%\Temp\EndpointAgent-Setup-%RANDOM%.exe"
copy /y "%SHARE%\EndpointAgent-Setup.exe" "%LOCAL%" >nul
if errorlevel 1 (
    call :log "ERROR: copying the installer from %SHARE% failed"
    exit /b 0
)

rem 3. Silent install + enrollment. /S must be upper case.
set "SITEARG="
rem    The values are quoted on purpose: the installer splits its options on
rem    every "/", so an unquoted /SERVER=https://host reaches it as "https:".
if defined SITE set "SITEARG=/SITE="%SITE%""
start "" /wait "%LOCAL%" /S /SERVER="%SERVER%" /TOKEN="%TOKEN%" %SITEARG%
set "RC=%errorlevel%"
del /f /q "%LOCAL%" >nul 2>&1

if "%RC%"=="0" (
    call :log "installed and enrolled (exit 0)"
) else if "%RC%"=="2" (
    call :log "ERROR: installer exit 2: SERVER is empty"
) else if "%RC%"=="3" (
    call :log "ERROR: installer exit 3: enrollment refused or server unreachable. Common causes: token_expired, token_used_up, token_revoked, TCP 8443 blocked, or a server certificate this PC does not trust. Will retry at next boot."
) else (
    call :log "ERROR: installer exit %RC%"
)

rem Always 0: a startup script's exit code only clutters the GPO event log, and
rem the next boot retries anyway. The reason is in the log above.
exit /b 0

:log
>>"%LOG%" echo %date% %time% %~1
exit /b 0
