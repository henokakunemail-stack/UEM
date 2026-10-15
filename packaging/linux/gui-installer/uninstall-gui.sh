#!/usr/bin/env bash
# ==============================================================================
# Linux Desktop GUI Uninstaller for Endpoint Management Agent
#
# Non-interactive use: pass -y/--yes to skip the confirmation prompt, and
# --quiet to also skip the final dialog. A fleet operator running this from a
# management tool has neither a desktop session nor a person to click "OK".
# ==============================================================================
set -euo pipefail

TITLE="Endpoint Management Agent Uninstaller"

ASSUME_YES=false
QUIET=false
UNINSTALL_TOKEN="${UNINSTALL_TOKEN:-}"
show_help() {
    cat <<EOF
Usage: $(basename "$0") [OPTIONS]

Options:
  -y, --yes          Skip the confirmation prompt and uninstall immediately.
                     Required for scripted/remote use, where there is no one to
                     answer a dialog.
  -t, --token TOKEN  Administrator Uninstall Verification Token required for enrolled endpoints.
  -q, --quiet        Also skip the final "uninstalled" dialog. Implies --yes, since
                     the confirmation is a dialog too.
  -h, --help         Show this help and exit.
EOF
}
while [ "$#" -gt 0 ]; do
    case "$1" in
        -y|--yes)   ASSUME_YES=true ;;
        -t|--token) UNINSTALL_TOKEN="$2"; shift ;;
        -q|--quiet) QUIET=true; ASSUME_YES=true ;;
        -h|--help)  show_help; exit 0 ;;
        *) echo "Unknown option: $1" >&2; echo >&2; show_help >&2; exit 2 ;;
    esac
    shift
done

# 1. Elevate to root via pkexec/sudo
if [ "$EUID" -ne 0 ]; then
    if command -v pkexec >/dev/null 2>&1; then
        exec pkexec "$0" "$@"
    elif command -v gksudo >/dev/null 2>&1; then
        exec gksudo "$0" "$@"
    else
        echo "[-] Uninstaller requires root privileges. Please run with sudo: sudo $0"
        exit 1
    fi
fi

USE_TOOL=""
if command -v zenity >/dev/null 2>&1; then
    USE_TOOL="zenity"
elif command -v kdialog >/dev/null 2>&1; then
    USE_TOOL="kdialog"
fi

# 2. Confirmation Dialog
#
# When --yes was passed, there is nothing to ask and no dialog to ask it with.
# This is the branch a fleet tool takes, and it is also the branch that makes
# the prompt below unreachable: without it, a redirected stdin (no TTY, no
# person) makes `read` return non-zero and `set -e` exits the script in the
# middle of step 2 with no message at all.
CONFIRM_MSG="Are you sure you want to completely uninstall Endpoint Management Agent?\n\nThis will stop the daemon service, remove the agent executable, and delete all local credentials and logs."
if [ "$ASSUME_YES" = true ]; then
    : # proceed without prompting
elif [ "$USE_TOOL" = "zenity" ]; then
    zenity --question --title="$TITLE" --width=450 --text="$CONFIRM_MSG" || exit 0
elif [ "$USE_TOOL" = "kdialog" ]; then
    kdialog --title "$TITLE" --yesno "$CONFIRM_MSG" || exit 0
else
    if ! read -rp "Are you sure you want to uninstall Endpoint Agent? [y/N]: " ANS; then
        echo "[-] No confirmation received (stdin is closed or not a terminal)." >&2
        echo "[-] Run with --yes to uninstall without prompting." >&2
        exit 1
    fi
    if [[ ! "$ANS" =~ ^[Yy]$ ]]; then
        echo "Uninstallation canceled."
        exit 0
    fi
fi

# 2b. Dual-verification check: prevent unauthorized removal of enrolled endpoints
CREDS_FILE="/etc/endpoint-agent/creds.json"
INSTALL_BIN="/usr/local/bin/endpoint-agent"
if [ -f "$CREDS_FILE" ] && [ -x "$INSTALL_BIN" ]; then
    if [ -z "$UNINSTALL_TOKEN" ]; then
        if [ "$USE_TOOL" = "zenity" ]; then
            UNINSTALL_TOKEN=$(zenity --entry --title="$TITLE" --width=450 --text="This managed endpoint requires an Administrator Uninstall Token.\n\nPlease enter the verification token generated from your Management Console:" || true)
        elif [ "$USE_TOOL" = "kdialog" ]; then
            UNINSTALL_TOKEN=$(kdialog --title "$TITLE" --inputbox "Enter Administrator Uninstall Verification Token:" || true)
        else
            if [ -t 0 ]; then
                read -r -p "Enter Administrator Uninstall Verification Token: " UNINSTALL_TOKEN || true
            fi
        fi
    fi

    if [ -z "$UNINSTALL_TOKEN" ]; then
        FAIL_MSG="Uninstallation aborted: Administrator Uninstall Token is required."
        if [ "$USE_TOOL" = "zenity" ]; then
            zenity --error --title="$TITLE" --text="$FAIL_MSG" || true
        elif [ "$USE_TOOL" = "kdialog" ]; then
            kdialog --title "$TITLE" --error "$FAIL_MSG" || true
        else
            echo "[-] ERROR: $FAIL_MSG" >&2
        fi
        exit 1
    fi

    if ! "$INSTALL_BIN" -creds "$CREDS_FILE" -verify-uninstall "$UNINSTALL_TOKEN"; then
        FAIL_MSG="Verification failed: Invalid or expired uninstallation token.\n\nPlease contact your administrator to generate a token from the Management Console."
        if [ "$USE_TOOL" = "zenity" ]; then
            zenity --error --title="$TITLE" --width=450 --text="$FAIL_MSG" || true
        elif [ "$USE_TOOL" = "kdialog" ]; then
            kdialog --title "$TITLE" --error "$FAIL_MSG" || true
        else
            echo "[-] ERROR: Invalid or expired uninstallation token." >&2
        fi
        exit 1
    fi
fi

# 3. Stop and Uninstall Service
if [ -x /usr/local/bin/endpoint-agent ]; then
    /usr/local/bin/endpoint-agent -service stop || true
    /usr/local/bin/endpoint-agent -service uninstall || true
fi

# Additional systemd cleanup
if [ -d /run/systemd/system ]; then
    systemctl stop endpoint-agent.service 2>/dev/null || true
    systemctl disable endpoint-agent.service 2>/dev/null || true
    rm -f /etc/systemd/system/endpoint-agent.service
    systemctl daemon-reload 2>/dev/null || true
fi

# 4. Remove Files, Credentials, and Desktop Launcher
rm -f /usr/local/bin/endpoint-agent
rm -rf /etc/endpoint-agent
rm -f /usr/share/applications/endpoint-agent-uninstall.desktop
rm -f /usr/local/bin/endpoint-agent-uninstall-gui

# 5. Success Dialog
DONE_MSG="Endpoint Management Agent has been completely removed from this system."
if [ "$QUIET" = true ]; then
    : # --quiet: no dialog, nothing to confirm against either
elif [ "$USE_TOOL" = "zenity" ]; then
    zenity --info --title="$TITLE" --width=400 --text="$DONE_MSG"
elif [ "$USE_TOOL" = "kdialog" ]; then
    kdialog --title "$TITLE" --msgbox "$DONE_MSG"
else
    echo "[+] $DONE_MSG"
fi
