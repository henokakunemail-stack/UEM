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
  !define VERSION "1.0.0"
!endif
!ifndef VERSION_NUM
  !define VERSION_NUM "1.0.0.0"
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
    !insertmacro MUI_HEADER_TEXT "Uninstall Options" "Choose cleanup actions for Endpoint Management Agent."

    nsDialogs::Create 1018
    Pop $Dialog
    ${If} $Dialog == error
        Abort
    ${EndIf}

    ${NSD_CreateLabel} 0 0 100% 24u "This wizard will stop and remove the 'endpoint-agent' Windows Service and remove all agent binaries from:$\n$TARGET_INSTDIR"
    Pop $0

    ${NSD_CreateCheckbox} 0 32u 100% 14u "Purge device credentials (creds.json) and cache logs from ProgramData"
    Pop $CheckboxPurge
    ${NSD_Check} $CheckboxPurge

    ${NSD_CreateLabel} 0 54u 100% 24u "Note: If unchecked, credentials are kept so re-installing will automatically resume fleet connectivity without re-enrollment."
    Pop $0

    nsDialogs::Show
FunctionEnd

Function ConfirmOptionsPageLeave
    ${NSD_GetState} $CheckboxPurge $CheckboxPurgeState
FunctionEnd

; ------------------------------------------------------------------------------
; Execution Section
; ------------------------------------------------------------------------------
Section "Remove Agent" SecMain
    SetOutPath "$TEMP"

    DetailPrint "Stopping Windows Service '${SERVICE_NAME}'..."
    ${If} ${FileExists} "$TARGET_INSTDIR\endpoint-agent.exe"
        nsExec::ExecToLog '"$TARGET_INSTDIR\endpoint-agent.exe" -service stop'
        Pop $0
    ${Else}
        nsExec::ExecToLog 'sc.exe stop "${SERVICE_NAME}"'
        Pop $0
    ${EndIf}
    Sleep 1500

    DetailPrint "Removing Windows Service '${SERVICE_NAME}'..."
    ${If} ${FileExists} "$TARGET_INSTDIR\endpoint-agent.exe"
        nsExec::ExecToLog '"$TARGET_INSTDIR\endpoint-agent.exe" -service uninstall'
        Pop $0
    ${Else}
        nsExec::ExecToLog 'sc.exe delete "${SERVICE_NAME}"'
        Pop $0
    ${EndIf}
    Sleep 1000

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
