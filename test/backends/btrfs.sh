btrfs_setup() {
  local LXD_DIR="${1}"

  echo "==> Setting up btrfs backend in ${LXD_DIR}"
}

btrfs_configure() {
  local LXD_DIR="${1}"

  lxc storage create "lxdtest-$(basename "${LXD_DIR}")" btrfs size=1GiB
  lxc profile device add default root disk path="/" pool="lxdtest-$(basename "${LXD_DIR}")"

  echo "==> Configuring btrfs backend in ${LXD_DIR}"
}

btrfs_teardown() {
  local LXD_DIR="${1}"

  echo "==> Tearing down btrfs backend in ${LXD_DIR}"
}
