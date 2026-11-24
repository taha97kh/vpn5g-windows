; TAHA VPN 5G — Windows installer (Inno Setup 6, Persian).
;
; Built by CI (see .github/workflows/build-windows.yml, job "installer"):
;   1. the "build" job produces VPN5G-Windows.zip (vpn5g.exe + xray.exe +
;      README-FA.md);
;   2. the "installer" job builds nmhost.exe and stages everything under
;      installer/staging/;
;   3. ISCC compiles this script into "TAHA VPN 5G Setup.exe".
;
; Install is per-user (PrivilegesRequired=lowest, no admin / no UAC):
; files go to %LOCALAPPDATA%\Programs\TAHA VPN 5G, shortcuts to the
; per-user Desktop + Start Menu, and the Chrome Native Messaging host is
; registered under HKCU. The uninstaller quits the running app first
; (POST /api/quit, then taskkill as a fallback) so no tunnel is left
; behind, and removes the Native Messaging registration.
;
; The Native Messaging host manifest (com.taha97kh.vpn5g.json) is written
; by the Pascal code below at install time because it must contain the
; ABSOLUTE path of nmhost.exe. Its allowed_origins lists the extension's
; fixed id (derived from the extension's public key in manifest.json);
; if the extension key ever changes, ExtensionID here must change too.

#define AppVersion "2.1.0"
#define AppName "TAHA VPN 5G"
#define ExtensionID "piknplkcmlhcmpngpjobbcflldoeailb"

[Setup]
AppId={{8F3A2B1C-7D4E-4A9F-8C2D-1E5F6A7B8C9D}
AppName={#AppName}
AppVersion={#AppVersion}
AppVerName={#AppName} {#AppVersion}
AppPublisher=AREF KH
AppPublisherURL=https://t.me/VPN5G_IRBot
AppSupportURL=https://t.me/VPN5G_IRBot?start=support
DefaultDirName={autopf}\{#AppName}
DefaultGroupName={#AppName}
DisableProgramGroupPage=yes
PrivilegesRequired=lowest
PrivilegesRequiredOverridesAllowed=dialog
ArchitecturesAllowed=x64
MinVersion=10.0
OutputDir=output
OutputBaseFilename=TAHA VPN 5G Setup
SetupIconFile=..\icon.ico
UninstallDisplayIcon={app}\vpn5g.exe
WizardStyle=modern
Compression=lzma2
SolidCompression=yes
; Refuse to install over a running copy: the app owns a named mutex
; (same name as the app's single-instance guard); setup tells the user
; to close the app first instead of failing on locked files.
AppMutex=TAHA VPN 5G Single Instance
VersionInfoVersion={#AppVersion}
VersionInfoDescription=TAHA VPN 5G — نصب‌کننده ویندوز
VersionInfoProductName={#AppName}

[Languages]
; Inno Setup ships no Persian language file, so the wizard uses the
; English base and the [Messages] section below overrides the visible
; strings in Persian. (Do NOT point at Languages\Persian.isl — it does
; not exist in Inno Setup 6 and the compile fails.)
Name: "en"; MessagesFile: "compiler:Default.isl"

; Persian wizard strings (this file must stay UTF-8 with BOM).
[Messages]
SetupAppTitle=نصب — %1
SetupWindowTitle=نصب — %1
UninstallAppTitle=حذف — %1
UninstallWindowTitle=حذف — %1
WelcomeLabel1=به جادوگر نصب [name] خوش آمدید
WelcomeLabel2=این برنامه [name] را روی رایانه‌ی شما نصب می‌کند.%n%nتوصیه می‌شود پیش از ادامه، همه‌ی برنامه‌های دیگر را ببندید.
ClickNext=برای ادامه «بعدی» را بزنید؛ برای بازگشت «بازگشت» را بزنید.
ClickInstall=برای ادامه‌ی نصب «نصب» را بزنید؛ برای بازبینی یا تغییر تنظیمات «بازگشت» را بزنید.
ClickFinish=برای خروج از نصب «پایان» را بزنید.
FinishedHeadingLabel=تکمیل جادوگر نصب [name]
FinishedLabel=نصب [name] روی رایانه‌ی شما کامل شد.
SelectDirTitle=انتخاب محل نصب
SelectDirLabel3=برنامه‌ی [name] در پوشه‌ی زیر نصب می‌شود.%n%nبرای ادامه «بعدی» را بزنید. برای انتخاب پوشه‌ای دیگر «مرور» را بزنید.
SelectDirBrowseLabel=برای ادامه «بعدی» را بزنید. برای انتخاب پوشه‌ای دیگر «مرور» را بزنید.
DiskSpaceMBLabel=حداقل [mb] مگابایت فضای خالی لازم است.
ReadyLabel1=آماده‌ی نصب
ButtonBack=بازگشت
ButtonNext=بعدی
ButtonInstall=نصب
ButtonFinish=پایان
ButtonCancel=لغو
ButtonBrowse=مرور
ButtonYes=بله
ButtonNo=خیر
ButtonOK=تأیید
ConfirmUninstall=آیا مطمئن هستید که می‌خواهید %1 و همه‌ی اجزای آن را کاملاً حذف کنید؟
UninstallStatusLabel=لطفاً صبر کنید تا برنامه‌ی حذف، %1 را از رایانه‌ی شما حذف کند.
StatusExtractFiles=در حال استخراج فایل‌ها…

[CustomMessages]
DesktopIcon=ایجاد میانبر روی دسکتاپ
AdditionalIcons=آیکون‌های اضافی

[Tasks]
Name: "desktopicon"; Description: "{cm:DesktopIcon}"; GroupDescription: "{cm:AdditionalIcons}"; Flags: checkedonce

[Files]
Source: "staging\vpn5g.exe"; DestDir: "{app}"; Flags: ignoreversion
Source: "staging\xray.exe"; DestDir: "{app}"; Flags: ignoreversion
Source: "staging\nmhost.exe"; DestDir: "{app}"; Flags: ignoreversion
Source: "staging\README-FA.md"; DestDir: "{app}"; Flags: ignoreversion

[Icons]
Name: "{autoprograms}\{#AppName}\{#AppName}"; Filename: "{app}\vpn5g.exe"
Name: "{autodesktop}\{#AppName}"; Filename: "{app}\vpn5g.exe"; Tasks: desktopicon
Name: "{autoprograms}\{#AppName}\حذف {#AppName}"; Filename: "{uninstallexe}"

[Registry]
; Chrome Native Messaging host registration (per-user). The value is the
; absolute path of the host manifest, written by the Pascal code below.
Root: HKCU; Subkey: "Software\Google\Chrome\NativeMessagingHosts\com.taha97kh.vpn5g"; ValueType: string; ValueName: ""; ValueData: "{app}\com.taha97kh.vpn5g.json"; Flags: uninsdeletekey

[Run]
Filename: "{app}\vpn5g.exe"; Description: "اجرای {#AppName}"; Flags: nowait postinstall skipifsilent

[UninstallRun]
; 1) Ask the running app to disconnect the tunnel and exit cleanly
;    (loopback-only endpoint; fails silently when the app is not running).
Filename: "{sys}\curl.exe"; Parameters: "-s -m 5 -X POST http://127.0.0.1:17890/api/quit"; Flags: runhidden
; 2) Fallbacks: make sure no tunnel process survives the uninstall.
Filename: "{sys}\taskkill.exe"; Parameters: "/F /IM vpn5g.exe"; Flags: runhidden
Filename: "{sys}\taskkill.exe"; Parameters: "/F /IM xray.exe"; Flags: runhidden

[UninstallDelete]
Type: files; Name: "{app}\com.taha97kh.vpn5g.json"

[Code]
const
  NMHostName = 'com.taha97kh.vpn5g';
  ExtensionID = '{#ExtensionID}';

{ Write the Native Messaging host manifest. It must contain the absolute
  path of nmhost.exe, which is only known at install time (the app
  folder). The description stays in English on purpose: the manifest
  is not user-visible UI, and ASCII avoids any encoding risk. }
procedure WriteNMHostManifest();
var
  ManifestPath, ExePath, Json: String;
begin
  ManifestPath := ExpandConstant('{app}\') + NMHostName + '.json';
  ExePath := ExpandConstant('{app}\nmhost.exe');
  StringChangeEx(ExePath, '\', '\\', True);
  Json := '{' + #13#10 +
    '  "name": "' + NMHostName + '",' + #13#10 +
    '  "description": "TAHA VPN 5G native messaging host",' + #13#10 +
    '  "path": "' + ExePath + '",' + #13#10 +
    '  "type": "stdio",' + #13#10 +
    '  "allowed_origins": ["chrome-extension://' + ExtensionID + '/"]' + #13#10 +
    '}';
  SaveStringToFile(ManifestPath, Json, False);
end;

procedure CurStepChanged(CurStep: TSetupStep);
begin
  if CurStep = ssPostInstall then
    WriteNMHostManifest();
end;
