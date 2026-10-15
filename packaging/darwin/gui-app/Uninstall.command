#!/usr/bin/env bash
# ==============================================================================
# Native macOS GUI Uninstaller for Endpoint Management Agent
# Uses AppleScript (osascript) dialogs, 2-step Token Verification, and Admin elevation
# ==============================================================================

osascript <<'APPLESCRIPT'
try
    set dialogResult to display dialog "Are you sure you want to completely uninstall Endpoint Management Agent?" & return & return & "This will stop the background launchd daemon, remove the agent binary, and delete all local credentials and logs." with title "Endpoint Management Agent Uninstaller" buttons {"Cancel", "Continue"} default button "Continue" with icon caution

    if button returned of dialogResult is "Continue" then
        set credsExist to false
        try
            do shell script "test -f /etc/endpoint-agent/creds.json && test -x /usr/local/bin/endpoint-agent"
            set credsExist to true
        on error
            set credsExist to false
        end try

        set tokenArg to ""
        if credsExist then
            set tokenDialog to display dialog "This managed Mac requires an Administrator Uninstall Token." & return & return & "Please enter the token generated from your Management Console:" default answer "" with title "Uninstall Verification Required" buttons {"Cancel", "Verify & Uninstall"} default button "Verify & Uninstall" with icon caution
            set enteredToken to text returned of tokenDialog
            if enteredToken is "" then
                display alert "Uninstallation Aborted" message "A valid uninstallation token is required to remove this managed agent." as critical
                return
            end if

            -- Verify token against central server
            try
                do shell script "/usr/local/bin/endpoint-agent -creds /etc/endpoint-agent/creds.json -verify-uninstall " & quoted form of enteredToken
            on error
                display alert "Verification Failed" message "Invalid or expired uninstallation token." & return & return & "Please contact your administrator to generate a token from the Management Console." as critical
                return
            end try
            set tokenArg to enteredToken
        end if

        set uninstScript to "launchctl unload -w /Library/LaunchDaemons/com.endpoint-mgmt.agent.plist 2>/dev/null || true; " & ¬
            "rm -f /Library/LaunchDaemons/com.endpoint-mgmt.agent.plist; " & ¬
            "rm -f /usr/local/bin/endpoint-agent; " & ¬
            "rm -rf /etc/endpoint-agent; " & ¬
            "rm -f /var/log/endpoint-agent.log /var/log/endpoint-agent.err;"

        do shell script uninstScript with administrator privileges

        display dialog "Endpoint Management Agent has been successfully uninstalled from your Mac." with title "Uninstallation Complete" buttons {"OK"} default button "OK" with icon note
    end if
on error number -128
    -- User canceled, do nothing
end try
APPLESCRIPT
