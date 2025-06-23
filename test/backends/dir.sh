# Nothing need be done for the dir backed, but we still need some functions.
# This file can also serve as a skel file for what needs to be done to
# implement a new backend.

# Any necessary backend-specific setup
dir_setup() {
  local LXD_DIR="${1}"

  echo "==> Setting up directory backend in ${LXD_DIR}"
}

# Do the API voodoo necessary to configure LXD to use this backend
dir_configure() {
  local LXD_DIR="${1}"
  local POOL_NAME="lxdtest-${LXD_DIR##*/}" # Use the last part of the LXD_DIR as pool name

  echo "==> Configuring directory backend in ${LXD_DIR}"

  lxc storage create "${POOL_NAME}" dir
  lxc profile device add default root disk path="/" pool="${POOL_NAME}"
}

dir_teardown() {
  local LXD_DIR="${1}"

  echo "==> Tearing down directory backend in ${LXD_DIR}"
}
