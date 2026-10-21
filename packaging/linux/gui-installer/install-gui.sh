#!/usr/bin/env bash
# ==============================================================================
# Linux Desktop GUI Installer for Endpoint Management Agent
# Supports Zenity (GNOME/Ubuntu) and KDialog (KDE) with terminal fallback
# ==============================================================================
set -euo pipefail

TITLE="Endpoint Management Agent Installer"

# 1. Elevate to root via pkexec/sudo if running as normal user
if [ "$EUID" -ne 0 ]; then
    if command -v pkexec >/dev/null 2>&1; then
        exec pkexec "$0" "$@"
    elif command -v gksudo >/dev/null 2>&1; then
        exec gksudo "$0" "$@"
    else
        echo "[-] This installer requires root privileges. Please run with sudo: sudo $0"
        exit 1
    fi
fi

# Detect GUI toolkit
USE_TOOL=""
if command -v zenity >/dev/null 2>&1; then
    USE_TOOL="zenity"
elif command -v kdialog >/dev/null 2>&1; then
    USE_TOOL="kdialog"
fi

# 2. Welcome Dialog
if [ "$USE_TOOL" = "zenity" ]; then
    zenity --info --title="$TITLE" --width=450 --height=220 \
        --text="Welcome to the Endpoint Management Agent Installer.\n\nThis wizard will install the background agent daemon, configure server communication, and register systemd services."
elif [ "$USE_TOOL" = "kdialog" ]; then
    kdialog --title "$TITLE" --msgbox "Welcome to the Endpoint Management Agent Installer.\n\nThis wizard will install the background agent daemon, configure server communication, and register systemd services."
fi

# 3. Server Configuration Dialog
SERVER_URL="https://mgmt.example.com"
ENROLL_TOKEN=""

if [ "$USE_TOOL" = "zenity" ]; then
    FORM_OUT=$(zenity --forms --title="$TITLE - Server Configuration" --width=500 \
        --text="Enter your central server connection details:" \
        --add-entry="Management Server URL:" \
        --add-entry="Enrollment Token (optional):" \
        --separator="|") || exit 1

    SERVER_URL=$(echo "$FORM_OUT" | cut -d'|' -f1)
    ENROLL_TOKEN=$(echo "$FORM_OUT" | cut -d'|' -f2)
elif [ "$USE_TOOL" = "kdialog" ]; then
    SERVER_URL=$(kdialog --title "$TITLE" --inputbox "Enter Management Server URL (HTTPS/WSS):" "$SERVER_URL") || exit 1
    ENROLL_TOKEN=$(kdialog --title "$TITLE" --inputbox "Enter Enrollment Token (from Web Console, optional):" "") || exit 1
else
    # Terminal Fallback
    echo "=== $TITLE ==="
    read -rp "Enter Management Server URL [https://mgmt.example.com]: " INPUT_URL
    SERVER_URL="${INPUT_URL:-https://mgmt.example.com}"
    read -rp "Enter Enrollment Token (optional): " ENROLL_TOKEN
fi

# 4. Perform Installation
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
BIN_SRC=""
for cand in "$SCRIPT_DIR/../../agent-linux-amd64" "$SCRIPT_DIR/endpoint-agent" /tmp/agent-linux-amd64; do
    if [ -f "$cand" ]; then BIN_SRC="$cand"; break; fi
done

if [ -z "$BIN_SRC" ]; then
    MSG="Agent binary not found. Please compile or place 'agent-linux-amd64' alongside installer."
    if [ "$USE_TOOL" = "zenity" ]; then
        zenity --error --title="$TITLE" --text="$MSG"
    else
        echo "[-] $MSG" >&2
    fi
    exit 1
fi

BIN_CHECK="$BIN_SRC"
# These scripts install whatever agent binary is lying next to them. One built
# before -site and the stored-identity check existed exits at once on -site
# ("flag provided but not defined") or re-enrolls on every run, spending a use
# of a multi-device token each time. Refuse it with a clear reason.
if ! "$BIN_CHECK" -h 2>&1 | grep -q -- '-reenroll'; then
    MSG_OLD="The agent binary at $BIN_CHECK is older than this installer (no -reenroll/-site support). Rebuild it from source or download the current release."
    if [ "$USE_TOOL" = "zenity" ]; then
        zenity --error --title="$TITLE" --text="$MSG_OLD"
    else
        echo "[-] $MSG_OLD" >&2
    fi
    exit 1
fi

DEST_BIN="/usr/local/bin/endpoint-agent"
CREDS_PATH="/etc/endpoint-agent/creds.json"

mkdir -p /usr/local/bin /etc/endpoint-agent
chmod 700 /etc/endpoint-agent
cp "$BIN_SRC" "$DEST_BIN"
chmod 0755 "$DEST_BIN"

# Perform Enrollment if token provided
if [ -n "$ENROLL_TOKEN" ]; then
    if ! ENROLL_OUT=$("$DEST_BIN" -server "$SERVER_URL" -enroll "$ENROLL_TOKEN" -enroll-only -creds "$CREDS_PATH" 2>&1); then
        MSG="Enrollment failed: $(printf '%s' "$ENROLL_OUT" | tail -n 3)"
        if [ "$USE_TOOL" = "zenity" ]; then
            zenity --error --title="$TITLE" --text="$MSG"
        else
            echo "[-] $MSG" >&2
        fi
        exit 1
    fi
fi

# Register and start systemd service
"$DEST_BIN" -server "$SERVER_URL" -creds "$CREDS_PATH" -service install || true
"$DEST_BIN" -service start || true

# Install GUI Uninstaller script and Desktop Entry
UNINSTALL_SCRIPT="/usr/local/bin/endpoint-agent-uninstall-gui"
cp "$SCRIPT_DIR/uninstall-gui.sh" "$UNINSTALL_SCRIPT"
chmod 0755 "$UNINSTALL_SCRIPT"

cat <<EOF > /usr/share/applications/endpoint-agent-uninstall.desktop
[Desktop Entry]
Name=Uninstall Endpoint Agent
Comment=Remove the Endpoint Management Agent service and files
Exec=$UNINSTALL_SCRIPT
Icon=system-software-install
Terminal=false
Type=Application
Categories=System;Settings;
EOF
chmod 0644 /usr/share/applications/endpoint-agent-uninstall.desktop

# 5. Success Dialog
SUCCESS_MSG="Endpoint Management Agent successfully installed!\n\nDaemon: systemctl status endpoint-agent\nServer: $SERVER_URL\nUninstaller: /usr/local/bin/endpoint-agent-uninstall-gui"

if [ "$USE_TOOL" = "zenity" ]; then
    zenity --info --title="$TITLE" --width=450 --text="$SUCCESS_MSG"
elif [ "$USE_TOOL" = "kdialog" ]; then
    kdialog --title "$TITLE" --msgbox "$SUCCESS_MSG"
else
    echo -e "[+] $SUCCESS_MSG"
fi
