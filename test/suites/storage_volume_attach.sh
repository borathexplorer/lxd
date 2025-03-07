test_storage_volume_attach() {
  # Check that we have a big enough range for this test
  if [ ! -e /etc/subuid ] && [ ! -e /etc/subgid ]; then
    UIDs=1000000000
    GIDs=1000000000
    UID_BASE=1000000
    GID_BASE=1000000
  else
    UIDs=0
    GIDs=0
    UID_BASE=0
    GID_BASE=0
    LARGEST_UIDs=0
    LARGEST_GIDs=0

    # shellcheck disable=SC2013
    for entry in $(grep ^root: /etc/subuid); do
      COUNT=$(echo "${entry}" | cut -d: -f3)
      UIDs=$((UIDs+COUNT))

      if [ "${COUNT}" -gt "${LARGEST_UIDs}" ]; then
        LARGEST_UIDs=${COUNT}
        UID_BASE=$(echo "${entry}" | cut -d: -f2)
      fi
    done

    # shellcheck disable=SC2013
    for entry in $(grep ^root: /etc/subgid); do
      COUNT=$(echo "${entry}" | cut -d: -f3)
      GIDs=$((GIDs+COUNT))

      if [ "${COUNT}" -gt "${LARGEST_GIDs}" ]; then
        LARGEST_GIDs=${COUNT}
        GID_BASE=$(echo "${entry}" | cut -d: -f2)
      fi
    done
  fi

  ensure_import_testimage
  pool="lxdtest-$(basename "${LXD_DIR}")"

  # create storage volume
  lxc storage volume create "${pool}" testvolume

  # create a storage colume using a YAML configuration
  lxc storage volume create "${pool}" testvolume-yaml <<EOF
description: foo
config:
  size: 3GiB
EOF
  # Check that the size and description are set correctly
  [ "$(lxc storage volume get "${pool}" testvolume-yaml size)" = "3GiB" ]
  [ "$(lxc storage volume get "${pool}" testvolume-yaml -p description)" = "foo" ]

  # create containers
  lxc launch testimage c1 -c security.privileged=true
  lxc launch testimage c2

  # Attach to a single privileged container
  lxc storage volume attach "${pool}" testvolume c1 testvolume
  PATH_TO_CHECK="${LXD_DIR}/storage-pools/${pool}/custom/default_testvolume"
  [ "$(stat -c %u:%g "${PATH_TO_CHECK}")" = "0:0" ]

  # make container unprivileged
  lxc config set c1 security.privileged false
  [ "$(stat -c %u:%g "${PATH_TO_CHECK}")" = "0:0" ]

  if [ "${UIDs}" -lt 500000 ] || [ "${GIDs}" -lt 500000 ]; then
    echo "==> SKIP: The storage volume attach test requires at least 500000 uids and gids"
    lxc rm -f c1 c2
    lxc storage volume delete "${pool}" testvolume
    return
  fi

  # restart
  lxc restart --force c1
  [ "$(stat -c %u:%g "${PATH_TO_CHECK}")" = "${UID_BASE}:${GID_BASE}" ]

  # give container isolated id mapping
  lxc config set c1 security.idmap.isolated true
  [ "$(stat -c %u:%g "${PATH_TO_CHECK}")" = "${UID_BASE}:${GID_BASE}" ]

  # restart
  lxc restart --force c1

  # get new isolated base ids
  ISOLATED_UID_BASE="$(lxc exec c1 -- cat /proc/self/uid_map | awk '{print $2}')"
  ISOLATED_GID_BASE="$(lxc exec c1 -- cat /proc/self/gid_map | awk '{print $2}')"
  [ "$(stat -c %u:%g "${PATH_TO_CHECK}")" = "${ISOLATED_UID_BASE}:${ISOLATED_GID_BASE}" ]

  ! lxc storage volume attach "${pool}" testvolume c2 testvolume || false

  # give container standard mapping
  lxc config set c1 security.idmap.isolated false
  [ "$(stat -c %u:%g "${PATH_TO_CHECK}")" = "${ISOLATED_UID_BASE}:${ISOLATED_GID_BASE}" ]

  # restart
  lxc restart --force c1
  [ "$(stat -c %u:%g "${PATH_TO_CHECK}")" = "${UID_BASE}:${GID_BASE}" ]

  # attach second container
  lxc storage volume attach "${pool}" testvolume c2 testvolume

  # check that setting perms on the root of the custom volume persists after a reboot.
  lxc exec c2 -- stat -c '%a' /testvolume | grep 711
  lxc exec c2 -- chmod 0700 /testvolume
  lxc exec c2 -- stat -c '%a' /testvolume | grep 700
  lxc restart --force c2
  lxc exec c2 -- stat -c '%a' /testvolume | grep 700

  # check specifying volumes with source=custom/c1 syntax
  lxc storage volume detach "${pool}" testvolume c1
  lxc storage volume detach "${pool}" testvolume c2

  lxc config device add c1 testvolume disk pool="${pool}" source=custom/testvolume path=/testvolume

  lxc storage volume show "${pool}" testvolume | grep -q '/1.0/instances/c1'

  # rename should preserve source=custom/testvolume syntax
  lxc stop c1

  lxc storage volume rename "${pool}" testvolume testvolume2

  [ "$(lxc config device get c1 testvolume source)" = "custom/testvolume2" ]

  # delete containers
  lxc delete -f c1
  lxc delete -f c2
  lxc storage volume delete "${pool}" testvolume2
}
