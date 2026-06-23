#!/bin/sh
# install.sh — install the r8e policy-skills fleet into a project's .claude/skills/
#
# Cross-platform companion: install.ps1 (Windows PowerShell). This POSIX script
# covers Linux and macOS.
#
# It installs from its OWN directory's siblings, so it works identically whether
# you run it from a clone of r8e (policy-skills/install.sh) or from an extracted
# release tarball (the tarball bundles the scripts next to the skill dirs).
#
# Usage:
#   ./install.sh [--dir <target>] [--copy] [--force] [--with-r8e-ref <path>] [--help]
#
#   --dir <target>        Target skills dir (default: ./.claude/skills)
#   --copy                Copy the skill dirs instead of symlinking (default: symlink)
#   --force               Replace a target even if it is a real directory (not a symlink)
#   --with-r8e-ref <path> Also install the r8e API-reference skill from <path>
#                         (the repo's claude-skill/ dir) as .claude/skills/r8e
#   --help                Show this help
set -eu

SCRIPT_DIR=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)

# The eight fleet skills (each is a sibling directory of this script).
SKILLS="r8e-policy review-r8e-policy \
review-r8e-policy-call review-r8e-policy-timeouts review-r8e-policy-retry \
review-r8e-policy-overload review-r8e-policy-fallback review-r8e-policy-observability"

TARGET_DIR="$PWD/.claude/skills"
METHOD="symlink"
FORCE=0
R8E_REF=""

usage() { sed -n '2,21p' "$0" | sed 's/^# \{0,1\}//'; }

while [ $# -gt 0 ]; do
  case "$1" in
    --dir) TARGET_DIR="$2"; shift 2 ;;
    --copy) METHOD="copy"; shift ;;
    --force) FORCE=1; shift ;;
    --with-r8e-ref) R8E_REF="$2"; shift 2 ;;
    --help|-h) usage; exit 0 ;;
    *) echo "unknown option: $1" >&2; usage; exit 2 ;;
  esac
done

# Read the pinned versions from the single source of truth.
read_ver() { sed -n 's/.*"'"$1"'"[[:space:]]*:[[:space:]]*"\([^"]*\)".*/\1/p' "$SCRIPT_DIR/VERSIONS.json"; }
SKILL_VERSION=$(read_ver skill_version)
R8E_VERSION=$(read_ver r8e)
MODULE=$(read_ver module)

echo "r8e policy-skills installer — skill v${SKILL_VERSION}, pinned to ${MODULE} ${R8E_VERSION}"
mkdir -p "$TARGET_DIR"

link_one() {
  name="$1"; src="$2"; dst="$TARGET_DIR/$name"
  if [ -e "$dst" ] || [ -L "$dst" ]; then
    if [ -L "$dst" ] || [ "$FORCE" -eq 1 ]; then
      rm -rf "$dst"
    else
      echo "  skip  $name (a real directory exists; re-run with --force to replace)"
      return 0
    fi
  fi
  if [ "$METHOD" = "copy" ]; then
    cp -R "$src" "$dst"; echo "  copy  $name"
  else
    ln -s "$src" "$dst"; echo "  link  $name -> $src"
  fi
}

for s in $SKILLS; do
  [ -d "$SCRIPT_DIR/$s" ] || { echo "  MISSING source: $SCRIPT_DIR/$s" >&2; exit 1; }
  link_one "$s" "$SCRIPT_DIR/$s"
done

if [ -n "$R8E_REF" ]; then
  R8E_REF_ABS=$(CDPATH= cd -- "$R8E_REF" && pwd)
  link_one "r8e" "$R8E_REF_ABS"
fi

cat <<EOF

Done. Installed into: $TARGET_DIR
This skill release is calibrated for ${MODULE} ${R8E_VERSION}. To use that exact
version in your project:

  go get ${MODULE}@${R8E_VERSION}

Then ask Claude Code, e.g.:
  "Use r8e-policy to write a policy for <service>"   (authoring, expert mode)
  "Use review-r8e-policy on this policy"             (audit)
EOF
