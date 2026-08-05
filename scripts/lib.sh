# Sourced by install.sh and uninstall.sh — shared OS detection and the
# per-OS conventions for where agent-winglet-app's built artifact and
# installed copy live, so both scripts agree without duplicating the logic.
# Not meant to be run directly.

APP_NAME="Winglet"
TRAY_BIN_NAME="agent-winglet-tray"

# Prints one of: darwin, linux, windows, unknown.
# Windows here means "running under a bash-capable environment on Windows"
# (Git Bash/MSYS2/Cygwin) — uname reports MINGW*/MSYS*/CYGWIN* in that case.
# Plain WSL reports Linux and is treated as Linux (correct: WSL has its own
# Linux filesystem/app-launcher conventions, not Windows' ones).
detect_os() {
  case "$(uname -s)" in
    Darwin) echo "darwin" ;;
    Linux) echo "linux" ;;
    MINGW*|MSYS*|CYGWIN*) echo "windows" ;;
    *) echo "unknown" ;;
  esac
}

# The file `wails build` produces under cmd/agent-winglet-app/build/bin,
# relative to that directory, for a given detect_os() value.
app_build_artifact() {
  case "$1" in
    darwin) echo "${APP_NAME}.app" ;;
    linux) echo "${APP_NAME}" ;;
    windows) echo "${APP_NAME}.exe" ;;
  esac
}

# Where the app's main artifact is installed to, per OS:
#   darwin  - the .app bundle itself, directly launchable/removable
#   linux   - the binary (the .desktop launcher and icon live alongside it,
#             at fixed paths derived from this by the caller)
#   windows - the .exe itself
app_install_path() {
  case "$1" in
    darwin) echo "/Applications/${APP_NAME}.app" ;;
    linux) echo "${HOME}/.local/bin/${APP_NAME}" ;;
    windows) echo "$(windows_local_app_dir)/${APP_NAME}/${APP_NAME}.exe" ;;
  esac
}

# linux/windows only — darwin's tray is built and installed as part of
# `make app`/the app's own install step (see the Makefile's
# nest-tray-darwin target and install.sh's darwin branch): it's nested
# inside Winglet.app so it can be registered as a proper macOS login item
# via SMAppService (cmd/agent-winglet-app/loginitem_darwin.go), which only
# works for a helper embedded in the calling app's own bundle, not a
# standalone one.

# The file `make tray` produces under cmd/agent-winglet-tray/build/bin,
# relative to that directory, for a given detect_os() value.
tray_build_artifact() {
  case "$1" in
    windows) echo "${TRAY_BIN_NAME}.exe" ;;
    *) echo "${TRAY_BIN_NAME}" ;;
  esac
}

# Where the tray helper binary is installed to — a stable path for the
# Startup-shortcut/autostart entry that launches it at login to point at,
# surviving across installs the same way the app's own install location
# does.
tray_install_path() {
  case "$1" in
    linux) echo "${HOME}/.local/bin/${TRAY_BIN_NAME}" ;;
    windows) echo "$(windows_local_app_dir)/${APP_NAME}/${TRAY_BIN_NAME}.exe" ;;
  esac
}

# The actual launchable executable, per OS — what a Startup-shortcut/
# autostart entry's Exec should point at. Same as tray_install_path here
# (both are bare binaries); kept as its own function for symmetry with
# app_install_path/app_build_artifact's naming, and because darwin used to
# need the two to differ before its tray moved into the app bundle.
tray_executable_path() {
  tray_install_path "$1"
}

# Legacy/alternate macOS install location — installers before this one
# (or a manual `make app` + drag) may have put the app in ~/Applications
# instead of /Applications. Both install.sh and uninstall.sh check this one
# too, for robustness, even though /Applications is the standard target now.
app_install_path_alt() {
  case "$1" in
    darwin) echo "${HOME}/Applications/${APP_NAME}.app" ;;
    *) echo "" ;;
  esac
}

# POSIX-style path to %LOCALAPPDATA% for use with plain bash file ops
# (mkdir/cp/rm), converted via cygpath when available since $LOCALAPPDATA
# itself is a native Windows path (C:\Users\...) that bash utilities under
# MSYS2/Git Bash generally accept but don't reliably manipulate (globs,
# string ops assuming '/' separators). Falls back to $LOCALAPPDATA as-is if
# cygpath isn't present, which works in practice under most Git Bash setups
# even though it's not guaranteed.
windows_local_app_dir() {
  if command -v cygpath >/dev/null 2>&1; then
    cygpath -u "${LOCALAPPDATA}"
  else
    echo "${LOCALAPPDATA}"
  fi
}

# Windows-style path (C:\...) for a POSIX path, for use in the PowerShell
# shortcut command below, which runs outside the MSYS2 path-translation
# layer. Falls back to the input unchanged if cygpath isn't present.
to_windows_path() {
  if command -v cygpath >/dev/null 2>&1; then
    cygpath -w "$1"
  else
    echo "$1"
  fi
}

# Best-effort Start Menu shortcut creation on Windows via PowerShell's
# WScript.Shell COM object — there's no plain-bash way to write a .lnk
# file. Silently does nothing (not a failure) if powershell.exe isn't on
# PATH, since a missing shortcut still leaves the app installed and
# launchable by running the .exe directly.
windows_create_shortcut() {
  target_exe="$1"
  if ! command -v powershell.exe >/dev/null 2>&1; then
    echo "note: powershell.exe not found — skipping Start Menu shortcut. The app is still installed at:"
    echo "  ${target_exe}"
    return 0
  fi
  start_menu="$(windows_local_app_dir)/../Roaming/Microsoft/Windows/Start Menu/Programs"
  mkdir -p "$start_menu"
  lnk_win="$(to_windows_path "${start_menu}/${APP_NAME}.lnk")"
  exe_win="$(to_windows_path "$target_exe")"
  powershell.exe -NoProfile -Command \
    "\$s = (New-Object -ComObject WScript.Shell).CreateShortcut('${lnk_win}'); \$s.TargetPath = '${exe_win}'; \$s.Save()" \
    >/dev/null 2>&1 \
    && echo "Created Start Menu shortcut: ${APP_NAME}" \
    || echo "note: failed to create Start Menu shortcut (app is still installed at ${target_exe})"
}

windows_remove_shortcut() {
  start_menu="$(windows_local_app_dir)/../Roaming/Microsoft/Windows/Start Menu/Programs"
  rm -f "${start_menu}/${APP_NAME}.lnk"
}

##############################################################################
# Tray helper autostart (login item), linux/windows — used by
# install.sh/uninstall.sh so the tray icon (cmd/agent-winglet-tray) is there
# from login onward rather than only after the dashboard has been opened
# once. darwin registers via SMAppService instead — see
# cmd/agent-winglet-app/loginitem_darwin.go and install.sh's darwin branch.
##############################################################################

linux_autostart_desktop_path() {
  echo "${HOME}/.config/autostart/winglet-tray.desktop"
}

linux_register_tray_autostart() {
  tray_path="$1"
  desktop="$(linux_autostart_desktop_path)"
  mkdir -p "$(dirname "$desktop")"
  cat > "$desktop" <<EOF
[Desktop Entry]
Type=Application
Name=${APP_NAME} Tray
Comment=Background menu-bar helper for ${APP_NAME}
Exec=${tray_path}
X-GNOME-Autostart-enabled=true
NoDisplay=true
EOF
}

linux_unregister_tray_autostart() {
  rm -f "$(linux_autostart_desktop_path)"
}

windows_startup_folder() {
  echo "$(windows_local_app_dir)/../Roaming/Microsoft/Windows/Start Menu/Programs/Startup"
}

# Same WScript.Shell COM approach as windows_create_shortcut, just targeting
# the Startup folder (which Windows auto-launches at login) instead of the
# regular Start Menu Programs folder.
windows_register_tray_autostart() {
  tray_path="$1"
  if ! command -v powershell.exe >/dev/null 2>&1; then
    echo "note: powershell.exe not found — skipping login-item shortcut. The tray helper is still installed at:"
    echo "  ${tray_path}"
    return 0
  fi
  startup="$(windows_startup_folder)"
  mkdir -p "$startup"
  lnk_win="$(to_windows_path "${startup}/${APP_NAME} Tray.lnk")"
  exe_win="$(to_windows_path "$tray_path")"
  powershell.exe -NoProfile -Command \
    "\$s = (New-Object -ComObject WScript.Shell).CreateShortcut('${lnk_win}'); \$s.TargetPath = '${exe_win}'; \$s.Save()" \
    >/dev/null 2>&1 \
    && echo "Registered ${APP_NAME} to start at login (Startup folder shortcut)" \
    || echo "note: failed to create the login-item shortcut (tray helper is still installed at ${tray_path})"
}

windows_unregister_tray_autostart() {
  startup="$(windows_startup_folder)"
  rm -f "${startup}/${APP_NAME} Tray.lnk"
}

# Best-effort: stops any currently-running tray helper instance. Used by
# install.sh before overwriting its binary — Windows in particular won't let
# you overwrite a running .exe — and by uninstall.sh so removing the
# autostart registration doesn't leave one running until next login. Never
# fatal: nothing running is the common case, not an error.
stop_tray() {
  if [ "$(detect_os)" = "windows" ]; then
    if command -v taskkill.exe >/dev/null 2>&1; then
      taskkill.exe /IM "${TRAY_BIN_NAME}.exe" /F >/dev/null 2>&1 || true
    fi
  else
    if pgrep -f "${TRAY_BIN_NAME}" >/dev/null 2>&1; then
      pkill -f "${TRAY_BIN_NAME}" || true
    fi
  fi
}
