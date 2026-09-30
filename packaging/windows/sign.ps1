# ==============================================================================
# Authenticode signing for the Windows installers and binaries.
# ==============================================================================
# Signs with signtool.exe from the Windows SDK, which ships with Visual Studio
# and with the Build Tools. It is not part of the base Windows image, so the
# script locates it rather than assuming it is on PATH.
#
# This is deliberately a separate script from build.ps1 and build-server.ps1.
# Signing needs a certificate, and a certificate is a credential: it must never
# be reachable from an ordinary build. A build script that signs when a
# certificate happens to be present makes "did this get signed?" a property of
# whoever ran it, which is exactly the property that has to be fixed. Here the
# answer is explicit: sign, or do not.
#
# Two certificate kinds, and they are not interchangeable:
#
#   - A code-signing certificate from a public CA (DigiCert, Sectigo, GlobalSign)
#     is what makes SmartScreen and enterprise WDAC policy accept the file
#     without a prior trust decision. It requires an organisation, a phone, and
#     days of issuance. This is the only kind suitable for public distribution.
#
#   - A self-signed certificate works for an internal fleet that has the
#     publisher installed in each machine's Trusted Publishers store. It does
#     nothing on any machine that has not been told to trust it, and it is not
#     sufficient for public distribution.
#
# The publisher name is baked into the certificate and is what the operator
# sees in the file properties. It cannot be chosen at signing time.
#
# Usage:
#
#   .\sign.ps1 -Files EndpointAgent-Setup.exe,agent-windows-amd64.exe `
#              -CertPath C:\certs\publisher.pfx -CertPassword $env:SIGNING_PW `
#              -TimestampUrl http://timestamp.digicert.com
#
#   -Sha1Only   use SHA-1. Required only by pre-2016 Windows on an internal
#               fleet, and it is a downgrade: SHA-256 is what every supported
#               Windows accepts.
#   -Verify     sign nothing; verify the given files are already validly signed.
#
# In CI the certificate arrives as the WINDOWS_CERTIFICATE (base64 .pfx) and
# WINDOWS_CERTIFICATE_PASSWORD repository secrets. Both must be present or the
# release workflow refuses to publish: an unsigned artifact must never be
# published by accident.

param(
    [Parameter(Mandatory = $true)]
    [string[]]$Files,

    [string]$CertPath,
    [string]$CertPassword,
    [string]$CertThumbprint,

    # Omitting the timestamp URL is the one thing that quietly ruins a signed
    # build: without it the signature is valid only while the certificate is,
    # and when it expires every copy already in the fleet starts warning.
    # SmartScreen also treats an expired signature as unsigned.
    [string]$TimestampUrl = "http://timestamp.digicert.com",

    [switch]$Sha1Only,
    [switch]$Verify
)

$ErrorActionPreference = "Stop"

function Find-SignTool {
    $cmd = Get-Command signtool.exe -ErrorAction SilentlyContinue
    if ($cmd) { return $cmd.Source }

    $roots = @(
        "${env:ProgramFiles(x86)}\Windows Kits\10\bin",
        "$env:ProgramFiles\Windows Kits\10\bin"
    )
    foreach ($root in $roots) {
        if (-not (Test-Path $root)) { continue }
        # The bin tree is versioned: .../bin/10.0.22621.0/x64/signtool.exe.
        # Newest first, so a machine with two SDKs uses the current one.
        $found = Get-ChildItem -Path $root -Filter signtool.exe -Recurse -ErrorAction SilentlyContinue |
            Where-Object { $_.Directory.Name -in @("x64", "amd64") } |
            Sort-Object FullName -Descending
        if ($found) { return $found[0].FullName }
    }
    return $null
}

function Assert-FileExists {
    foreach ($f in $Files) {
        if (-not (Test-Path $f)) {
            throw "not found: $f"
        }
    }
}

$SignTool = Find-SignTool
if (-not $SignTool) {
    throw @"
signtool.exe not found.

It ships with the Windows SDK, not with Windows. Install either:
  - Visual Studio Build Tools (workload: Desktop development with C++)
  - The standalone Windows SDK

The script re-runs once it is installed; there is nothing to configure.
"@
}

Write-Host "Using $SignTool" -ForegroundColor DarkGray
Assert-FileExists

if ($Verify) {
    $bad = 0
    foreach ($f in $Files) {
        Write-Host "Verifying $f ..."
        & $SignTool verify /pa /v $f
        if ($LASTEXITCODE -ne 0) {
            Write-Host "  NOT VALIDLY SIGNED: $f" -ForegroundColor Red
            $bad++
        } else {
            Write-Host "  ok" -ForegroundColor Green
        }
    }
    if ($bad -gt 0) { throw "$bad of $($Files.Count) file(s) are not validly signed" }
    exit 0
}

# Prefer the certificate file; fall back to a thumbprint already in the store,
# which is how a hardware token (YubiKey, HSM) is used without exporting it.
if (-not $CertPath -and -not $CertThumbprint) {
    throw "supply either -CertPath (a .pfx) or -CertThumbprint (already in the certificate store)"
}
if ($CertPath -and $CertThumbprint) {
    throw "supply -CertPath or -CertThumbprint, not both"
}

$alg = if ($Sha1Only) { "/sha1" } else { "/sha256" }

if ($CertPath) {
    $signArgs = @("sign", $alg, "/fd", $alg.TrimStart("/"), "/tr", $TimestampUrl,
        "/f", $CertPath, "/p", $CertPassword)
} else {
    # /sm is required when the certificate lives in the machine store, which is
    # what makes it usable with an HSM. Without a thumbprint on the command
    # line, signtool picks a certificate by its own rules and may pick the wrong
    # one.
    $signArgs = @("sign", $alg, "/fd", $alg.TrimStart("/"), "/tr", $TimestampUrl,
        "/n", "/sha1", $CertThumbprint)
}

# $LASTEXITCODE is the only reliable signal: signtool returns 0 for a success,
# non-zero for both a real failure and a refusal to overwrite.
foreach ($f in $Files) {
    Write-Host "Signing $f ..."
    & $SignTool @signArgs $f
    if ($LASTEXITCODE -ne 0) {
        throw "signtool failed on ${f} with exit code $LASTEXITCODE"
    }

    # Verify immediately rather than trusting the exit code alone. A signature
    # can be produced and still be invalid: a timestamp server that is
    # unreachable, or an algorithm the target OS will not accept, both exit 0
    # on the signing step and fail here.
    & $SignTool verify /pa /v $f
    if ($LASTEXITCODE -ne 0) {
        throw "signed ${f} but it does not verify; do not ship it"
    }
    Write-Host "  signed and verified" -ForegroundColor Green
}

Write-Host "All $($Files.Count) file(s) signed and verified." -ForegroundColor Green
