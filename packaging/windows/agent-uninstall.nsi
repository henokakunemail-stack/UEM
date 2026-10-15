; ==============================================================================
; Enterprise Endpoint Management Agent - Standalone GUI Uninstaller
; ==============================================================================

!include "MUI2.nsh"
!include "LogicLib.nsh"
!include "nsDialogs.nsh"
!include "FileFunc.nsh"

; --- General Settings ---
Name "Endpoint Management Agent Uninstaller"
OutFile "EndpointAgent-Uninstall.exe"
Unicode True
RequestExecutionLevel admin

!define SERVICE_NAME "endpoint-agent"
!define SERVICE_KEY "Software\EndpointAgent"
!define UNINSTALL_KEY "Software\Microsoft\Windows\CurrentVersion\Uninstall\EndpointAgent"

; --- Version Information ---
!ifndef VERSION
  !define VERSION "1.5.0"
!endif
!ifndef VERSION_NUM
  !define VERSION_NUM "1.5.0.0"
!endif
VIProductVersion "${VERSION_NUM}"
VIAddVersionKey "ProductName" "Endpoint Management Agent Uninstaller"
VIAddVersionKey "CompanyName" "Enterprise Management"
VIAddVersionKey "LegalCopyright" "MIT License"
VIAddVersionKey "FileDescription" "Enterprise Endpoint Management Agent Standalone Uninstaller"
VIAddVersionKey "FileVersion" "${VERSION_NUM}"
VIAddVersionKey "ProductVersion" "${VERSION}"

; --- Variables ---
Var Dialog
Var CheckboxPurge
Var CheckboxPurgeState
Var TARGET_INSTDIR
Var TARGET_DATADIR
Var TextToken
Var UninstallToken

; --- Interface Settings ---
!define MUI_ABORTWARNING
!define MUI_ICON "${NSISDIR}\Contrib\Graphics\Icons\modern-uninstall.ico"
!define MUI_UNICON "${NSISDIR}\Contrib\Graphics\Icons\modern-uninstall.ico"

; --- Pages ---
!insertmacro MUI_PAGE_WELCOME
Page custom ConfirmOptionsPage ConfirmOptionsPageLeave
!insertmacro MUI_PAGE_INSTFILES
!insertmacro MUI_PAGE_FINISH

!insertmacro MUI_LANGUAGE "English"

; ------------------------------------------------------------------------------
; Initialization: Locate existing installation
; ------------------------------------------------------------------------------
Function .onInit
    SetShellVarContext all
    ReadRegStr $TARGET_INSTDIR HKLM "${SERVICE_KEY}" "InstallDir"
    ${If} $TARGET_INSTDIR == ""
        ReadRegStr $TARGET_INSTDIR HKLM "${UNINSTALL_KEY}" "InstallLocation"
    ${EndIf}
    ${If} $TARGET_INSTDIR == ""
        StrCpy $TARGET_INSTDIR "$PROGRAMFILES64\EndpointAgent"
    ${EndIf}

    ReadRegStr $TARGET_DATADIR HKLM "${SERVICE_KEY}" "DataDir"
    ${If} $TARGET_DATADIR == ""
        StrCpy $TARGET_DATADIR "$APPDATA\EndpointAgent"
    ${EndIf}
FunctionEnd

; ------------------------------------------------------------------------------
; Custom Options Page
; ------------------------------------------------------------------------------
Function ConfirmOptionsPage
    !insertmacro MUI_HEADER_TEXT "Uninstall Options" "Enter verification token and choose cleanup actions."

    nsDialogs::Create 1018
    Pop $Dialog
    ${If} $Dialog == error
        Abort
    ${EndIf}

    ${NSD_CreateLabel} 0 0 100% 16u "This wizard will stop and remove the 'endpoint-agent' service from:$\n$TARGET_INSTDIR"
    Pop $0

    ${NSD_CreateLabel} 0 20u 100% 10u "Administrator Uninstall Verification Token:"
    Pop $0

    ${NSD_CreateText} 0 32u 100% 14u "$UninstallToken"
    Pop $TextToken

    ${NSD_CreateLabel} 0 48u 100% 18u "To prevent unauthorized uninstallation, enter a time-limited token issued by an administrator from the Endpoint Management Console."
    Pop $0

    ${NSD_CreateCheckbox} 0 70u 100% 14u "Purge device credentials (creds.json) and cache logs from ProgramData"
    Pop $CheckboxPurge
    ${NSD_Check} $CheckboxPurge

    ${NSD_CreateLabel} 0 88u 100% 18u "Note: If unchecked, credentials are kept so re-installing will automatically resume fleet connectivity."
    Pop $0

    nsDialogs::Show
FunctionEnd

Function ConfirmOptionsPageLeave
    ${NSD_GetText} $TextToken $UninstallToken
    ${NSD_GetState} $CheckboxPurge $CheckboxPurgeState

    ; Verify token against agent binary if enrolled
    ${If} ${FileExists} "$TARGET_DATADIR\creds.json"
    ${AndIf} ${FileExists} "$TARGET_INSTDIR\endpoint-agent.exe"
        ${If} $UninstallToken == ""
            MessageBox MB_ICONEXCLAMATION|MB_OK "An administrator verification token is required to uninstall this managed agent.$\n$\nPlease request an uninstallation token from your IT administrator."
            Abort
        ${EndIf}

        nsExec::ExecToStack '"$TARGET_INSTDIR\endpoint-agent.exe" -creds "$TARGET_DATADIR\creds.json" -verify-uninstall "$UninstallToken"'
        Pop $0 ; Exit code
        Pop $1 ; Output

        ${If} $0 != 0
            MessageBox MB_ICONSTOP|MB_OK "Uninstallation Rejected: Invalid or expired verification token.$\n$\n$1$\n$\nPlease contact your administrator."
            Abort
        ${EndIf}
    ${EndIf}
FunctionEnd

; ------------------------------------------------------------------------------
; Execution Section
; ------------------------------------------------------------------------------
Section "Remove Agent" SecMain
    SetOutPath "$TEMP"

    DetailPrint "Stopping Windows Service '${SERVICE_NAME}'..."
    nsExec::ExecToLog 'net.exe stop "${SERVICE_NAME}"'
    Pop $0
    nsExec::Exec 'cmd.exe /c "taskkill.exe /F /IM endpoint-agent.exe /T 2>nul"'
    Pop $0
    Sleep 1000

    DetailPrint "Removing Windows Service '${SERVICE_NAME}'..."
    ${If} ${FileExists} "$TARGET_INSTDIR\endpoint-agent.exe"
        ${If} $UninstallToken != ""
            nsExec::ExecToLog '"$TARGET_INSTDIR\endpoint-agent.exe" -creds "$TARGET_DATADIR\creds.json" -service uninstall -uninstall-token "$UninstallToken"'
        ${Else}
            nsExec::ExecToLog '"$TARGET_INSTDIR\endpoint-agent.exe" -service uninstall'
        ${EndIf}
        Pop $0
        ${If} $0 != 0
            DetailPrint "Service uninstall command exited with code $0. Forcing removal via sc.exe..."
            nsExec::ExecToLog 'sc.exe stop "${SERVICE_NAME}"'
            Pop $1
            nsExec::ExecToLog 'sc.exe delete "${SERVICE_NAME}"'
            Pop $1
        ${EndIf}
    ${Else}
        nsExec::ExecToLog 'sc.exe delete "${SERVICE_NAME}"'
        Pop $0
    ${EndIf}
    Sleep 1000

    DetailPrint "Cleaning up Network Filter firewall rules..."
    nsExec::ExecToLog 'netsh advfirewall firewall delete rule group="EndpointManager-Filter"'
    Pop $0

    ; Delete installation files
    DetailPrint "Removing files from '$TARGET_INSTDIR'..."
    Delete "$TARGET_INSTDIR\endpoint-agent.exe"
    Delete "$TARGET_INSTDIR\uninstall.exe"
    RMDir /r "$TARGET_INSTDIR"

    ; Clean credentials if requested
    ${If} $CheckboxPurgeState == ${BST_CHECKED}
        DetailPrint "Purging system credentials and cache from '$TARGET_DATADIR'..."
        Delete "$TARGET_DATADIR\creds.json"
        Delete "$TARGET_DATADIR\*.log"
        RMDir /r "$TARGET_DATADIR"
    ${Else}
        DetailPrint "Retaining credentials in '$TARGET_DATADIR' for future re-enrollment."
    ${EndIf}

    ; Clean Registry
    DetailPrint "Cleaning Windows Registry..."
    DeleteRegKey HKLM "${UNINSTALL_KEY}"
    DeleteRegKey HKLM "${SERVICE_KEY}"

    DetailPrint "Endpoint Management Agent successfully uninstalled."
SectionEnd
