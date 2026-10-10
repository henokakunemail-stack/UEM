; ==============================================================================
; Enterprise Endpoint Management Server - Standalone GUI Uninstaller
; ==============================================================================

!include "MUI2.nsh"
!include "LogicLib.nsh"
!include "nsDialogs.nsh"
!include "FileFunc.nsh"

; --- General Settings ---
Name "Endpoint Management Server Uninstaller"
OutFile "EndpointServer-Uninstall.exe"
Unicode True
RequestExecutionLevel admin

!define SERVER_NAME "endpoint-mgmt-server"
!define SERVICE_KEY "Software\EndpointMgmtServer"
!define UNINSTALL_KEY "Software\Microsoft\Windows\CurrentVersion\Uninstall\EndpointMgmtServer"

; --- Version Information ---
!ifndef VERSION
  !define VERSION "1.0.0"
!endif
!ifndef VERSION_NUM
  !define VERSION_NUM "1.0.0.0"
!endif
VIProductVersion "${VERSION_NUM}"
VIAddVersionKey "ProductName" "Endpoint Management Server Uninstaller"
VIAddVersionKey "CompanyName" "Enterprise Management"
VIAddVersionKey "LegalCopyright" "MIT License"
VIAddVersionKey "FileDescription" "Enterprise Endpoint Management Server Standalone Uninstaller"
VIAddVersionKey "FileVersion" "${VERSION_NUM}"
VIAddVersionKey "ProductVersion" "${VERSION}"

; --- Variables ---
Var Dialog
Var CheckboxPurgeConfig
Var CheckboxPurgeConfigState
Var CheckboxPurgeDB
Var CheckboxPurgeDBState
Var TARGET_INSTDIR
Var TARGET_DATADIR
Var TARGET_ENVFILE

; --- Interface Settings ---
!define MUI_ABORTWARNING
!define MUI_ICON "${NSISDIR}\Contrib\Graphics\Icons\modern-uninstall.ico"
!define MUI_UNICON "${NSISDIR}\Contrib\Graphics\Icons\modern-uninstall.ico"

; --- Pages ---
!insertmacro MUI_PAGE_WELCOME
Page custom ServerConfirmOptionsPage ServerConfirmOptionsPageLeave
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
        StrCpy $TARGET_INSTDIR "$PROGRAMFILES64\EndpointMgmtServer"
    ${EndIf}

    ReadRegStr $TARGET_ENVFILE HKLM "${SERVICE_KEY}" "EnvFile"
    ${If} $TARGET_ENVFILE != ""
        ${GetParent} "$TARGET_ENVFILE" $TARGET_DATADIR
    ${Else}
        StrCpy $TARGET_DATADIR "$APPDATA\EndpointMgmtServer"
        StrCpy $TARGET_ENVFILE "$TARGET_DATADIR\server.env"
    ${EndIf}
FunctionEnd

; ------------------------------------------------------------------------------
; Custom Options Page
; ------------------------------------------------------------------------------
Function ServerConfirmOptionsPage
    !insertmacro MUI_HEADER_TEXT "Uninstall Options" "Choose components to remove for Endpoint Management Server."

    nsDialogs::Create 1018
    Pop $Dialog
    ${If} $Dialog == error
        Abort
    ${EndIf}

    ${NSD_CreateLabel} 0 0 100% 24u "This wizard will stop and remove the '${SERVER_NAME}' Windows Service and binaries from:$\n$TARGET_INSTDIR"
    Pop $0

    ${NSD_CreateCheckbox} 0 28u 100% 14u "Remove server configuration (server.env) and logs from ProgramData"
    Pop $CheckboxPurgeConfig
    ${NSD_Check} $CheckboxPurgeConfig

    ${NSD_CreateCheckbox} 0 46u 100% 14u "Purge database and backups (server.db) - PERMANENT DATA DELETION"
    Pop $CheckboxPurgeDB
    ${NSD_Uncheck} $CheckboxPurgeDB

    ${NSD_CreateLabel} 0 68u 100% 24u "Note: Leaving database un-purged allows you to upgrade or reinstall the server without losing devices or audit logs."
    Pop $0

    nsDialogs::Show
FunctionEnd

Function ServerConfirmOptionsPageLeave
    ${NSD_GetState} $CheckboxPurgeConfig $CheckboxPurgeConfigState
    ${NSD_GetState} $CheckboxPurgeDB $CheckboxPurgeDBState
FunctionEnd

; ------------------------------------------------------------------------------
; Execution Section
; ------------------------------------------------------------------------------
Section "Remove Server" SecRemoveServer
    SetOutPath "$TEMP"

    DetailPrint "Stopping Windows Service '${SERVER_NAME}'..."
    ${If} ${FileExists} "$TARGET_INSTDIR\endpoint-server.exe"
        nsExec::ExecToLog '"$TARGET_INSTDIR\endpoint-server.exe" -service stop'
        Pop $0
    ${Else}
        nsExec::ExecToLog 'sc.exe stop "${SERVER_NAME}"'
        Pop $0
    ${EndIf}
    Sleep 2000

    DetailPrint "Removing Windows Service registration..."
    ${If} ${FileExists} "$TARGET_INSTDIR\endpoint-server.exe"
        nsExec::ExecToLog '"$TARGET_INSTDIR\endpoint-server.exe" -service uninstall'
        Pop $0
    ${Else}
        nsExec::ExecToLog 'sc.exe delete "${SERVER_NAME}"'
        Pop $0
    ${EndIf}
    Sleep 1000

    DetailPrint "Removing Windows Firewall rule..."
    nsExec::ExecToLog 'netsh advfirewall firewall delete rule name="Endpoint Management Server"'

    ; Clean server binaries
    DetailPrint "Removing server binary..."
    Delete "$TARGET_INSTDIR\endpoint-server.exe"
    Delete "$TARGET_INSTDIR\uninstall.exe"

    ; Clean config and logs if checked
    ${If} $CheckboxPurgeConfigState == ${BST_CHECKED}
        DetailPrint "Removing configuration and logs from '$TARGET_DATADIR'..."
        Delete "$TARGET_ENVFILE"
        Delete "$TARGET_DATADIR\server.log"
        RMDir /r "$TARGET_DATADIR"
    ${EndIf}

    ; Clean database if checked
    ${If} $CheckboxPurgeDBState == ${BST_CHECKED}
        DetailPrint "Purging database and backups..."
        RMDir /r "$TARGET_INSTDIR\data"
        RMDir /r "$TARGET_INSTDIR"
    ${Else}
        DetailPrint "Database retained in '$TARGET_INSTDIR\data'."
    ${EndIf}

    ; Clean Registry
    DetailPrint "Cleaning Windows Registry..."
    DeleteRegKey HKLM "${UNINSTALL_KEY}"
    DeleteRegKey HKLM "${SERVICE_KEY}"

    DetailPrint "Endpoint Management Server successfully uninstalled."
SectionEnd
