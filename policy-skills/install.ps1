<#
.SYNOPSIS
  Install the r8e policy-skills fleet into a project's .claude/skills/ (Windows).

.DESCRIPTION
  Cross-platform companion of install.sh (Linux/macOS). Installs from this
  script's OWN directory's siblings, so it works the same whether run from a
  clone of r8e or from an extracted release tarball (the tarball bundles the
  scripts next to the skill dirs).

  On Windows, copying is the default because symbolic links require Developer
  Mode or an elevated shell. Use -Symlink to link instead (updatable in place).

.PARAMETER Dir
  Target skills directory. Default: .\.claude\skills

.PARAMETER Symlink
  Create symbolic links instead of copying (needs Developer Mode / admin).

.PARAMETER Force
  Replace a target even if it is a real directory (not a link).

.PARAMETER WithR8eRef
  Also install the r8e API-reference skill from <path> (the repo's claude-skill\
  dir) as .claude\skills\r8e.

.EXAMPLE
  .\install.ps1
.EXAMPLE
  .\install.ps1 -Dir C:\proj\.claude\skills -Symlink
#>
[CmdletBinding()]
param(
  [string]$Dir = (Join-Path (Get-Location) ".claude\skills"),
  [switch]$Symlink,
  [switch]$Force,
  [string]$WithR8eRef
)
$ErrorActionPreference = "Stop"
$ScriptDir = $PSScriptRoot

$Skills = @(
  "r8e-policy", "review-r8e-policy",
  "review-r8e-policy-call", "review-r8e-policy-timeouts", "review-r8e-policy-retry",
  "review-r8e-policy-overload", "review-r8e-policy-fallback", "review-r8e-policy-observability"
)

# Read the pinned versions from the single source of truth.
$versions = Get-Content (Join-Path $ScriptDir "VERSIONS.json") -Raw | ConvertFrom-Json
$skillVersion = $versions.skill_version
$module       = $versions.module
$r8eVersion   = $versions.targets.r8e

Write-Host "r8e policy-skills installer - skill v$skillVersion, pinned to $module $r8eVersion"
New-Item -ItemType Directory -Force -Path $Dir | Out-Null

function Install-One($name, $src) {
  $dst = Join-Path $Dir $name
  if (Test-Path $dst) {
    $item = Get-Item $dst -Force
    $isLink = $item.LinkType -ne $null
    if ($isLink -or $Force) {
      Remove-Item $dst -Recurse -Force
    } else {
      Write-Host "  skip  $name (a real directory exists; re-run with -Force to replace)"
      return
    }
  }
  if ($Symlink) {
    New-Item -ItemType SymbolicLink -Path $dst -Target $src | Out-Null
    Write-Host "  link  $name -> $src"
  } else {
    Copy-Item $src $dst -Recurse
    Write-Host "  copy  $name"
  }
}

foreach ($s in $Skills) {
  $src = Join-Path $ScriptDir $s
  if (-not (Test-Path $src)) { throw "MISSING source: $src" }
  Install-One $s $src
}

if ($WithR8eRef) {
  $refAbs = (Resolve-Path $WithR8eRef).Path
  Install-One "r8e" $refAbs
}

Write-Host ""
Write-Host "Done. Installed into: $Dir"
Write-Host "This skill release is calibrated for $module $r8eVersion. To use that exact version:"
Write-Host ""
Write-Host "  go get $module@$r8eVersion"
Write-Host ""
Write-Host 'Then ask Claude Code, e.g.:'
Write-Host '  "Use r8e-policy to write a policy for <service>"   (authoring, expert mode)'
Write-Host '  "Use review-r8e-policy on this policy"             (audit)'
