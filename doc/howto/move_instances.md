---
discourse: lxc:16635
---

(move-instances)=
# How to move existing LXD instances between servers

If you use the LXD client, you can move or copy instances from one LXD server (remote or local) to another.

```{note}
{ref}`Remote servers <remotes>` are a concept of the LXD client.
Therefore, there is no direct equivalent for moving instances in the API or the UI.

However, you can {ref}`export an instance <instances-backup-export-instance>` from one server and {ref}`import it <instances-backup-import-instance>` to another server.
```

To move an instance from one LXD server to another, use the [`lxc move`](lxc_move.md) command:

    lxc move [<source_remote>:]<source_instance_name> <target_remote>:[<target_instance_name>]

```{note}
When moving a container, you must stop it first.
See {ref}`live-migration-containers` for more information.

When moving a virtual machine, you must either enable {ref}`live-migration-vms` or stop it first.
```

Alternatively, you can use the [`lxc copy`](lxc_copy.md) command if you want to duplicate the instance:

    lxc copy [<source_remote>:]<source_instance_name> <target_remote>:[<target_instance_name>]

```{tip}
If the volume already exists in the target location, use the `--refresh` flag to update the copy (see {ref}`storage-optimized-volume-transfer` for the benefits).
```

In both cases, you don't need to specify the source remote if it is your default remote, and you can leave out the target instance name if you want to use the same instance name.
If you want to move the instance to a specific cluster member, specify it with the `--target` flag.
In this case, do not specify the source and target remote.

You can add the `--mode` flag to choose a transfer mode, depending on your network setup:

`pull` (default)
: Instruct the target server to connect to the source server and pull the respective instance.

`push`
: Instruct the source server to connect to the target server and push the instance.

`relay`
: Instruct the client to connect to both the source and the target server and transfer the data through the client.

If you need to adapt the configuration for the instance to run on the target server, you can either specify the new configuration directly (using `--config`, `--device`, `--storage` or `--target-project`) or through profiles (using `--no-profiles` or `--profile`). See [`lxc move --help`](lxc_move.md) for all available flags.

(live-migration)=
## Live migration

Live migration means migrating an instance while it is running.
This method is supported for virtual machines.
For containers, there is limited support.

(live-migration-vms)=
### Live migration for virtual machines

Virtual machines can be moved to another server while they are running, thus without any downtime.

To allow for live migration, you must enable support for stateful migration.
To do so, ensure the following configuration:

* Set {config:option}`instance-migration:migration.stateful` to `true` on the instance.
* Set {config:option}`device-disk-device-conf:size.state` of the virtual machine's root disk device to at least the size of the virtual machine's {config:option}`instance-resource-limits:limits.memory` setting.

```{note}
If you are using a shared storage pool like Ceph RBD to back your instance, you don't need to set {config:option}`device-disk-device-conf:size.state` to perform live migration.
```

```{note}
When {config:option}`instance-migration:migration.stateful` is enabled in LXD, virtiofs shares are disabled, and files are only shared via the 9P protocol. Consequently, guest OSes lacking 9P support, such as CentOS 8, cannot share files with the host unless stateful migration is disabled. Additionally, the `lxd-agent` will not function for these guests under these conditions.
```

(live-migration-containers)=
### Live migration for containers

For containers, there is limited support for live migration using [{abbr}`CRIU (Checkpoint/Restore in Userspace)`](https://criu.org/Main_Page).
However, because of extensive kernel dependencies, only very basic containers (non-`systemd` containers without a network device) can be migrated reliably.
In most real-world scenarios, you should stop the container, move it over and then start it again.

If you want to use live migration for containers, you must enable CRIU on both the source and the target server.
If you are using the snap, use the following commands to enable CRIU:

    snap set lxd criu.enable=true
    sudo systemctl reload snap.lxd.daemon

Otherwise, make sure you have CRIU installed on both systems.

To optimize the memory transfer for a container, set the {config:option}`instance-migration:migration.incremental.memory` property to `true` to make use of the pre-copy features in CRIU.
With this configuration, LXD instructs CRIU to perform a series of memory dumps for the container.
After each dump, LXD sends the memory dump to the specified remote.
In an ideal scenario, each memory dump will decrease the delta to the previous memory dump, thereby increasing the percentage of memory that is already synced.
When the percentage of synced memory is equal to or greater than the threshold specified via {config:option}`instance-migration:migration.incremental.memory.goal`, or the maximum number of allowed iterations specified via {config:option}`instance-migration:migration.incremental.memory.iterations` is reached, LXD instructs CRIU to perform a final memory dump and transfers it.
