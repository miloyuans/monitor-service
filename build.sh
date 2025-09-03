#!/bin/bash

# Optimized build and package script: Downloads pre-compiled Go binary, config.yaml, and systemd-sml.service
# from GitHub Releases and Raw, then builds RPM or DEB package for x86_64 (amd64) or aarch64 (arm64).
# Usage: ./build_package.sh <rpm|deb> <github_repo_url> [version]
# Example: ./build_package.sh rpm https://github.com/yourusername/systemd-sml [v1.0.0]

set -e  # Exit on any error
set -o pipefail  # Exit on pipeline errors

# Function to log messages
log() {
    echo "[$(date '+%Y-%m-%d %H:%M:%S')] $1"
}

# Function to clean up temporary directory
cleanup() {
    if [ -n "$TEMP_DIR" ] && [ -d "$TEMP_DIR" ]; then
        log "Cleaning up temporary directory: $TEMP_DIR"
        rm -rf "$TEMP_DIR"
    fi
}

# Trap errors and cleanup
trap cleanup EXIT

# Check arguments
if [ $# -lt 2 ] || [ $# -gt 3 ]; then
    log "Usage: $0 <rpm|deb> <github_repo_url> [version]"
    log "Example: $0 rpm https://github.com/yourusername/systemd-sml v1.0.0"
    exit 1
fi

PACKAGE_TYPE="$1"
REPO_URL="$2"
VERSION="${3:-latest}"  # Default to latest if not provided

# Detect system architecture
ARCH=$(uname -m)
case "$ARCH" in
    x86_64)
        BINARY_ARCH="amd64"
        BUILD_ARCH="x86_64"
        ;;
    aarch64)
        BINARY_ARCH="arm64"
        BUILD_ARCH="aarch64"
        ;;
    *)
        log "Error: Unsupported architecture: $ARCH"
        exit 1
        ;;
esac

# Parse GitHub repo owner and name
REPO_OWNER=$(echo "$REPO_URL" | grep -oE '[^/]+/[^/]+$' | cut -d'/' -f1)
REPO_NAME=$(echo "$REPO_URL" | grep -oE '[^/]+/[^/]+$' | cut -d'/' -f2)
if [ -z "$REPO_OWNER" ] || [ -z "$REPO_NAME" ]; then
    log "Error: Invalid GitHub repo URL: $REPO_URL"
    exit 1
fi

# Get latest version from GitHub API if 'latest' is specified
if [ "$VERSION" = "latest" ]; then
    log "Fetching latest version from GitHub API..."
    VERSION=$(curl -sSL "https://api.github.com/repos/$REPO_OWNER/$REPO_NAME/releases/latest" | \
              grep '"tag_name":' | sed -E 's/.*"tag_name":\s*"([^"]+)".*/\1/')
    if [ -z "$VERSION" ]; then
        log "Error: Failed to fetch latest version from GitHub API"
        exit 1
    fi
    log "Using latest version: $VERSION"
fi
# Remove 'v' prefix if present
VERSION=$(echo "$VERSION" | sed 's/^v//')

# Construct URLs
BINARY_URL="https://github.com/${REPO_OWNER}/${REPO_NAME}/releases/download/v${VERSION}/systemd-sml-linux-${BINARY_ARCH}"
RAW_BASE="https://raw.githubusercontent.com/${REPO_OWNER}/${REPO_NAME}/main"

# Check for required tools
if [ "$PACKAGE_TYPE" = "rpm" ]; then
    command -v rpmbuild >/dev/null 2>&1 || {
        log "Installing rpm-build..."
        sudo yum install -y rpm-build
    }
elif [ "$PACKAGE_TYPE" = "deb" ]; then
    command -v dpkg-deb >/dev/null 2>&1 || {
        log "Installing dpkg-dev and debhelper..."
        sudo apt update
        sudo apt install -y dpkg-dev debhelper
    }
else
    log "Error: Invalid package type. Use 'rpm' or 'deb'"
    exit 1
fi

# Create temporary directory
TEMP_DIR=$(mktemp -d)
log "Working in temporary directory: $TEMP_DIR"
cd "$TEMP_DIR"

# Download files with verification
download_file() {
    local url="$1"
    local output="$2"
    log "Downloading $url to $output..."
    if ! wget --timeout=30 --tries=2 "$url" -O "$output"; then
        log "Error: Failed to download $url"
        exit 1
    fi
    if [ ! -s "$output" ]; then
        log "Error: Downloaded file $output is empty"
        exit 1
    fi
}

download_file "$BINARY_URL" "systemd-sml"
download_file "${RAW_BASE}/config-default.yaml" "config.yaml"
download_file "${RAW_BASE}/systemd-sml.service" "systemd-sml.service"

# Set executable permission for binary (but avoid running unverified binary)
chmod +x systemd-sml

# Verify binary has debug info and build ID to avoid find-debuginfo.sh errors
if [ "$PACKAGE_TYPE" = "rpm" ]; then
    log "Verifying binary build ID..."
    if ! readelf -n systemd-sml | grep -q 'Build ID'; then
        log "Warning: No build ID found in binary, adding --build-id"
        # Re-link binary to add build ID (requires ld and objcopy)
        objcopy --add-gnu-debuglink=/dev/null --set-section-flags .note.gnu.build-id=alloc,contents,read systemd-sml systemd-sml.tmp
        mv systemd-sml.tmp systemd-sml
        chmod +x systemd-sml
    fi
fi

if [ "$PACKAGE_TYPE" = "rpm" ]; then
    # Create RPM build directories
    mkdir -p ~/rpmbuild/{BUILD,RPMS,SOURCES,SPECS,SRPMS}

    # Create source tarball
    mkdir "systemd-sml-$VERSION"
    cp systemd-sml config.yaml systemd-sml.service "systemd-sml-$VERSION/"
    tar -czf ~/rpmbuild/SOURCES/systemd-sml.tar.gz "systemd-sml-$VERSION"

    # Create RPM spec file
    cat > systemd-sml.spec << EOF
Name:           systemd-sml
Version:        $VERSION
Release:        1%{?dist}
Summary:        A simple Go HTTP service

License:        MIT
Source0:        systemd-sml.tar.gz
BuildArch:      $BUILD_ARCH

Requires:       systemd

%description
systemd-sml is a simple Go-based HTTP service that serves a configurable web page.

%global debug_package %{nil}  # Disable debuginfo to avoid find-debuginfo.sh errors

%prep
%setup -q

%build
# No build needed, binary is pre-compiled

%install
install -d %{buildroot}/usr/lib/systemd
install -m 755 systemd-sml %{buildroot}/usr/lib/systemd/systemd-sml
install -d %{buildroot}/usr/local/etc/systemd-sml
install -m 644 config.yaml %{buildroot}/usr/local/etc/systemd-sml/config.yaml
install -d %{buildroot}/%{_unitdir}
install -m 644 systemd-sml.service %{buildroot}/%{_unitdir}/systemd-sml.service

%files
/usr/lib/systemd/systemd-sml
/usr/local/etc/systemd-sml/config.yaml
%{_unitdir}/systemd-sml.service

%post
%systemd_post systemd-sml.service
systemctl enable systemd-sml.service
systemctl start systemd-sml.service

%preun
%systemd_preun systemd-sml.service

%postun
%systemd_postun_with_restart systemd-sml.service

%changelog
* $(date '+%a %b %d %Y') $(whoami) <$(whoami)@$(hostname)> - $VERSION-1
- Optimized for pre-compiled binary
EOF

    # Build RPM
    log "Building RPM package..."
    if ! rpmbuild -ba systemd-sml.spec; then
        log "Error: RPM build failed"
        exit 1
    fi

    # Move RPM to current directory
    RPM_PACKAGE=$(find ~/rpmbuild/RPMS/$BUILD_ARCH -name "systemd-sml-$VERSION-*.rpm" | head -n 1)
    if [ -n "$RPM_PACKAGE" ]; then
        mv "$RPM_PACKAGE" .
        log "RPM package created: $(basename "$RPM_PACKAGE")"
    else
        log "Error: RPM package not found"
        exit 1
    fi

elif [ "$PACKAGE_TYPE" = "deb" ]; then
    # Create DEB structure
    mkdir -p "systemd-sml-$VERSION/DEBIAN"
    mkdir -p "systemd-sml-$VERSION/usr/lib/systemd"
    mkdir -p "systemd-sml-$VERSION/usr/local/etc/systemd-sml"
    mkdir -p "systemd-sml-$VERSION/lib/systemd/system"

    # Create DEB control file
    cat > "systemd-sml-$VERSION/DEBIAN/control" << EOF
Package: systemd-sml
Version: $VERSION
Architecture: $BINARY_ARCH
Maintainer: $(whoami) <$(whoami)@$(hostname)>
Depends: systemd
Section: utils
Priority: optional
Description: A simple Go HTTP service
 systemd-sml is a simple Go-based HTTP service that serves a configurable web page.
EOF

    # Create DEB postinst script
    cat > "systemd-sml-$VERSION/DEBIAN/postinst" << EOF
#!/bin/sh
set -e
systemctl enable systemd-sml.service
systemctl start systemd-sml.service
EOF
    chmod +x "systemd-sml-$VERSION/DEBIAN/postinst"

    # Copy files
    cp systemd-sml "systemd-sml-$VERSION/usr/lib/systemd/"
    cp config.yaml "systemd-sml-$VERSION/usr/local/etc/systemd-sml/"
    cp systemd-sml.service "systemd-sml-$VERSION/lib/systemd/system/"

    # Build DEB
    log "Building DEB package..."
    if ! dpkg-deb --build "systemd-sml-$VERSION"; then
        log "Error: DEB build failed"
        exit 1
    fi
    log "DEB package created: systemd-sml-$VERSION.deb"
fi

# Output installation instructions
log "Package build complete. Install with:"
if [ "$PACKAGE_TYPE" = "rpm" ]; then
    log "sudo yum install -y $(basename "$RPM_PACKAGE")"
else
    log "sudo apt install -y ./systemd-sml-$VERSION.deb"
fi