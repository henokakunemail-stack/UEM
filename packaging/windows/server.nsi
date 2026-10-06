; ==============================================================================
; Enterprise Endpoint Management Server - NSIS Modern UI Installer
; ==============================================================================
;
; Paired with the agent installer in the same folder. The two install different
; programs under different service names, so both may be present on one machine
; (which is also how the test fleet is laid out).
;
; Runtime configuration lives in an env file rather than in the registry
; because the Service Control Manager launches a service with no environment
; block: anything set in the operator's shell is simply not there when the SCM
; starts the process. The file is the only channel that survives a reboot.

!include "MUI2.nsh"
!include "LogicLib.nsh"
!include "nsDialogs.nsh"
!include "FileFunc.nsh"

; --- General Settings ---
Name "Endpoint Management Server"
OutFile "EndpointServer-Setup.exe"
Unicode True
RequestExecutionLevel admin

; SERVICE_KEY and UNINSTALL_KEY are preprocessor constants, so they are written
; ${NAME}. The two data paths cannot be: NSIS variables are resolved at run time
; and $COMMONAPPDATA is only known then, so they are StrCpy'd into Vars in .onInit.
!define SERVER_NAME "endpoint-mgmt-server"
!define SERVICE_KEY "Software\EndpointMgmtServer"
!define UNINSTALL_KEY "Software\Microsoft\Windows\CurrentVersion\Uninstall\EndpointMgmtServer"

InstallDir "$PROGRAMFILES64\EndpointMgmtServer"
InstallDirRegKey HKLM "${SERVICE_KEY}" "InstallDir"

; Same VERSION plumbing as agent.nsi: /DVERSION and /DVERSION_NUM come from
; build-server.ps1, which defaults both from the repository's VERSION file.
!ifndef VERSION
  !define VERSION "1.0.0"
!endif
!ifndef VERSION_NUM
  !define VERSION_NUM "1.0.0.0"
!endif
VIProductVersion "${VERSION_NUM}"
VIAddVersionKey "ProductName" "Enterprise Endpoint Management Server"
VIAddVersionKey "CompanyName" "Enterprise Management"
VIAddVersionKey "LegalCopyright" "MIT License"
VIAddVersionKey "FileDescription" "Enterprise Endpoint Management Server Installer"
VIAddVersionKey "FileVersion" "${VERSION_NUM}"
VIAddVersionKey "ProductVersion" "${VERSION}"

Var DATADIR
Var ENVFILE
Var Dialog
Var TextServer
Var TextAddr
Var TextPassword
Var BatchH
Var BatchLineText

; --- Interface Settings ---
!define MUI_ABORTWARNING
!define MUI_FINISHPAGE_RUN "$INSTDIR\endpoint-server.exe"
!define MUI_FINISHPAGE_RUN_TEXT "Start the management console now"
!define MUI_ICON "${NSISDIR}\Contrib\Graphics\Icons\modern-install.ico"
!define MUI_UNICON "${NSISDIR}\Contrib\Graphics\Icons\modern-uninstall.ico"

!insertmacro MUI_PAGE_WELCOME
Page custom ConfigPage ConfigPageLeave
!insertmacro MUI_PAGE_DIRECTORY
!insertmacro MUI_PAGE_INSTFILES
!insertmacro MUI_PAGE_FINISH

!insertmacro MUI_UNPAGE_CONFIRM
!insertmacro MUI_UNPAGE_INSTFILES

!insertmacro MUI_LANGUAGE "English"

; ------------------------------------------------------------------------------
; Resolve the runtime paths once. The uninstaller runs without ever visiting
; the install section, so a path computed only there would be empty exactly when
; it is needed to delete the secret file.
; ------------------------------------------------------------------------------
; Set once so every section and the uninstaller see the same paths. The service
; runs as LocalSystem, so this has to be the machine-wide ProgramData rather than
; a per-user profile: $APPDATA under shell context "all" resolves to
; C:\ProgramData, which is also what $COMMONAPPDATA names.
!macro ResolvePaths
    SetShellVarContext all
    StrCpy $DATADIR $APPDATA
    StrCpy $DATADIR "$DATADIR\EndpointMgmtServer"
    StrCpy $ENVFILE "$DATADIR\server.env"
!macroend

; The uninstaller has to find the config file the installer actually wrote, and
; the installer records where that was. Falling back to the default location is
; not a formality: an operator who chose a different Program Files directory
; still has the same ProgramData path, but one who once ran the installer on a
; machine where the data had been moved would otherwise be left with a readable
; copy of the signing secret on disk.
!macro ResolveUninstallPaths
    ReadRegStr $1 HKLM "${SERVICE_KEY}" "EnvFile"
    ${If} $1 != ""
        StrCpy $ENVFILE $1
        ${GetParent} "$ENVFILE" $DATADIR
    ${Else}
        !insertmacro ResolvePaths
    ${EndIf}
    ; The deferred cleanup pass runs as a copy in the temp directory, so $INSTDIR
    ; describes that copy rather than the install tree. The recorded location is
    ; the only thing that still says where the tree actually is.
    ReadRegStr $1 HKLM "${SERVICE_KEY}" "InstallDir"
    ${If} $1 != ""
        StrCpy $INSTDIR $1
    ${EndIf}
!macroend

; ------------------------------------------------------------------------------
; un.BatLine -- append one line to the deferred-cleanup batch file.
;
; One FileWrite per line, with the line break supplied by the FileWrite line
; itself rather than embedded in the text. NSIS splits a command line on an
; unquoted ">" and hands FileWrite the extra pieces as arguments, so a redirect
; written into a literal has to sit inside the quoted path portion to survive --
; which is why every batch line below keeps its whole body in one pair of single
; quotes. And FileWrite appends no newline of its own, so omitting the break
; would collapse the entire file onto one line.
;
; The un. prefix is required: NSIS resolves uninstaller functions by name and
; refuses a Call to a bare function name from inside a Section.
; ------------------------------------------------------------------------------
Function un.BatLine
    FileWrite $BatchH "$BatchLineText$\n"
FunctionEnd

; ------------------------------------------------------------------------------
; Config page
; ------------------------------------------------------------------------------
Function ConfigPage
    !insertmacro MUI_HEADER_TEXT "Server Configuration" \
        "The management console and the agent gateway will both listen on this address."

    nsDialogs::Create 1018
    Pop $Dialog
    ${If} $Dialog == error
        Abort
    ${EndIf}

    ${NSD_CreateLabel} 0 0 100% 12u "Listen address (host:port):"
    Pop $0
    ${NSD_CreateText} 0 14u 100% 12u ":8443"
    Pop $TextAddr

    ${NSD_CreateLabel} 0 32u 100% 12u "Public URL agents connect to (used by the console):"
    Pop $0
    ${NSD_CreateText} 0 46u 100% 12u "http://localhost:8443"
    Pop $TextServer

    ; The admin password is written into the env file the service reads, so the
    ; page must not claim it is optional: bootstrapAdmin only reads it when
    ; there is no admin user yet, and on a fresh install there always is one
    ; missing.
    ${NSD_CreateLabel} 0 64u 100% 12u "Initial admin password (create-admin):"
    Pop $0
    ${NSD_CreatePassword} 0 78u 100% 12u ""
    Pop $TextPassword

    ${NSD_CreateLabel} 0 96u 100% 24u \
        "The signing secret is generated on the server's first start; the password is stored in the server's own config file. Change it after first login."
    Pop $0

    nsDialogs::Show
FunctionEnd

Function ConfigPageLeave
    ${NSD_GetText} $TextAddr $0
    ${If} $0 == ""
        MessageBox MB_ICONEXCLAMATION|MB_OK "A listen address is required."
        Abort
    ${EndIf}
FunctionEnd

; ------------------------------------------------------------------------------
; Install
; ------------------------------------------------------------------------------
Section "Install Server" SecInstall
    !insertmacro ResolvePaths
    SetOutPath "$INSTDIR"
    File "/oname=endpoint-server.exe" "..\..\server-windows-amd64.exe"

    CreateDirectory "$DATADIR"
    CreateDirectory "$INSTDIR\data"

    ; JWT_SECRET is written empty on purpose. The installer cannot invent one
    ; without shipping a CScript helper that does not exist on the Linux build
    ; of this same product, and a secret baked into a public installer is a
    ; published secret. The server fills the value in on first start and writes
    ; it back, so later restarts keep the same tokens valid.
    ${NSD_GetText} $TextAddr $0
    ${NSD_GetText} $TextServer $1
    ${NSD_GetText} $TextPassword $2

    ; NSIS FileWrite appends the text and nothing else -- it does NOT add a
    ; line break. Written bare, the whole file collapses onto one line, the
    ; loader sees it as a single comment, and every setting is silently
    ; discarded: the server then starts on its built-in defaults with its
    ; database in the service's working directory. The trailing `$\n` is what
    ; makes each line a line.
    FileOpen $3 "$ENVFILE" w
    FileWrite $3 "# Runtime configuration for ${SERVER_NAME}.$\n"
    FileWrite $3 "# Written by EndpointServer-Setup.exe. Edit and restart the service to change.$\n"
    FileWrite $3 "# An empty JWT_SECRET is filled in by the server on first start.$\n"
    FileWrite $3 "HTTP_ADDR=$0$\n"
    FileWrite $3 "DB_PATH=$INSTDIR\data\server.db$\n"
    FileWrite $3 "BACKUP_DIR=$INSTDIR\data\backups$\n"
    FileWrite $3 "LOG_FILE=$DATADIR\server.log$\n"
    FileWrite $3 "LOG_LEVEL=info$\n"
    FileWrite $3 "ALLOWED_ORIGIN_DOMAINS=$\n"
    FileWrite $3 "JWT_SECRET=$\n"
    ${If} $2 != ""
        FileWrite $3 "ADMIN_PASSWORD=$2$\n"
    ${EndIf}
    FileClose $3

    DetailPrint "Registering Windows service '${SERVER_NAME}'..."
    nsExec::ExecToLog '"$INSTDIR\endpoint-server.exe" -env-file "$ENVFILE" -service install'
    Pop $0
    ${If} $0 != 0
        MessageBox MB_ICONEXCLAMATION|MB_OK \
            "The service could not be registered (exit $0). The files are installed; run: endpoint-server.exe -service install as administrator."
        Abort
    ${EndIf}

    DetailPrint "Starting Windows service '${SERVER_NAME}'..."
    nsExec::ExecToLog '"$INSTDIR\endpoint-server.exe" -service start'
    Pop $0
    ${If} $0 != 0
        MessageBox MB_ICONEXCLAMATION|MB_OK \
            "The service was installed but did not start (exit $0). Check $ENVFILE."
    ${EndIf}

    WriteRegStr HKLM "${SERVICE_KEY}" "InstallDir" "$INSTDIR"
    WriteRegStr HKLM "${SERVICE_KEY}" "EnvFile" "$ENVFILE"

    WriteRegStr HKLM "${UNINSTALL_KEY}" "DisplayName" "Endpoint Management Server"
    WriteRegStr HKLM "${UNINSTALL_KEY}" "UninstallString" '"$INSTDIR\uninstall.exe"'
    WriteRegStr HKLM "${UNINSTALL_KEY}" "DisplayIcon" '"$INSTDIR\endpoint-server.exe"'
    WriteRegStr HKLM "${UNINSTALL_KEY}" "DisplayVersion" "${VERSION}"
    WriteRegStr HKLM "${UNINSTALL_KEY}" "Publisher" "Enterprise Endpoint Management"
    WriteRegDWORD HKLM "${UNINSTALL_KEY}" "NoModify" 1
    WriteRegDWORD HKLM "${UNINSTALL_KEY}" "NoRepair" 1

    WriteUninstaller "$INSTDIR\uninstall.exe"
    DetailPrint "Endpoint Management Server installed."
SectionEnd

; ------------------------------------------------------------------------------
; Uninstall
; ------------------------------------------------------------------------------
Section "Uninstall"
    !insertmacro ResolveUninstallPaths

    ; Move the current directory out of the install tree before anything is
    ; deleted. A process cannot remove the directory it is standing in: Windows
    ; refuses the delete of the tree's own root, so the recursive delete in the
    ; cleanup batch empties $INSTDIR and then leaves the empty directory behind,
    ; forever, with no error to explain it. This is invisible in the log because
    ; every deletion is redirected to nul.
    SetOutPath "$TEMP"

    DetailPrint "Stopping service..."
    ; sc.exe stop returns when the stop request is accepted, not when the process
    ; is gone, and the server unwinds through a graceful HTTP shutdown first. The
    ; batch below is what actually waits, by retrying the delete until the locks
    ; clear, so a short sleep here is only there to avoid deleting the binary out
    ; from under a process that is still opening it.
    nsExec::ExecToLog '"$INSTDIR\endpoint-server.exe" -service stop'
    Pop $0
    Sleep 2000

    DetailPrint "Removing service registration..."
    nsExec::ExecToLog '"$INSTDIR\endpoint-server.exe" -service uninstall'
    Pop $0
    Sleep 1000

    ; The env file holds the signing secret and admin password: delete first
    Delete "$ENVFILE"
    Delete "$DATADIR\server.log"
    RMDir /r "$DATADIR"

    ; $INSTDIR is read from SERVICE_KEY lazily, so deleting that key empties it --
    ; and an empty path handed to rd is a drive root, which is not a thing anyone
    ; should be running a recursive delete against. Every path this section
    ; still needs is therefore copied into registers BEFORE the keys go away.
    StrCpy $0 "$INSTDIR"
    StrCpy $1 "$DATADIR"
    DeleteRegKey HKLM "${UNINSTALL_KEY}"
    DeleteRegKey HKLM "${SERVICE_KEY}"

    ; These are unlocked by now, so taking them here keeps the batch's work to
    ; whatever the graceful shutdown was still holding. uninstall.exe is absent
    ; on purpose: it is this process.
    Delete "$0\endpoint-server.exe"
    RMDir /r "$0\data"

    ; The install tree cannot be removed from here. Everything under it is still
    ; held open at this point: the server process is mid-way through a graceful
    ; shutdown with its database and log open, and sc.exe returned as soon as the
    ; stop request was accepted rather than when the process actually exited. A
    ; recursive delete attempted now fails with "Access is denied" and is gone,
    ; leaving the whole tree behind. So a small batch file is left in TEMP to
    ; retry the delete until the locks actually clear.
    ;
    ; rd is retried rather than del: the per-file delete succeeds for unlocked
    ; files and there is no way to tell that the locked ones are the ones that
    ; matter. The test is on the directories, not on the files.
    ;
    ; The retry loop is bounded. An unbounded one is a process that never exits
    ; if a lock is ever held by something else, and a batch file spinning in the
    ; background forever is a worse leftover than the directory it was cleaning.
    ; Sixty retries at one second is a minute, which is already far past the
    ; point this process needs to have exited.
    FileOpen $BatchH "$TEMP\em-cleanup.bat" w

    StrCpy $BatchLineText "@echo off"
    Call un.BatLine
    StrCpy $BatchLineText "setlocal enabledelayedexpansion"
    Call un.BatLine
    StrCpy $BatchLineText "set /a n=0"
    Call un.BatLine
    StrCpy $BatchLineText ":try"
    Call un.BatLine
    StrCpy $BatchLineText 'rd /s /q "$0" >nul 2>nul'
    Call un.BatLine
    StrCpy $BatchLineText 'if not exist "$0" goto data'
    Call un.BatLine
    StrCpy $BatchLineText "set /a n+=1"
    Call un.BatLine
    StrCpy $BatchLineText "if !n! geq 60 goto giveup"
    Call un.BatLine
    StrCpy $BatchLineText "ping -n 2 127.0.0.1 >nul"
    Call un.BatLine
    StrCpy $BatchLineText "goto try"
    Call un.BatLine
    StrCpy $BatchLineText ":giveup"
    Call un.BatLine
    StrCpy $BatchLineText ":data"
    Call un.BatLine
    StrCpy $BatchLineText 'rd /s /q "$1" >nul 2>nul'
    Call un.BatLine
    StrCpy $BatchLineText 'del /f /q "%~f0" >nul 2>nul'
    Call un.BatLine
    FileClose $BatchH

    ; SW_HIDE, and cmd rather than the batch itself: this is the whole reason the
    ; uninstall no longer flashes a second NSIS window at the operator.
    ExecShell "" "$SYSDIR\cmd.exe" '/c ""$TEMP\em-cleanup.bat""' SW_HIDE

    DetailPrint "Endpoint Management Server removed."
SectionEnd

; Keep the agent installer and this one from colliding when both are run from
; the same folder during a build.
