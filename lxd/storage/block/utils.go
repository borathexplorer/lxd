package block

import (
	"os"

	"golang.org/x/sys/unix"

	"github.com/canonical/lxd/shared"
)

// DiskSizeBytes returns the size of a block disk (path can be either block device or raw file).
func DiskSizeBytes(blockDiskPath string) (int64, error) {
	if shared.IsBlockdevPath(blockDiskPath) {
		// Attempt to open the device path.
		f, err := os.Open(blockDiskPath)
		if err != nil {
			return -1, err
		}

		defer func() { _ = f.Close() }()
		fd := int(f.Fd())

		// Retrieve the block device size.
		res, err := unix.IoctlGetInt(fd, unix.BLKGETSIZE64)
		if err != nil {
			return -1, err
		}

		return int64(res), nil
	}

	// Block device is assumed to be a raw file.
	fi, err := os.Lstat(blockDiskPath)
	if err != nil {
		return -1, err
	}

	return fi.Size(), nil
}

// DiskBlockSize returns the physical block size of a block device.
func DiskBlockSize(path string) (uint32, error) {
	f, err := os.Open(path)
	if err != nil {
		return 0, err
	}

	defer func() { _ = f.Close() }()
	fd := int(f.Fd())

	// Retrieve the physical block size.
	res, err := unix.IoctlGetUint32(fd, unix.BLKPBSZGET)
	if err != nil {
		return 0, err
	}

	return res, nil
}
