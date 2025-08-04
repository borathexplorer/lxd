package apparmor

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/canonical/lxd/lxd/sys"
	"github.com/canonical/lxd/shared"
	"github.com/canonical/lxd/shared/version"
)

const (
	cmdLoad   = "--replace"
	cmdUnload = "--remove"
	cmdParse  = "--skip-kernel-load"
)

var aaPath = shared.VarPath("security", "apparmor")

// runApparmor runs the relevant AppArmor command.
func runApparmor(sysOS *sys.OS, command string, name string) error {
	if !sysOS.AppArmorAvailable {
		return nil
	}

	_, err := shared.RunCommandContext(context.TODO(), "apparmor_parser", []string{
		command,
		"--write-cache", "--cache-loc", filepath.Join(aaPath, "cache"),
		filepath.Join(aaPath, "profiles", name),
	}...)

	if err != nil {
		return err
	}

	return nil
}

// createNamespace creates a new AppArmor namespace.
func createNamespace(sysOS *sys.OS, name string) error {
	if !sysOS.AppArmorAvailable {
		return nil
	}

	if !sysOS.AppArmorStacking || sysOS.AppArmorStacked {
		return nil
	}

	p := filepath.Join("/sys/kernel/security/apparmor/policy/namespaces", name)
	err := os.Mkdir(p, 0755)
	if err != nil && !os.IsExist(err) {
		return err
	}

	return nil
}

// deleteNamespace destroys an AppArmor namespace.
func deleteNamespace(sysOS *sys.OS, name string) error {
	if !sysOS.AppArmorAvailable {
		return nil
	}

	if !sysOS.AppArmorStacking || sysOS.AppArmorStacked {
		return nil
	}

	p := filepath.Join("/sys/kernel/security/apparmor/policy/namespaces", name)
	err := os.Remove(p)
	if err != nil && !os.IsNotExist(err) {
		return err
	}

	return nil
}

// hasProfile checks if the profile is already loaded.
func hasProfile(name string) (bool, error) {
	mangled := strings.ReplaceAll(strings.ReplaceAll(strings.ReplaceAll(name, "/", "."), "<", ""), ">", "")

	profilesPath := "/sys/kernel/security/apparmor/policy/profiles"
	if shared.PathExists(profilesPath) {
		entries, err := os.ReadDir(profilesPath)
		if err != nil {
			return false, err
		}

		for _, entry := range entries {
			fields := strings.Split(entry.Name(), ".")
			if mangled == strings.Join(fields[0:len(fields)-1], ".") {
				return true, nil
			}
		}
	}

	return false, nil
}

// parseProfile parses the profile without loading it into the kernel.
func parseProfile(sysOS *sys.OS, name string) error {
	if !sysOS.AppArmorAvailable {
		return nil
	}

	return runApparmor(sysOS, cmdParse, name)
}

// loadProfile loads the AppArmor profile into the kernel.
func loadProfile(sysOS *sys.OS, name string) error {
	if !sysOS.AppArmorAdmin {
		return nil
	}

	return runApparmor(sysOS, cmdLoad, name)
}

// unloadProfile removes the profile from the kernel.
func unloadProfile(sysOS *sys.OS, fullName string, name string) error {
	if !sysOS.AppArmorAvailable {
		return nil
	}

	ok, err := hasProfile(fullName)
	if err != nil {
		return err
	}

	if !ok {
		return nil
	}

	return runApparmor(sysOS, cmdUnload, name)
}

// deleteProfile unloads and delete profile and cache for a profile.
func deleteProfile(sysOS *sys.OS, fullName string, name string) error {
	if !sysOS.AppArmorAdmin {
		return nil
	}

	cacheDir, err := getCacheDir(sysOS)
	if err != nil {
		return err
	}

	err = unloadProfile(sysOS, fullName, name)
	if err != nil {
		return err
	}

	cachePath := filepath.Join(cacheDir, name)
	err = os.Remove(cachePath)
	if err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("Failed to remove %s: %w", cachePath, err)
	}

	profilePath := filepath.Join(aaPath, "profiles", name)
	err = os.Remove(profilePath)
	if err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("Failed to remove %s: %w", profilePath, err)
	}

	return nil
}

// parserSupports checks if the parser supports a particular feature.
func parserSupports(sysOS *sys.OS, feature string) (bool, error) {
	if !sysOS.AppArmorAvailable {
		return false, nil
	}

	ver, err := getVersion(sysOS)
	if err != nil {
		return false, err
	}

	if feature == "unix" {
		minVer, err := version.NewDottedVersion("2.10.95")
		if err != nil {
			return false, err
		}

		return ver.Compare(minVer) >= 0, nil
	}

	if feature == "mount_nosymfollow" || feature == "userns_rule" {
		sysOS.AppArmorFeatures.Lock()
		defer sysOS.AppArmorFeatures.Unlock()
		supported, ok := sysOS.AppArmorFeatures.Map[feature]
		if !ok {
			supported, err = FeatureCheck(sysOS, feature)
			if err != nil {
				return false, nil
			}

			sysOS.AppArmorFeatures.Map[feature] = supported
		}

		return supported, nil
	}

	return false, nil
}

// getVersion reads and parses the AppArmor version.
func getVersion(sysOS *sys.OS) (*version.DottedVersion, error) {
	if !sysOS.AppArmorAvailable {
		return version.NewDottedVersion("0.0")
	}

	out, err := shared.RunCommandContext(context.TODO(), "apparmor_parser", "--version")
	if err != nil {
		return nil, err
	}

	fields := strings.Fields(strings.Split(out, "\n")[0])
	return version.Parse(fields[len(fields)-1])
}

// getCacheDir returns the applicable AppArmor cache directory.
func getCacheDir(sysOS *sys.OS) (string, error) {
	basePath := filepath.Join(aaPath, "cache")

	if !sysOS.AppArmorAvailable {
		return basePath, nil
	}

	ver, err := getVersion(sysOS)
	if err != nil {
		return "", err
	}

	// Multiple policy cache directories were only added in v2.13.
	minVer, err := version.NewDottedVersion("2.13")
	if err != nil {
		return "", err
	}

	if ver.Compare(minVer) < 0 {
		return basePath, nil
	}

	output, err := shared.RunCommandContext(context.TODO(), "apparmor_parser", "-L", basePath, "--print-cache-dir")
	if err != nil {
		return "", err
	}

	return strings.TrimSpace(output), nil
}

// profileName handles generating valid profile names.
func profileName(prefix string, name string) string {
	separators := 1
	if len(prefix) > 0 {
		separators = 2
	}

	// Max length in AppArmor is 253 chars.
	if len(name)+len(prefix)+3+separators >= 253 {
		hash := sha256.New()
		_, _ = io.WriteString(hash, name)
		name = hex.EncodeToString(hash.Sum(nil))
	}

	if len(prefix) > 0 {
		return "lxd_" + prefix + "-" + name
	}

	return "lxd-" + name
}
