; ==============================================================================
; Enterprise Endpoint Management Agent - NSIS Modern UI Installer
; ==============================================================================

!include "MUI2.nsh"
!include "LogicLib.nsh"
!include "nsDialogs.nsh"
!include "FileFunc.nsh"

; --- General Settings ---
Name "Endpoint Management Agent"
OutFile "EndpointAgent-Setup.exe"
Unicode True
RequestExecutionLevel admin

!define SERVICE_NAME "endpoint-agent"
!define SERVICE_KEY "Software\EndpointAgent"
!define UNINSTALL_KEY "Software\Microsoft\Windows\CurrentVersion\Uninstall\EndpointAgent"

InstallDir "$PROGRAMFILES64\EndpointAgent"
InstallDirRegKey HKLM "${SERVICE_KEY}" "InstallDir"

; --- Version Information ---
!ifndef VERSION
  !define VERSION "1.4.0"
!endif
!ifndef VERSION_NUM
  !define VERSION_NUM "1.4.0.0"
!endif
VIProductVersion "${VERSION_NUM}"
VIAddVersionKey "ProductName" "Endpoint Management Agent"
VIAddVersionKey "CompanyName" "Enterprise Management"
VIAddVersionKey "LegalCopyright" "MIT License"
VIAddVersionKey "FileDescription" "Enterprise Endpoint Management Agent Installer"
VIAddVersionKey "FileVersion" "${VERSION_NUM}"
VIAddVersionKey "ProductVersion" "${VERSION}"

; --- Variables ---
Var Dialog
Var LabelServer
Var TextServer
Var ServerURL
Var LabelToken
Var TextToken
Var EnrollToken
Var DATADIR
Var UninstallToken
Var UninstDialog
Var TextUninstToken

; --- Interface Settings ---
!define MUI_ABORTWARNING
!define MUI_ICON "${NSISDIR}\Contrib\Graphics\Icons\modern-install.ico"
!define MUI_UNICON "${NSISDIR}\Contrib\Graphics\Icons\modern-uninstall.ico"

; --- Installer Pages ---
!insertmacro MUI_PAGE_WELCOME
Page custom ServerConfigPage ServerConfigPageLeave
!insertmacro MUI_PAGE_DIRECTORY
!insertmacro MUI_PAGE_INSTFILES
!insertmacro MUI_PAGE_FINISH

; --- Uninstaller Pages ---
!insertmacro MUI_UNPAGE_CONFIRM
UninstPage custom un.ConfirmTokenPage un.ConfirmTokenPageLeave
!insertmacro MUI_UNPAGE_INSTFILES
!insertmacro MUI_UNPAGE_FINISH

; --- Languages ---
!insertmacro MUI_LANGUAGE "English"

; ------------------------------------------------------------------------------
; Path Resolution Macro
; ------------------------------------------------------------------------------
!macro ResolvePaths
    SetShellVarContext all
    StrCpy $DATADIR "$APPDATA\EndpointAgent"
!macroend

; --- Custom Page: Server & Enrollment Configuration ---
Function ServerConfigPage
    !insertmacro MUI_HEADER_TEXT "Server Configuration" "Enter central management server URL and optional enrollment token."
    nsDialogs::Create 1018
    Pop $Dialog
    ${If} $Dialog == error
        Abort
    ${EndIf}

    ; Server URL Label and Textbox
    ${NSD_CreateLabel} 0 0 100% 12u "Management Server URL (HTTPS/WSS):"
    Pop $LabelServer

    ${NSD_CreateText} 0 14u 100% 12u "http://localhost:8443"
    Pop $TextServer

    ; Enrollment Token Label and Textbox
    ${NSD_CreateLabel} 0 34u 100% 12u "Enrollment Token (Generate from Web Console -> Devices):"
    Pop $LabelToken

    ${NSD_CreateText} 0 48u 100% 12u ""
    Pop $TextToken

    ${NSD_CreateLabel} 0 70u 100% 24u "Note: If enrollment token is left empty, the agent service will be installed with Administrator (LocalSystem) rights and will enroll once a token is supplied."
    Pop $LabelToken

    nsDialogs::Show
FunctionEnd

Function ServerConfigPageLeave
    ${NSD_GetText} $TextServer $ServerURL
    ${NSD_GetText} $TextToken $EnrollToken

    ${If} $ServerURL == ""
        MessageBox MB_ICONEXCLAMATION|MB_OK "Server URL is required to configure agent communication."
        Abort
    ${EndIf}

    ; Normalize URL: auto-prepend https:// if scheme is missing
    StrCpy $0 $ServerURL 7
    StrCpy $1 $ServerURL 8
    ${If} $0 != "http://"
    ${AndIf} $1 != "https://"
        StrCpy $ServerURL "https://$ServerURL"
    ${EndIf}
FunctionEnd

; --- Installer Section ---
Section "Install Agent" SecInstall
    !insertmacro ResolvePaths

    ; Stop existing service and terminate lingering processes to avoid file-in-use locks
    DetailPrint "Stopping existing '${SERVICE_NAME}' service if running..."
    nsExec::ExecToLog 'net.exe stop "${SERVICE_NAME}"'
    Pop $0
    nsExec::Exec 'cmd.exe /c "taskkill.exe /F /IM endpoint-agent.exe /T 2>nul"'
    Pop $0
    Sleep 1000

    SetOutPath "$INSTDIR"

    ; 1. Copy agent binary
    File "/oname=endpoint-agent.exe" "..\..\agent-windows-amd64.exe"

    ; 2. Ensure system-wide ProgramData directory exists for credentials
    CreateDirectory "$DATADIR"

    ; 3. Run Enrollment if token provided
    ${If} $EnrollToken != ""
        DetailPrint "Enrolling device with central server..."
        nsExec::ExecToLog '"$INSTDIR\endpoint-agent.exe" -server "$ServerURL" -enroll "$EnrollToken" -enroll-only -creds "$DATADIR\creds.json"'
        Pop $0
        ${If} $0 != 0
            ${IfNot} ${FileExists} "$DATADIR\creds.json"
                DetailPrint "ERROR: Enrollment failed with exit code $0."
                MessageBox MB_ICONSTOP|MB_OK "Device enrollment failed (exit code $0).$\r$\n$\r$\nPastikan:$\r$\n1. Server URL ($ServerURL) dapat diakses.$\r$\n2. Enrollment Token valid dan belum kadaluarsa.$\r$\n3. Koneksi internet/jaringan aktif.$\r$\n$\r$\nInstalasi dibatalkan untuk mencegah service error."
                Abort
            ${EndIf}
        ${Else}
            DetailPrint "Device enrolled successfully."
        ${EndIf}
    ${EndIf}

    ; 4. Register Windows Service under Service Control Manager (runs as LocalSystem)
    DetailPrint "Registering Windows Service '${SERVICE_NAME}' as LocalSystem..."
    nsExec::ExecToLog '"$INSTDIR\endpoint-agent.exe" -server "$ServerURL" -creds "$DATADIR\creds.json" -service install'
    Pop $0
    ${If} $0 != 0
        DetailPrint "Service install command exited with code $0"
    ${EndIf}

    ; 5. Start Windows Service
    DetailPrint "Starting Windows Service '${SERVICE_NAME}'..."
    nsExec::ExecToLog '"$INSTDIR\endpoint-agent.exe" -service start'
    Pop $0
    ${If} $0 != 0
        DetailPrint "Service start exited with code $0 (will start on next boot or once enrolled)"
    ${EndIf}

    ; 6. Write registry keys for uninstaller
    WriteRegStr HKLM "${SERVICE_KEY}" "InstallDir" "$INSTDIR"
    WriteRegStr HKLM "${SERVICE_KEY}" "ServerURL" "$ServerURL"
    WriteRegStr HKLM "${SERVICE_KEY}" "DataDir" "$DATADIR"

    WriteRegStr HKLM "${UNINSTALL_KEY}" "DisplayName" "Endpoint Management Agent"
    WriteRegStr HKLM "${UNINSTALL_KEY}" "UninstallString" '"$INSTDIR\uninstall.exe"'
    WriteRegStr HKLM "${UNINSTALL_KEY}" "DisplayIcon" '"$INSTDIR\endpoint-agent.exe"'
    WriteRegStr HKLM "${UNINSTALL_KEY}" "DisplayVersion" "${VERSION}"
    WriteRegStr HKLM "${UNINSTALL_KEY}" "Publisher" "Enterprise Endpoint Management"
    WriteRegDWORD HKLM "${UNINSTALL_KEY}" "NoModify" 1
    WriteRegDWORD HKLM "${UNINSTALL_KEY}" "NoRepair" 1

    ; 7. Create Uninstaller
    WriteUninstaller "$INSTDIR\uninstall.exe"

    DetailPrint "Endpoint Agent installed and configured successfully."
SectionEnd

; --- Uninstaller Custom Functions ---
Function un.onInit
    !insertmacro ResolvePaths
    ${GetParameters} $R0
    ${GetOptions} "$R0" "/TOKEN=" $UninstallToken
FunctionEnd

Function un.ConfirmTokenPage
    !insertmacro ResolvePaths

    ; Unenrolled agents do not require an authorization token
    ${IfNot} ${FileExists} "$DATADIR\creds.json"
        Abort
    ${EndIf}

    !insertmacro MUI_HEADER_TEXT "Uninstall Verification" "Administrator authorization token required for uninstallation."

    nsDialogs::Create 1018
    Pop $UninstDialog
    ${If} $UninstDialog == error
        Abort
    ${EndIf}

    ${NSD_CreateLabel} 0 0 100% 20u "This endpoint is managed by central policy. An Administrator Uninstall Verification Token is required to authorize removal:"
    Pop $0

    ${NSD_CreateLabel} 0 24u 100% 12u "Verification Token (issued from Management Console -> Devices):"
    Pop $0

    ${NSD_CreateText} 0 38u 100% 14u "$UninstallToken"
    Pop $TextUninstToken

    ${NSD_CreateLabel} 0 58u 100% 28u "Note: Request a token from your IT administrator before continuing. Uninstallation will be rejected if the token is missing, expired, or invalid."
    Pop $0

    nsDialogs::Show
FunctionEnd

Function un.ConfirmTokenPageLeave
    !insertmacro ResolvePaths

    ${If} ${FileExists} "$DATADIR\creds.json"
        ${NSD_GetText} $TextUninstToken $UninstallToken

        ${If} $UninstallToken == ""
            MessageBox MB_ICONEXCLAMATION|MB_OK "An administrator verification token is required to uninstall this managed agent.$\n$\nPlease request an uninstallation token from your IT administrator."
            Abort
        ${EndIf}

        DetailPrint "Verifying uninstallation authorization..."
        nsExec::ExecToStack '"$INSTDIR\endpoint-agent.exe" -creds "$DATADIR\creds.json" -verify-uninstall "$UninstallToken"'
        Pop $0 ; Exit code
        Pop $1 ; Output

        ${If} $0 != 0
            MessageBox MB_ICONSTOP|MB_OK "Uninstallation Rejected: Invalid or expired verification token.$\n$\n$1$\n$\nPlease contact your administrator."
            Abort
        ${EndIf}
    ${EndIf}
FunctionEnd

; --- Uninstaller Section ---
Section "Uninstall"
    !insertmacro ResolvePaths

    ; In silent mode or direct invocation, enforce token verification if enrolled
    ${If} ${FileExists} "$DATADIR\creds.json"
    ${AndIf} ${FileExists} "$INSTDIR\endpoint-agent.exe"
        ${If} $UninstallToken == ""
            DetailPrint "ERROR: Managed device requires an administrator uninstall token (/TOKEN=<token>)."
            Abort
        ${EndIf}
        nsExec::ExecToStack '"$INSTDIR\endpoint-agent.exe" -creds "$DATADIR\creds.json" -verify-uninstall "$UninstallToken"'
        Pop $0
        Pop $1
        ${If} $0 != 0
            DetailPrint "ERROR: Invalid or expired verification token: $1"
            Abort
        ${EndIf}
    ${EndIf}

    ; Move working directory out of install tree
    SetOutPath "$TEMP"

    DetailPrint "Stopping Windows Service '${SERVICE_NAME}'..."
    nsExec::ExecToLog 'net.exe stop "${SERVICE_NAME}"'
    Pop $0
    nsExec::Exec 'cmd.exe /c "taskkill.exe /F /IM endpoint-agent.exe /T 2>nul"'
    Pop $0
    Sleep 1000

    DetailPrint "Removing Windows Service '${SERVICE_NAME}'..."
    ${If} $UninstallToken != ""
        nsExec::ExecToLog '"$INSTDIR\endpoint-agent.exe" -creds "$DATADIR\creds.json" -service uninstall -uninstall-token "$UninstallToken"'
    ${Else}
        nsExec::ExecToLog '"$INSTDIR\endpoint-agent.exe" -service uninstall'
    ${EndIf}
    Pop $0
    ${If} $0 != 0
        ${If} ${FileExists} "$DATADIR\creds.json"
            MessageBox MB_ICONSTOP|MB_OK "Service uninstallation was rejected (exit code $0). Aborting uninstallation."
            Abort
        ${EndIf}
    ${EndIf}

    DetailPrint "Cleaning up Network Filter firewall rules..."
    nsExec::ExecToLog 'netsh advfirewall firewall delete rule group="EndpointManager-Filter"'
    Pop $0

    ; Allow SCM short delay to release process handles
    Sleep 2000

    ; Remove files and directories
    Delete "$INSTDIR\endpoint-agent.exe"
    Delete "$INSTDIR\uninstall.exe"
    RMDir "$INSTDIR"

    ; Clean up system credentials and log cache
    Delete "$DATADIR\creds.json"
    Delete "$DATADIR\*.log"
    RMDir /r "$DATADIR"

    ; Remove Registry entries
    DeleteRegKey HKLM "${UNINSTALL_KEY}"
    DeleteRegKey HKLM "${SERVICE_KEY}"

    DetailPrint "Endpoint Agent uninstalled successfully."
SectionEnd
