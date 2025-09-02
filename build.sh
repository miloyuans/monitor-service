#!/bin/bash
# Optimized build and package script: Download pre-compiled Go binary from GitHub Releases,
# along with config.yaml and systemd-sml.service from Raw, then build RPM or DEB package.
# Supports x86_64 (amd64) and aarch64 (arm64) architectures.
# Usage: ./build_package.sh <rpm|deb> <github_repo_url>
# Example: ./build_package.sh rpm https://github.com/yourusername/systemd-sml

if [ $# -ne 2 ]; then
  echo "Usage: $0 <rpm|deb> <github_repo_url>"
  exit 1
fi

PACKAGE_TYPE="$1"
REPO_URL="$2"
VERSION="$3"

# Detect system architecture
ARCH=$(uname -m)
if [ "$ARCH" = "x86_64" ]; then
  BINARY_ARCH="amd64"
  BUILD_ARCH="x86_64"
elif [ "$ARCH" = "aarch64" ]; then
  BINARY_ARCH="arm64"
  BUILD_ARCH="aarch64"
else
  echo "Error: Unsupported architecture: $ARCH"
  exit 1
fi

# Parse GitHub repo owner and name from REPO_URL (e.g., https://github.com/owner/repo)
REPO_OWNER=$(basename $(dirname "$REPO_URL"))
REPO_NAME=$(basename "$REPO_URL")

# Construct URLs
BINARY_URL="https://github.com/${REPO_OWNER}/${REPO_NAME}/releases/download/v${VERSION}/systemd-sml-linux-${BINARY_ARCH}"
RAW_BASE="https://raw.githubusercontent.com/${REPO_OWNER}/${REPO_NAME}/main"

# Install packaging dependencies (no Go needed)
if [ "$PACKAGE_TYPE" = "rpm" ]; then
  sudo yum install -y rpm-build
elif [ "$PACKAGE_TYPE" = "deb" ]; then
  sudo apt update
  sudo apt install -y dpkg-dev debhelper
else
  echo "Error: Invalid package type. Use 'rpm' or 'deb'"
  exit 1
fi

# Create temporary directory
TEMP_DIR=$(mktemp -d)
cd "$TEMP_DIR"

# Download pre-compiled binary from Releases
wget "$BINARY_URL" -O systemd-sml
if [ $? -ne 0 ]; then
  echo "Error: Failed to download binary from $BINARY_URL"
  exit 1
fi
chmod +x systemd-sml

# Download config.yaml and systemd-sml.service from Raw
wget "${RAW_BASE}/config-default.yaml" -O config.yaml
if [ $? -ne 0 ]; then
  echo "Error: Failed to download config.yaml"
  exit 1
fi
wget "${RAW_BASE}/systemd-sml.service"
if [ $? -ne 0 ]; then
  echo "Error: Failed to download systemd-sml.service"
  exit 1
fi

if [ "$PACKAGE_TYPE" = "rpm" ]; then
  # Create RPM build directories
  mkdir -p ~/rpmbuild/{BUILD,RPMS,SOURCES,SPECS,SRPMS}

  # Create source tarball (includes binary, config, and service)
  mkdir systemd-sml-$VERSION
  cp systemd-sml config.yaml systemd-sml.service systemd-sml-$VERSION/
  tar -czf ~/rpmbuild/SOURCES/systemd-sml.tar.gz systemd-sml-$VERSION

  # Create RPM spec file (no build step, since binary is pre-compiled)
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
* Fri Aug 29 2025 Your Name <your.email@example.com> - $VERSION-1
- Optimized for pre-compiled binary
EOF

  # Build RPM
  rpmbuild -ba systemd-sml.spec
  # Move the RPM package to current directory (use glob for dist)
  mv ~/rpmbuild/RPMS/$BUILD_ARCH/systemd-sml-$VERSION-*.$BUILD_ARCH.rpm .
  RPM_PACKAGE=$(ls systemd-sml-$VERSION-*.$BUILD_ARCH.rpm)
  echo "RPM package created: $RPM_PACKAGE"

elif [ "$PACKAGE_TYPE" = "deb" ]; then
  # Create DEB structure
  mkdir -p systemd-sml-$VERSION/DEBIAN
  mkdir -p systemd-sml-$VERSION/usr/lib/systemd
  mkdir -p systemd-sml-$VERSION/usr/local/etc/systemd-sml
  mkdir -p systemd-sml-$VERSION/lib/systemd/system

  # Create DEB control file
  cat > systemd-sml-$VERSION/DEBIAN/control << EOF
Package: systemd-sml
Version: $VERSION
Architecture: $BINARY_ARCH
Maintainer: Your Name <your.email@example.com>
Depends: systemd
Section: utils
Priority: optional
Description: A simple Go HTTP service
 systemd-sml is a simple Go-based HTTP service that serves a configurable web page.
EOF

  # Create DEB postinst script
  cat > systemd-sml-$VERSION/DEBIAN/postinst << EOF
#!/bin/sh
set -e
systemctl enable systemd-sml.service
systemctl start systemd-sml.service
EOF
  chmod +x systemd-sml-$VERSION/DEBIAN/postinst

  # Copy files
  cp systemd-sml systemd-sml-$VERSION/usr/lib/systemd/
  cp config.yaml systemd-sml-$VERSION/usr/local/etc/systemd-sml/
  cp systemd-sml.service systemd-sml-$VERSION/lib/systemd/system/

  # Build DEB
  dpkg-deb --build systemd-sml-$VERSION
  echo "DEB package created: systemd-sml-$VERSION.deb"
fi

# Clean up
cd -
rm -rf "$TEMP_DIR"

echo "Package build complete. Install with:"
if [ "$PACKAGE_TYPE" = "rpm" ]; then
  echo "sudo yum install -y $RPM_PACKAGE"
else
  echo "sudo apt install -y ./systemd-sml-$VERSION.deb"
fi