; ==============================================================================
; Enterprise Endpoint Management Agent - NSIS Modern UI Installer
; ==============================================================================

!include "MUI2.nsh"
!include "LogicLib.nsh"
!include "nsDialogs.nsh"

; --- General Settings ---
Name "Endpoint Management Agent"
OutFile "EndpointAgent-Setup.exe"
Unicode True
RequestExecutionLevel admin

InstallDir "$PROGRAMFILES64\EndpointAgent"
InstallDirRegKey HKLM "Software\EndpointAgent" "InstallDir"

; --- Version Information ---
; /DVERSION= and /DVERSION_NUM= are both passed by build.ps1, derived from the
; repository's VERSION file. The defaults below keep a direct makensis run
; from failing on undefined macros; they exist so the script compiles, not
; because 1.0.0 is special anywhere else.
!ifndef VERSION
  !define VERSION "1.0.0"
!endif
; VIProductVersion wants four numeric components, so the build script passes
; 1.0.0 as 1.0.0.0 separately rather than rendering "${VERSION}.0" here.
!ifndef VERSION_NUM
  !define VERSION_NUM "1.0.0.0"
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
!insertmacro MUI_UNPAGE_INSTFILES
!insertmacro MUI_UNPAGE_FINISH

; --- Languages ---
!insertmacro MUI_LANGUAGE "English"

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

    ${NSD_CreateText} 0 14u 100% 12u "https://mgmt.example.com"
    Pop $TextServer

    ; Enrollment Token Label and Textbox
    ${NSD_CreateLabel} 0 34u 100% 12u "Enrollment Token (Generate from Web Console -> Devices):"
    Pop $LabelToken

    ${NSD_CreateText} 0 48u 100% 12u ""
    Pop $TextToken

    ${NSD_CreateLabel} 0 70u 100% 24u "Note: If enrollment token is left empty, agent binary will be installed but service will not enroll automatically until token is provided via CLI."
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
FunctionEnd

; --- Installer Section ---
Section "Install Agent" SecInstall
    SetOutPath "$INSTDIR"

    ; 1. Copy agent binary
    File "/oname=endpoint-agent.exe" "..\..\agent-windows-amd64.exe"

    ; 2. Ensure system-wide ProgramData directory exists for credentials
    CreateDirectory "$COMMONAPPDATA\EndpointAgent"

    ; 3. Run Enrollment if token provided
    ${If} $EnrollToken != ""
        DetailPrint "Enrolling device with central server..."
        nsExec::ExecToLog '"$INSTDIR\endpoint-agent.exe" -server "$ServerURL" -enroll "$EnrollToken" -creds "$COMMONAPPDATA\EndpointAgent\creds.json"'
        Pop $0
        ${If} $0 != 0
            DetailPrint "Warning: Enrollment returned code $0. Service registration will continue."
        ${Else}
            DetailPrint "Device enrolled successfully."
        ${EndIf}
    ${EndIf}

    ; 4. Register Windows Service under Service Control Manager
    DetailPrint "Registering Windows Service 'endpoint-agent'..."
    nsExec::ExecToLog '"$INSTDIR\endpoint-agent.exe" -server "$ServerURL" -creds "$COMMONAPPDATA\EndpointAgent\creds.json" -service install'
    Pop $0
    ${If} $0 != 0
        DetailPrint "Service install command exited with code $0"
    ${EndIf}

    ; 5. Start Windows Service
    DetailPrint "Starting Windows Service 'endpoint-agent'..."
    nsExec::ExecToLog '"$INSTDIR\endpoint-agent.exe" -service start'
    Pop $0
    ${If} $0 != 0
        DetailPrint "Service start exited with code $0 (will start on next boot or once enrolled)"
    ${EndIf}

    ; 6. Write registry keys for uninstaller
    WriteRegStr HKLM "Software\EndpointAgent" "InstallDir" "$INSTDIR"
    WriteRegStr HKLM "Software\EndpointAgent" "ServerURL" "$ServerURL"

    WriteRegStr HKLM "Software\Microsoft\Windows\CurrentVersion\Uninstall\EndpointAgent" "DisplayName" "Endpoint Management Agent"
    WriteRegStr HKLM "Software\Microsoft\Windows\CurrentVersion\Uninstall\EndpointAgent" "UninstallString" '"$INSTDIR\uninstall.exe"'
    WriteRegStr HKLM "Software\Microsoft\Windows\CurrentVersion\Uninstall\EndpointAgent" "DisplayIcon" '"$INSTDIR\endpoint-agent.exe"'
    WriteRegStr HKLM "Software\Microsoft\Windows\CurrentVersion\Uninstall\EndpointAgent" "DisplayVersion" "${VERSION}"
    WriteRegStr HKLM "Software\Microsoft\Windows\CurrentVersion\Uninstall\EndpointAgent" "Publisher" "Enterprise Endpoint Management"
    WriteRegDWORD HKLM "Software\Microsoft\Windows\CurrentVersion\Uninstall\EndpointAgent" "NoModify" 1
    WriteRegDWORD HKLM "Software\Microsoft\Windows\CurrentVersion\Uninstall\EndpointAgent" "NoRepair" 1

    ; 7. Create Uninstaller
    WriteUninstaller "$INSTDIR\uninstall.exe"

    DetailPrint "Endpoint Agent installed and configured successfully."
SectionEnd

; --- Uninstaller Section ---
Section "Uninstall"
    DetailPrint "Stopping Windows Service 'endpoint-agent'..."
    nsExec::ExecToLog '"$INSTDIR\endpoint-agent.exe" -service stop'
    Pop $0

    DetailPrint "Removing Windows Service 'endpoint-agent'..."
    nsExec::ExecToLog '"$INSTDIR\endpoint-agent.exe" -service uninstall'
    Pop $0

    ; Allow SCM short delay to release process handles
    Sleep 2000

    ; Remove files and directories
    Delete "$INSTDIR\endpoint-agent.exe"
    Delete "$INSTDIR\uninstall.exe"
    RMDir "$INSTDIR"

    ; Clean up system credentials and log cache
    Delete "$COMMONAPPDATA\EndpointAgent\creds.json"
    Delete "$COMMONAPPDATA\EndpointAgent\*.log"
    RMDir "$COMMONAPPDATA\EndpointAgent"

    ; Remove Registry entries
    DeleteRegKey HKLM "Software\Microsoft\Windows\CurrentVersion\Uninstall\EndpointAgent"
    DeleteRegKey HKLM "Software\EndpointAgent"

    DetailPrint "Endpoint Agent uninstalled successfully."
SectionEnd
