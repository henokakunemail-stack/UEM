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
show_help() {
    cat <<EOF
Usage: $(basename "$0") [OPTIONS]

Options:
  -y, --yes     Skip the confirmation prompt and uninstall immediately.
                Required for scripted/remote use, where there is no one to
                answer a dialog.
  -q, --quiet   Also skip the final "uninstalled" dialog. Implies --yes, since
                the confirmation is a dialog too.
  -h, --help    Show this help and exit.
EOF
}
while [ "$#" -gt 0 ]; do
    case "$1" in
        -y|--yes)   ASSUME_YES=true ;;
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
