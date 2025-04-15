//go:build linux && cgo && !agent

package sys

import (
	"os"
	"os/user"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/canonical/lxd/lxd/cgroup"
	"github.com/canonical/lxd/lxd/db/cluster"
	"github.com/canonical/lxd/lxd/idmap"
	"github.com/canonical/lxd/lxd/storage/filesystem"
	"github.com/canonical/lxd/lxd/util"
	"github.com/canonical/lxd/shared"
	"github.com/canonical/lxd/shared/logger"
	"github.com/canonical/lxd/shared/osarch"
	"github.com/canonical/lxd/shared/version"
)

// InotifyTargetInfo records the inotify information associated with a given
// inotify target.
type InotifyTargetInfo struct {
	Mask uint32
	Wd   int
	Path string
}

// InotifyInfo records the inotify information associated with a given
// inotify instance.
type InotifyInfo struct {
	Fd int
	sync.RWMutex
	Targets map[string]*InotifyTargetInfo
}

// AppArmorFeaturesInfo records the AppArmor features availability.
type AppArmorFeaturesInfo struct {
	sync.Mutex
	Map map[string]bool
}

// OS is a high-level facade for accessing all operating-system
// level functionality that LXD uses.
type OS struct {
	// Directories
	CacheDir string // Cache directory (e.g. /var/cache/lxd/).
	LogDir   string // Log directory (e.g. /var/log/lxd).
	VarDir   string // Data directory (e.g. /var/lib/lxd/).

	// Daemon environment
	Architectures   []int           // Cache of detected system architectures
	BackingFS       string          // Backing filesystem of $LXD_DIR/containers
	ExecPath        string          // Absolute path to the LXD executable
	IdmapSet        *idmap.IdmapSet // Information about user/group ID mapping
	InotifyWatch    InotifyInfo
	LxcPath         string // Path to the $LXD_DIR/containers directory
	MockMode        bool   // If true some APIs will be mocked (for testing)
	Nodev           bool
	RunningInUserNS bool

	// Privilege dropping
	UnprivUser  string
	UnprivUID   uint32
	UnprivGroup string
	UnprivGID   uint32

	// Apparmor features
	AppArmorAdmin     bool
	AppArmorAvailable bool
	AppArmorConfined  bool
	AppArmorStacked   bool
	AppArmorStacking  bool
	AppArmorFeatures  AppArmorFeaturesInfo

	// Cgroup features
	CGInfo cgroup.Info

	// Kernel features
	BPFToken                bool // BPFToken indicates support for BPF token delegation mechanism.
	CloseRange              bool // CloseRange indicates support for the close_range syscall.
	ContainerCoreScheduling bool // ContainerCoreScheduling indicates LXC and kernel support for core scheduling.
	CoreScheduling          bool // CoreScheduling indicates support for core scheduling syscalls.
	IdmappedMounts          bool // IdmappedMounts indicates kernel support for VFS idmap.
	NativeTerminals         bool // NativeTerminals indicates support for TIOGPTPEER ioctl.
	NetnsGetifaddrs         bool // NetnsGetifaddrs indicates support for NETLINK_GET_STRICT_CHK.
	PidFds                  bool // PidFds indicates support for PID fds.
	PidFdSetns              bool // PidFdSetns indicates support for setns through PID fds.
	SeccompListenerAddfd    bool // SeccompListenerAddfd indicates support for passing new FD to process through seccomp notify.
	SeccompListener         bool // SeccompListener indicates support for seccomp notify.
	SeccompListenerContinue bool // SeccompListenerContinue indicates support continuing syscalls path for process through seccomp notify.
	UeventInjection         bool // UeventInjection indicates support for injecting uevents to a specific netns.
	UnprivBinfmt            bool // UnprivBinfmt indicates support for mounting binfmt_misc inside of a user namespace.
	VFS3Fscaps              bool // VFS3FScaps indicates support for v3 filesystem capabilities.

	// LXC features
	LXCFeatures map[string]bool

	// OS info
	ReleaseInfo   map[string]string
	KernelVersion version.DottedVersion
	Uname         *shared.Utsname
	BootTime      time.Time
}

// DefaultOS returns a fresh uninitialized OS instance with default values.
func DefaultOS() *OS {
	newOS := &OS{
		VarDir:   shared.VarPath(),
		CacheDir: shared.CachePath(),
		LogDir:   shared.LogPath(),
	}

	newOS.InotifyWatch.Fd = -1
	newOS.InotifyWatch.Targets = make(map[string]*InotifyTargetInfo)
	newOS.ReleaseInfo = make(map[string]string)
	return newOS
}

// Init our internal data structures.
func (s *OS) Init() ([]cluster.Warning, error) {
	var dbWarnings []cluster.Warning

	err := s.initDirs()
	if err != nil {
		return nil, err
	}

	s.Architectures, err = util.GetArchitectures()
	if err != nil {
		return nil, err
	}

	s.LxcPath = filepath.Join(s.VarDir, "containers")

	s.BackingFS, err = filesystem.Detect(s.LxcPath)
	if err != nil {
		logger.Error("Error detecting backing fs", logger.Ctx{"err": err})
	}

	// Detect if it is possible to run daemons as an unprivileged user and group.
	for _, userName := range []string{"lxd", "nobody"} {
		u, err := user.Lookup(userName)
		if err != nil {
			continue
		}

		uid, err := strconv.ParseUint(u.Uid, 10, 32)
		if err != nil {
			return nil, err
		}

		s.UnprivUser = userName
		s.UnprivUID = uint32(uid)
		break
	}

	for _, groupName := range []string{"lxd", "nogroup"} {
		g, err := user.LookupGroup(groupName)
		if err != nil {
			continue
		}

		gid, err := strconv.ParseUint(g.Gid, 10, 32)
		if err != nil {
			return nil, err
		}

		s.UnprivGroup = groupName
		s.UnprivGID = uint32(gid)
		break
	}

	s.IdmapSet = idmap.GetIdmapSet()
	s.ExecPath = util.GetExecPath()
	s.RunningInUserNS = shared.RunningInUserNS()

	dbWarnings = s.initAppArmor()
	cgroup.Init()
	s.CGInfo = cgroup.GetInfo()

	// Fill in the OS release info.
	osInfo, err := osarch.GetLSBRelease()
	if err != nil {
		return nil, err
	}

	s.ReleaseInfo = osInfo

	uname, err := shared.Uname()
	if err != nil {
		return nil, err
	}

	s.Uname = uname

	kernelVersion, err := version.Parse(uname.Release)
	if err == nil {
		s.KernelVersion = *kernelVersion
	}

	// Fill in the boot time.
	out, err := os.ReadFile("/proc/stat")
	if err != nil {
		return nil, err
	}

	btime := int64(0)
	for _, line := range strings.Split(string(out), "\n") {
		if !strings.HasPrefix(line, "btime ") {
			continue
		}

		fields := strings.Fields(line)
		btime, err = strconv.ParseInt(fields[1], 10, 64)
		if err != nil {
			return nil, err
		}

		break
	}

	if btime > 0 {
		s.BootTime = time.Unix(btime, 0)
	}

	return dbWarnings, nil
}

// InitStorage initialises the storage layer after it has been mounted.
func (s *OS) InitStorage() error {
	return s.initStorageDirs()
}

// InUbuntuCore returns true if we're running on Ubuntu Core.
func (s *OS) InUbuntuCore() bool {
	if !shared.InSnap() {
		return false
	}

	if s.ReleaseInfo["NAME"] == "Ubuntu Core" {
		return true
	}

	return false
}
