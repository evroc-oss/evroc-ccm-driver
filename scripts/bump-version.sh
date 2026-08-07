#!/bin/bash
# SPDX-License-Identifier: Apache-2.0
# Copyright (c) 2026 evroc
#
# Automated version bump script for evroc-ccm
# Usage: ./scripts/bump-version.sh <version>
#        ./scripts/bump-version.sh v0.2.0
#        ./scripts/bump-version.sh 0.2.0

set -euo pipefail

if [ -z "${1:-}" ]; then
    echo "Error: Version number required"
    echo "Usage: ./scripts/bump-version.sh <version>"
    echo "Example: ./scripts/bump-version.sh v0.2.0"
    exit 1
fi

INPUT_VERSION="$1"
CHART_FILE="chart/Chart.yaml"
VALUES_FILE="chart/values.yaml"
VERSION_FILE="VERSION"
CHANGELOG_FILE="CHANGELOG.md"

# Strip 'v' prefix if present
NEW_VERSION="${INPUT_VERSION#v}"

# Validate version format (semantic versioning)
if ! [[ "$NEW_VERSION" =~ ^[0-9]+\.[0-9]+\.[0-9]+$ ]]; then
    echo "Error: Invalid version format '$INPUT_VERSION'"
    echo "Version must be in format: [v]major.minor.patch (e.g., v0.2.0 or 0.2.0)"
    exit 1
fi

# Check if this version has already been released
ESCAPED_VERSION="${NEW_VERSION//./\\.}"
if [ -f "$CHANGELOG_FILE" ] && grep -qE "## \[$ESCAPED_VERSION\]" "$CHANGELOG_FILE"; then
    echo "WARNING: Version $NEW_VERSION already exists in $CHANGELOG_FILE"
    read -p "   Continue anyway? (yes/no): " CONFIRM
    if [ "$CONFIRM" != "yes" ]; then
        echo "Aborted"
        exit 1
    fi
fi

# Get current version from Chart.yaml
CURRENT_VERSION=$(grep "^version:" "$CHART_FILE" | awk '{print $2}')

echo "Current version: $CURRENT_VERSION"
echo "New version: $NEW_VERSION"
echo ""

# Track updated files
UPDATED_FILES=()

# 1. Update CHANGELOG.md
if [ -f "$CHANGELOG_FILE" ]; then
    echo "Updating $CHANGELOG_FILE..."
    TODAY=$(date +%Y-%m-%d)
    sed -i "s/## \[Unreleased\]/## [Unreleased]\n\n## [$NEW_VERSION] - $TODAY/" "$CHANGELOG_FILE"

    # Update [Unreleased] compare link
    if grep -q "\[Unreleased\]:" "$CHANGELOG_FILE"; then
        sed -i "s|\[Unreleased\]:.*|[Unreleased]: https://github.com/evroc-oss/evroc-ccm-driver/compare/v$NEW_VERSION...HEAD|" "$CHANGELOG_FILE"
    else
        echo "" >> "$CHANGELOG_FILE"
        echo "[Unreleased]: https://github.com/evroc-oss/evroc-ccm-driver/compare/v$NEW_VERSION...HEAD" >> "$CHANGELOG_FILE"
    fi

    # Add version link
    if ! grep -q "\[$NEW_VERSION\]:" "$CHANGELOG_FILE"; then
        echo "[$NEW_VERSION]: https://github.com/evroc-oss/evroc-ccm-driver/releases/tag/v$NEW_VERSION" >> "$CHANGELOG_FILE"
    fi

    UPDATED_FILES+=("$CHANGELOG_FILE")
fi

# 2. Update Helm chart version
if [ -f "$CHART_FILE" ]; then
    echo "Updating $CHART_FILE..."
    sed -i "s/^version:.*/version: $NEW_VERSION/" "$CHART_FILE"
    sed -i "s/^appVersion:.*/appVersion: \"$NEW_VERSION\"/" "$CHART_FILE"
    UPDATED_FILES+=("$CHART_FILE")
fi

# 3. Update the image tag in values.yaml. Replace the whole value so a digest
# pinned to the previous release is not carried into the new tag; the new
# release's digest can only be pinned once its image has been published.
if [ -f "$VALUES_FILE" ]; then
    echo "Updating $VALUES_FILE..."
    sed -i '/repository: ghcr\.io\/evroc-oss\/evroc-ccm-driver/{n;s/^\([[:space:]]*tag:[[:space:]]*\).*$/\1v'"$NEW_VERSION"'/;}' "$VALUES_FILE"
    UPDATED_FILES+=("$VALUES_FILE")
fi

# 4. Update VERSION file
if [ -f "$VERSION_FILE" ]; then
    echo "Updating $VERSION_FILE..."
    echo "$NEW_VERSION" > "$VERSION_FILE"
    UPDATED_FILES+=("$VERSION_FILE")
fi

# 5. Update README.md version references
if [ -f "README.md" ]; then
    echo "Updating README.md..."
    sed -i "s|VERSION=\"v[0-9]*\.[0-9]*\.[0-9]*\"|VERSION=\"v$NEW_VERSION\"|g" README.md
    sed -i "s|evroc-ccm:v[0-9]*\.[0-9]*\.[0-9]*|evroc-ccm:v$NEW_VERSION|g" README.md
    UPDATED_FILES+=("README.md")
fi

# Stage and commit
echo ""
echo "Staging changes..."
git add "${UPDATED_FILES[@]}"

echo "Creating commit..."
git commit -m "chore: bump version to v$NEW_VERSION"

echo ""
echo "Version bumped successfully!"
echo "   Old: v$CURRENT_VERSION"
echo "   New: v$NEW_VERSION"
echo ""
echo "Files updated:"
for f in "${UPDATED_FILES[@]}"; do
    echo "   - $f"
done
echo ""
echo "Next steps:"
echo "   git push origin $(git branch --show-current)"
echo "   ./scripts/sync-to-github.sh"
echo "   # Review and merge PR on GitHub, then:"
echo "   ./scripts/sync-to-github.sh --tag v$NEW_VERSION"
echo ""
