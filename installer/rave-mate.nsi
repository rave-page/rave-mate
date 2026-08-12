; rave-mate Windows installer - per-user (no admin), user-selectable dir.
; Built on Linux CI via makensis. Defines from CI (all optional, defaulted below):
;   APP_VERSION APP_BUILD VERSION_4 SRC_EXE OUT_FILE [APP_ICON] [SPOUT_DLL] [OPENVR_DLL]
;   [ENC_EXE] [SHELL_EXE]: sidecar exes bundled beside the main exe - the MF encoder child and the
;   Zig window child. SHELL_EXE is the DEFAULT window host; omitting it ships an install that
;   silently uses the in-process Go window instead.
; SPOUT_DLL / OPENVR_DLL (optional): paths to SpoutLibrary.dll / openvr_api.dll - bundled beside
; the exe. Both are runtime-loaded (LoadLibrary), so a missing DLL only disables that feature;
; we ship them so the feature works out of the box. Omit either to skip bundling it.
; The in-app updater does NOT run this - it self-swaps the exe. This is for first-time
; installs + the web download. A manual re-run (or /S) reuses the remembered InstallDir.

Unicode true
SetCompressor /SOLID lzma

!include "MUI2.nsh"
!include "LogicLib.nsh"

!ifndef APP_VERSION
  !define APP_VERSION "dev"
!endif
!ifndef APP_BUILD
  !define APP_BUILD "0"
!endif
!ifndef VERSION_4
  !define VERSION_4 "0.0.0.0"
!endif
!ifndef SRC_EXE
  !define SRC_EXE "dist/rave-mate.exe"
!endif
!ifndef OUT_FILE
  !define OUT_FILE "dist/mate/rave-mate-setup.exe"
!endif

!define APP_NAME  "rave-mate"
!define PUBLISHER "rave.page"
!define EXE_NAME  "rave-mate.exe"
!define UNINST_KEY "Software\Microsoft\Windows\CurrentVersion\Uninstall\${APP_NAME}"

Name "${APP_NAME}"
OutFile "${OUT_FILE}"
RequestExecutionLevel user                                  ; per-user, no UAC
InstallDir "$LOCALAPPDATA\Programs\${APP_NAME}"             ; sane user-level default
InstallDirRegKey HKCU "Software\${APP_NAME}" "InstallDir"  ; re-runs reuse chosen dir
ShowInstDetails show
ShowUninstDetails show
BrandingText "${APP_NAME} ${APP_VERSION}"

VIProductVersion "${VERSION_4}"
VIAddVersionKey "ProductName"    "${APP_NAME}"
VIAddVersionKey "CompanyName"    "${PUBLISHER}"
VIAddVersionKey "FileDescription" "${APP_NAME} installer"
VIAddVersionKey "FileVersion"    "${APP_VERSION}"
VIAddVersionKey "ProductVersion" "${APP_VERSION}"
VIAddVersionKey "LegalCopyright" "(c) ${PUBLISHER}"

!ifdef APP_ICON
  !define MUI_ICON   "${APP_ICON}"
  !define MUI_UNICON "${APP_ICON}"
!endif

!define MUI_ABORTWARNING
!define MUI_FINISHPAGE_RUN      "$INSTDIR\${EXE_NAME}"
!define MUI_FINISHPAGE_RUN_TEXT "Launch ${APP_NAME}"

!insertmacro MUI_PAGE_WELCOME
!insertmacro MUI_PAGE_DIRECTORY
!insertmacro MUI_PAGE_INSTFILES
!insertmacro MUI_PAGE_FINISH

!insertmacro MUI_UNPAGE_CONFIRM
!insertmacro MUI_UNPAGE_INSTFILES

!insertmacro MUI_LANGUAGE "English"

Section "Install"
  ${If} ${Silent}
    Sleep 2000                       ; if invoked as an update, let the running app exit
  ${EndIf}

  SetOutPath "$INSTDIR"
  File "/oname=${EXE_NAME}" "${SRC_EXE}"
  !ifdef SPOUT_DLL
    File "/oname=SpoutLibrary.dll" "${SPOUT_DLL}"   ; runtime-loaded by the spout backend
  !endif
  !ifdef OPENVR_DLL
    File "/oname=openvr_api.dll" "${OPENVR_DLL}"    ; runtime-loaded by the vr backend
  !endif
  !ifdef ENC_EXE
    File "/oname=rave-mate-enc.exe" "${ENC_EXE}"    ; per-adapter MF encoder child (zigenc)
  !endif
  !ifdef SHELL_EXE
    ; Zig window child (zigui). The DEFAULT window host - it must land beside the exe, which is
    ; where webui.resolveZigShellExe looks; absent, the app degrades to the in-process Go window.
    File "/oname=rave-shell.exe" "${SHELL_EXE}"
  !endif
  !ifdef FREI0R_DIR
    ; bundled frei0r plugin DLLs (GPL-2+, built from pinned source - scripts/build-frei0r.sh;
    ; COPYING + SOURCE.txt ride along). vfx.PluginDirs scans $INSTDIR\frei0r at runtime.
    SetOutPath "$INSTDIR\frei0r"
    File /r "${FREI0R_DIR}\*"
    SetOutPath "$INSTDIR"
  !endif

  CreateShortCut "$SMPROGRAMS\${APP_NAME}.lnk" "$INSTDIR\${EXE_NAME}"
  WriteUninstaller "$INSTDIR\uninstall.exe"

  WriteRegStr   HKCU "Software\${APP_NAME}" "InstallDir" "$INSTDIR"
  WriteRegStr   HKCU "${UNINST_KEY}" "DisplayName"     "${APP_NAME}"
  WriteRegStr   HKCU "${UNINST_KEY}" "DisplayVersion"  "${APP_VERSION}"
  WriteRegStr   HKCU "${UNINST_KEY}" "Publisher"       "${PUBLISHER}"
  WriteRegStr   HKCU "${UNINST_KEY}" "DisplayIcon"     "$INSTDIR\${EXE_NAME}"
  WriteRegStr   HKCU "${UNINST_KEY}" "UninstallString" "$INSTDIR\uninstall.exe"
  WriteRegStr   HKCU "${UNINST_KEY}" "InstallLocation" "$INSTDIR"
  WriteRegDWORD HKCU "${UNINST_KEY}" "NoModify" 1
  WriteRegDWORD HKCU "${UNINST_KEY}" "NoRepair" 1

  ${If} ${Silent}
    Exec '"$INSTDIR\${EXE_NAME}"'     ; relaunch after a silent update
  ${EndIf}
SectionEnd

Section "Uninstall"
  Delete "$INSTDIR\${EXE_NAME}"
  Delete "$INSTDIR\SpoutLibrary.dll"
  Delete "$INSTDIR\openvr_api.dll"
  Delete "$INSTDIR\rave-mate-enc.exe"
  Delete "$INSTDIR\rave-shell.exe"
  RMDir /r "$INSTDIR\frei0r"
  Delete "$INSTDIR\uninstall.exe"
  Delete "$SMPROGRAMS\${APP_NAME}.lnk"
  RMDir  "$INSTDIR"
  DeleteRegKey HKCU "${UNINST_KEY}"
  DeleteRegKey HKCU "Software\${APP_NAME}"
SectionEnd
