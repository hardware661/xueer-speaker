param([string]$OutputDirectory = '')
# Builds XueerSpeaker.exe (the tray app) with Windows' own .NET Framework C#
# compiler, as Husic's desktop/build.ps1 does. The WebView2 SDK is fetched
# by fetch-sdk.py into vendor/ (pinned in packages.lock.json).
$ErrorActionPreference = 'Stop'
[Console]::OutputEncoding = [System.Text.UTF8Encoding]::new($false)
$root = $PSScriptRoot
$lock = (Get-Content (Join-Path $root 'packages.lock.json') -Raw | ConvertFrom-Json).'Microsoft.Web.WebView2'
$vendor = Join-Path $root ('vendor\webview2-' + $lock.version)
$archive = Join-Path $root ('vendor\microsoft.web.webview2.' + $lock.version + '.nupkg')
if (-not (Test-Path $archive)) { throw 'Run fetch-sdk.py first.' }
if ((Get-FileHash $archive -Algorithm SHA256).Hash.ToLowerInvariant() -ne $lock.sha256) { throw 'SDK checksum mismatch' }
$csc = Join-Path $env:WINDIR 'Microsoft.NET\Framework64\v4.0.30319\csc.exe'
$out = if ($OutputDirectory) { $OutputDirectory } else { Join-Path $root 'dist' }
New-Item -ItemType Directory -Path $out -Force | Out-Null
$exe = Join-Path $out 'XueerSpeaker.exe'
$core = Join-Path $vendor 'lib\net462\Microsoft.Web.WebView2.Core.dll'
$forms = Join-Path $vendor 'lib\net462\Microsoft.Web.WebView2.WinForms.dll'
$loader = Join-Path $vendor 'build\native\x64\WebView2Loader.dll'
$cscArgs = @('/nologo', '/target:winexe', '/platform:x64', '/optimize+', '/utf8output', '/codepage:65001', '/langversion:5',
    ('/out:' + $exe), ('/win32manifest:' + (Join-Path $root 'app.manifest')),
    ('/win32icon:' + (Join-Path $root 'speaker.ico')), ('/resource:' + (Join-Path $root 'speaker.ico') + ',Xueer.App.ico'),
    '/reference:System.dll', '/reference:System.Core.dll', '/reference:System.Drawing.dll',
    '/reference:System.Windows.Forms.dll', '/reference:System.Web.Extensions.dll',
    ('/reference:' + $core), ('/reference:' + $forms),
    ('/resource:' + $core + ',Xueer.SDK.Microsoft.Web.WebView2.Core.dll'),
    ('/resource:' + $forms + ',Xueer.SDK.Microsoft.Web.WebView2.WinForms.dll'),
    ('/resource:' + $loader + ',Xueer.SDK.WebView2Loader.dll'))
$cscArgs += @(Get-ChildItem $root -Filter '*.cs' | ForEach-Object { $_.FullName })
& $csc $cscArgs
if ($LASTEXITCODE -ne 0) { throw ('C# build failed: ' + $LASTEXITCODE) }
[ordered]@{ artifact = 'XueerSpeaker.exe'; bytes = (Get-Item $exe).Length } | ConvertTo-Json -Compress
