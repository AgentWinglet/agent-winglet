# Sourced by install.sh and uninstall.sh — shared OS detection and the
# per-OS conventions for where agent-winglet-app's built artifact and
# installed copy live, so both scripts agree without duplicating the logic.
# Not meant to be run directly.

APP_NAME="Winglet"

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
