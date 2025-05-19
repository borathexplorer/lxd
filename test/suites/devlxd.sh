test_devlxd() {
  ensure_import_testimage
  fingerprint="$(lxc image info testimage | awk '/^Fingerprint:/ {print $2}')"

  # Ensure testimage is not set as cached.
  lxd sql global "UPDATE images SET cached=0 WHERE fingerprint=\"${fingerprint}\""

  (
    cd devlxd-client || return
    # Use -buildvcs=false here to prevent git complaining about untrusted directory when tests are run as root.
    CGO_ENABLED=0 go build -tags netgo -v -buildvcs=false ./...
  )

  lxc launch testimage devlxd -c security.devlxd=false

  ! lxc exec devlxd -- test -S /dev/lxd/sock || false
  lxc config unset devlxd security.devlxd
  lxc exec devlxd -- test -S /dev/lxd/sock
  lxc file push --mode 0755 "devlxd-client/devlxd-client" devlxd/bin/

  # Try to get a host's private image from devlxd.
  [ "$(lxc exec devlxd -- devlxd-client image-export "${fingerprint}")" = "not authorized" ]
  lxc config set devlxd security.devlxd.images true
  # Trying to get a private image should return a not found error so that the client can't infer the existence
  # of an image with the provided fingerprint.
  [ "$(lxc exec devlxd -- devlxd-client image-export "${fingerprint}")" = "not found" ]
  lxd sql global "UPDATE images SET cached=1 WHERE fingerprint=\"${fingerprint}\""
  # No output means the export succeeded.
  [ -z "$(lxc exec devlxd -- devlxd-client image-export "${fingerprint}")" ]

  lxc config set devlxd user.foo bar
  [ "$(lxc exec devlxd -- devlxd-client user.foo)" = "bar" ]

  lxc config set devlxd user.foo "bar %s bar"
  [ "$(lxc exec devlxd -- devlxd-client user.foo)" = "bar %s bar" ]

  # Make sure instance configuration keys are not accessible
  [ "$(lxc exec devlxd -- devlxd-client security.nesting)" = "not authorized" ]
  lxc config set devlxd security.nesting true
  [ "$(lxc exec devlxd -- devlxd-client security.nesting)" = "not authorized" ]

  cmd=$(unset -f lxc; command -v lxc)
  ${cmd} exec devlxd -- devlxd-client monitor-websocket > "${TEST_DIR}/devlxd-websocket.log" &
  client_websocket=$!

  ${cmd} exec devlxd -- devlxd-client monitor-stream > "${TEST_DIR}/devlxd-stream.log" &
  client_stream=$!

  (
    cat << EOF
{
  "type": "config",
  "timestamp": "0001-01-01T00:00:00Z",
  "metadata": {
    "key": "user.foo",
    "old_value": "bar",
    "value": "baz"
  }
}
{
  "type": "device",
  "timestamp": "0001-01-01T00:00:00Z",
  "metadata": {
    "action": "added",
    "config": {
      "path": "/mnt",
      "source": "${TEST_DIR}",
      "type": "disk"
    },
    "name": "mnt"
  }
}
{
  "type": "device",
  "timestamp": "0001-01-01T00:00:00Z",
  "metadata": {
    "action": "removed",
    "config": {
      "path": "/mnt",
      "source": "${TEST_DIR}",
      "type": "disk"
    },
    "name": "mnt"
  }
}
EOF
  ) > "${TEST_DIR}/devlxd.expected"

  MATCH=0

  for _ in $(seq 10); do
    lxc config set devlxd user.foo bar
    lxc config set devlxd security.nesting true

    true > "${TEST_DIR}/devlxd-websocket.log"
    true > "${TEST_DIR}/devlxd-stream.log"

    lxc config set devlxd user.foo baz
    lxc config set devlxd security.nesting false
    lxc config device add devlxd mnt disk source="${TEST_DIR}" path=/mnt
    lxc config device remove devlxd mnt

    if [ "$(tr -d '\0' < "${TEST_DIR}/devlxd-websocket.log" | md5sum | cut -d' ' -f1)" != "$(md5sum "${TEST_DIR}/devlxd.expected" | cut -d' ' -f1)" ] || [ "$(tr -d '\0' < "${TEST_DIR}/devlxd-stream.log" | md5sum | cut -d' ' -f1)" != "$(md5sum "${TEST_DIR}/devlxd.expected" | cut -d' ' -f1)" ]; then
      sleep 0.5
      continue
    fi

    MATCH=1
    break
  done

  kill -9 "${client_websocket}"
  kill -9 "${client_stream}"

  lxc monitor --type=lifecycle > "${TEST_DIR}/devlxd.log" &
  monitorDevlxdPID=$!

  # Test instance Ready state
  lxc info devlxd | grep -q 'Status: RUNNING'
  lxc exec devlxd -- devlxd-client ready-state true
  [ "$(lxc config get devlxd volatile.last_state.ready)" = "true" ]

  [ "$(grep -Fc "instance-ready" "${TEST_DIR}/devlxd.log")" = "1" ]

  lxc info devlxd | grep -q 'Status: READY'
  lxc exec devlxd -- devlxd-client ready-state false
  [ "$(lxc config get devlxd volatile.last_state.ready)" = "false" ]

  [ "$(grep -Fc "instance-ready" "${TEST_DIR}/devlxd.log")" = "1" ]

  lxc info devlxd | grep -q 'Status: RUNNING'

  kill -9 ${monitorDevlxdPID} || true

  shutdown_lxd "${LXD_DIR}"
  respawn_lxd "${LXD_DIR}" true

  # volatile.last_state.ready should be unset during daemon init
  [ -z "$(lxc config get devlxd volatile.last_state.ready)" ]

  lxc monitor --type=lifecycle > "${TEST_DIR}/devlxd.log" &
  monitorDevlxdPID=$!

  lxc exec devlxd -- devlxd-client ready-state true
  [ "$(lxc config get devlxd volatile.last_state.ready)" = "true" ]

  [ "$(grep -Fc "instance-ready" "${TEST_DIR}/devlxd.log")" = "1" ]

  lxc stop -f devlxd
  [ "$(lxc config get devlxd volatile.last_state.ready)" = "false" ]

  lxc start devlxd
  lxc exec devlxd -- devlxd-client ready-state true
  [ "$(lxc config get devlxd volatile.last_state.ready)" = "true" ]

  [ "$(grep -Fc "instance-ready" "${TEST_DIR}/devlxd.log")" = "2" ]

  # Check device configs are available and that NIC hwaddr is available even if volatile.
  hwaddr=$(lxc config get devlxd volatile.eth0.hwaddr)
  [ "$(lxc exec devlxd -- devlxd-client devices | jq -r .eth0.hwaddr)" = "${hwaddr}" ]

  lxc delete devlxd --force
  kill -9 ${monitorDevlxdPID} || true

  [ "${MATCH}" = "1" ]
}
