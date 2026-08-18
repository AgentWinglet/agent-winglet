Unicode true

####
## Please note: Template replacements don't work in this file. They are provided with default defines like
## mentioned underneath.
## If the keyword is not defined, "wails_tools.nsh" will populate them with the values from ProjectInfo.
## If they are defined here, "wails_tools.nsh" will not touch them. This allows to use this project.nsi manually
## from outside of Wails for debugging and development of the installer.
##
## For development first make a wails nsis build to populate the "wails_tools.nsh":
## > wails build --target windows/amd64 --nsis
## Then you can call makensis on this file with specifying the path to your binary:
## For a AMD64 only installer:
## > makensis -DARG_WAILS_AMD64_BINARY=..\..\bin\app.exe
## For a ARM64 only installer:
## > makensis -DARG_WAILS_ARM64_BINARY=..\..\bin\app.exe
## For a installer with both architectures:
## > makensis -DARG_WAILS_AMD64_BINARY=..\..\bin\app-amd64.exe -DARG_WAILS_ARM64_BINARY=..\..\bin\app-arm64.exe
##
## Winglet also always requires ARG_TRAY_AMD64_BINARY (path to the built
## agent-winglet-tray.exe — see the "Winglet:" block below and
## scripts/package/windows.sh), e.g.:
## > makensis -DARG_WAILS_AMD64_BINARY=..\..\bin\app.exe -DARG_TRAY_AMD64_BINARY=..\..\..\..\agent-winglet-tray\build\bin\agent-winglet-tray.exe
####
## The following information is taken from the ProjectInfo file, but they can be overwritten here.
####
## !define INFO_PROJECTNAME    "MyProject" # Default "{{.Name}}"
## !define INFO_COMPANYNAME    "MyCompany" # Default "{{.Info.CompanyName}}"
## !define INFO_PRODUCTNAME    "MyProduct" # Default "{{.Info.ProductName}}"
## !define INFO_PRODUCTVERSION "1.0.0"     # Default "{{.Info.ProductVersion}}"
## !define INFO_COPYRIGHT      "Copyright" # Default "{{.Info.Copyright}}"
###
## !define PRODUCT_EXECUTABLE  "Application.exe"      # Default "${INFO_PROJECTNAME}.exe"
## !define UNINST_KEY_NAME     "UninstKeyInRegistry"  # Default "${INFO_COMPANYNAME}${INFO_PRODUCTNAME}"
####
## !define REQUEST_EXECUTION_LEVEL "admin"            # Default "admin"  see also https://nsis.sourceforge.io/Docs/Chapter4.html
####
## Winglet: per-user install scope — the Windows deliverable is a per-user
## install, not machine-wide/admin, to match install.sh's existing
## %LOCALAPPDATA% install path and avoid an elevation prompt for a
## single-user desktop app. Defining these here before including
## wails_tools.nsh overrides its "admin" default.
####
!define WAILS_INSTALL_SCOPE "user"
!define REQUEST_EXECUTION_LEVEL "user"

####
## Winglet: the tray helper (cmd/agent-winglet-tray) binary to bundle
## alongside the dashboard — passed at makensis invocation time via
## -DARG_TRAY_AMD64_BINARY=path\to\agent-winglet-tray.exe (see
## scripts/package/windows.sh). Not a wails-generated define, so it isn't
## populated by wails_tools.nsh the way ARG_WAILS_AMD64_BINARY is.
####
!ifndef ARG_TRAY_AMD64_BINARY
    !error "Winglet: ARG_TRAY_AMD64_BINARY must be defined (path to the built agent-winglet-tray.exe) — see scripts/package/windows.sh"
!endif
!define TRAY_EXECUTABLE "agent-winglet-tray.exe"

####
## Include the wails tools
####
!include "wails_tools.nsh"

# The version information for this two must consist of 4 parts
VIProductVersion "${INFO_PRODUCTVERSION}.0"
VIFileVersion    "${INFO_PRODUCTVERSION}.0"

VIAddVersionKey "CompanyName"     "${INFO_COMPANYNAME}"
VIAddVersionKey "FileDescription" "${INFO_PRODUCTNAME} Installer"
VIAddVersionKey "ProductVersion"  "${INFO_PRODUCTVERSION}"
VIAddVersionKey "FileVersion"     "${INFO_PRODUCTVERSION}"
VIAddVersionKey "LegalCopyright"  "${INFO_COPYRIGHT}"
VIAddVersionKey "ProductName"     "${INFO_PRODUCTNAME}"

# Enable HiDPI support. https://nsis.sourceforge.io/Reference/ManifestDPIAware
ManifestDPIAware true

!include "MUI.nsh"

!define MUI_ICON "..\icon.ico"
!define MUI_UNICON "..\icon.ico"
# !define MUI_WELCOMEFINISHPAGE_BITMAP "resources\leftimage.bmp" #Include this to add a bitmap on the left side of the Welcome Page. Must be a size of 164x314
!define MUI_FINISHPAGE_NOAUTOCLOSE # Wait on the INSTFILES page so the user can take a look into the details of the installation steps
!define MUI_ABORTWARNING # This will warn the user if they exit from the installer.

!insertmacro MUI_PAGE_WELCOME # Welcome to the installer page.
# !insertmacro MUI_PAGE_LICENSE "resources\eula.txt" # Adds a EULA page to the installer
!insertmacro MUI_PAGE_DIRECTORY # In which folder install page.
!insertmacro MUI_PAGE_INSTFILES # Installing page.
!insertmacro MUI_PAGE_FINISH # Finished installation page.

!insertmacro MUI_UNPAGE_INSTFILES # Uinstalling page

!insertmacro MUI_LANGUAGE "English" # Set the Language of the installer

## The following two statements can be used to sign the installer and the uninstaller. The path to the binaries are provided in %1
#!uninstfinalize 'signtool --file "%1"'
#!finalize 'signtool --file "%1"'

Name "${INFO_PRODUCTNAME}"
OutFile "..\..\bin\${INFO_PROJECTNAME}-${ARCH}-installer.exe" # Name of the installer's file.
!ifdef WAILS_INSTALL_SCOPE
  !if "${WAILS_INSTALL_SCOPE}" == "user"
    InstallDir "$LOCALAPPDATA\Programs\${INFO_PRODUCTNAME}"
  !else
    InstallDir "$PROGRAMFILES64\${INFO_COMPANYNAME}\${INFO_PRODUCTNAME}"
  !endif
!else
  InstallDir "$PROGRAMFILES64\${INFO_COMPANYNAME}\${INFO_PRODUCTNAME}"
!endif # Default installing folder ($PROGRAMFILES is Program Files folder).
ShowInstDetails show # This will always show the installation details.

Function .onInit
   !insertmacro wails.checkArchitecture
FunctionEnd

Section
    !insertmacro wails.setShellContext

    !insertmacro wails.webview2runtime

    SetOutPath $INSTDIR

    !insertmacro wails.files

    ; Tray helper — installed alongside the dashboard so
    ; cmd/agent-winglet-app/tray_path.go's sibling-of-executable lookup
    ; finds it, started at login via a Startup-folder shortcut (mirrors
    ; scripts/lib.sh's windows_register_tray_autostart, reimplemented here
    ; so the installer doesn't depend on Git Bash/PowerShell scripts from a
    ; checkout).
    File "/oname=${TRAY_EXECUTABLE}" "${ARG_TRAY_AMD64_BINARY}"
    CreateDirectory "$SMSTARTUP"
    CreateShortcut "$SMSTARTUP\${INFO_PRODUCTNAME} Tray.lnk" "$INSTDIR\${TRAY_EXECUTABLE}"

    CreateShortcut "$SMPROGRAMS\${INFO_PRODUCTNAME}.lnk" "$INSTDIR\${PRODUCT_EXECUTABLE}"
    CreateShortCut "$DESKTOP\${INFO_PRODUCTNAME}.lnk" "$INSTDIR\${PRODUCT_EXECUTABLE}"

    !insertmacro wails.associateFiles
    !insertmacro wails.associateCustomProtocols

    !insertmacro wails.writeUninstaller

    ; Launch the tray helper now so the icon appears right away instead of
    ; only at the next login — best-effort and non-blocking (ExecShell, not
    ; ExecWait: the installer shouldn't wait on a background helper, and a
    ; failure here isn't fatal to the install).
    ExecShell "" "$INSTDIR\${TRAY_EXECUTABLE}"
SectionEnd

Section "uninstall"
    !insertmacro wails.setShellContext

    ; Stop any running tray helper before removing files — Windows won't
    ; let a running .exe be deleted, and NSIS runs the previous version's
    ; uninstaller before installing a new one (same UNINST_KEY), so this
    ; also covers the upgrade case.
    ExecWait 'taskkill /IM "${TRAY_EXECUTABLE}" /F'

    RMDir /r "$AppData\${PRODUCT_EXECUTABLE}" # Remove the WebView2 DataPath

    RMDir /r $INSTDIR

    Delete "$SMPROGRAMS\${INFO_PRODUCTNAME}.lnk"
    Delete "$DESKTOP\${INFO_PRODUCTNAME}.lnk"
    Delete "$SMSTARTUP\${INFO_PRODUCTNAME} Tray.lnk"

    !insertmacro wails.unassociateFiles
    !insertmacro wails.unassociateCustomProtocols

    !insertmacro wails.deleteUninstaller
SectionEnd
