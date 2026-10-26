# Installs the stagd CLI on Windows from its GitHub releases:
#
#   irm https://stagd.dev/install.ps1 | iex
#
# $env:STAGD_VERSION picks a release, e.g. v0.4.0 (default: the latest), and
# $env:STAGD_INSTALL_DIR where it goes (default: %LOCALAPPDATA%\Programs\stagd),
# which is added to your PATH. It checks the download against the release's
# checksums before installing. Windows on amd64 and arm64; Windows
# PowerShell 5.1 or PowerShell 7.
#
# Everything is in Install-Stagd, called on the last line: a download cut
# short runs nothing.

function Install-Stagd {
  $ErrorActionPreference = 'Stop'

  # Invoke-WebRequest is far slower with its progress bar.
  $ProgressPreference = 'SilentlyContinue'

  # Windows PowerShell 5.1 may not offer TLS 1.2 by default; GitHub needs it.
  [Net.ServicePointManager]::SecurityProtocol = [Net.ServicePointManager]::SecurityProtocol -bor [Net.SecurityProtocolType]::Tls12

  $releases = if ($env:STAGD_RELEASES_URL) { $env:STAGD_RELEASES_URL } else { 'https://github.com/stagdhq/stagd/releases' }
  $version = if ($env:STAGD_VERSION) { $env:STAGD_VERSION } else { 'latest' }
  $dir = if ($env:STAGD_INSTALL_DIR) { $env:STAGD_INSTALL_DIR } else { Join-Path $env:LOCALAPPDATA 'Programs\stagd' }

  # 32-bit PowerShell on 64-bit Windows says x86 here, and the real one in PROCESSOR_ARCHITEW6432.
  $machine = if ($env:PROCESSOR_ARCHITEW6432) { $env:PROCESSOR_ARCHITEW6432 } else { $env:PROCESSOR_ARCHITECTURE }

  $arch = switch ($machine) {
    'AMD64' { 'amd64' }
    'ARM64' { 'arm64' }
    default { throw "stagd install: no build for $machine Windows: amd64 and arm64 only" }
  }

  $base = if ($version -eq 'latest') { "$releases/latest/download" } else { "$releases/download/$version" }
  $asset = "stagd_windows_$arch.zip"
  $tmp = Join-Path ([IO.Path]::GetTempPath()) ('stagd-' + [Guid]::NewGuid())

  New-Item -ItemType Directory -Path $tmp | Out-Null

  try {
    Write-Host "Downloading stagd ($version, windows/$arch)..."

    Invoke-WebRequest -UseBasicParsing -Uri "$base/$asset" -OutFile (Join-Path $tmp $asset)
    Invoke-WebRequest -UseBasicParsing -Uri "$base/checksums.txt" -OutFile (Join-Path $tmp 'checksums.txt')

    # The archive's checksum, from the release's list.
    $want = $null

    foreach ($line in Get-Content (Join-Path $tmp 'checksums.txt')) {
      $fields = $line -split '\s+', 2

      if ($fields.Count -eq 2 -and $fields[1].TrimStart('*') -eq $asset) {
        $want = $fields[0].ToLower()
      }
    }

    if (-not $want) {
      throw "stagd install: $asset isn't in the release's checksums"
    }

    $got = (Get-FileHash -Algorithm SHA256 -Path (Join-Path $tmp $asset)).Hash.ToLower()

    if ($got -ne $want) {
      throw "stagd install: the download doesn't match its checksum: not installing it"
    }

    Expand-Archive -Path (Join-Path $tmp $asset) -DestinationPath (Join-Path $tmp 'out') -Force
    New-Item -ItemType Directory -Force -Path $dir | Out-Null

    # A running stagd.exe can't be overwritten, but it can be moved aside.
    $exe = Join-Path $dir 'stagd.exe'

    Remove-Item -Force "$exe.old" -ErrorAction SilentlyContinue

    if (Test-Path $exe) {
      Move-Item -Force $exe "$exe.old"
    }

    Copy-Item -Force (Join-Path $tmp 'out\stagd.exe') $exe
    Remove-Item -Force "$exe.old" -ErrorAction SilentlyContinue
  }
  finally {
    Remove-Item -Recurse -Force $tmp -ErrorAction SilentlyContinue
  }

  # On the user's PATH, for new terminals; and this one's, for now.
  $userPath = [Environment]::GetEnvironmentVariable('Path', 'User')

  if (($userPath -split ';') -notcontains $dir) {
    $newPath = if ($userPath) { "$userPath;$dir" } else { $dir }

    [Environment]::SetEnvironmentVariable('Path', $newPath, 'User')
    Write-Host "Added $dir to your PATH."
  }

  if (($env:Path -split ';') -notcontains $dir) {
    $env:Path = "$env:Path;$dir"
  }

  $installed = & $exe --version

  Write-Host "Installed $installed to $exe"
  Write-Host 'Next: stagd login'
}

Install-Stagd
