//go:build !windows

package platform

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"syscall"
)

func currentUserID() (uint32, bool) {
	return uint32(os.Geteuid()), true
}

func rejectSymlinkAncestors(path string) error {
	absolute, err := filepath.Abs(path)
	if err != nil {
		return fmt.Errorf("%w: invalid path", ErrUnsafe)
	}
	current := string(filepath.Separator)
	for _, component := range splitPathComponents(absolute) {
		current = filepath.Join(current, component)
		info, inspectErr := os.Lstat(current)
		if errors.Is(inspectErr, os.ErrNotExist) {
			return nil
		}
		if inspectErr != nil {
			return inspectErr
		}
		if info.Mode()&os.ModeSymlink != 0 {
			// macOS exposes stable system roots such as /var and /tmp through
			// root-owned links. Accept only those immutable system links; a link
			// in a user-writable ancestor remains an escape and fails closed.
			linkStat, linkOK := info.Sys().(*syscall.Stat_t)
			parentInfo, parentErr := os.Stat(filepath.Dir(current))
			if parentErr != nil {
				return parentErr
			}
			parentStat, parentOK := parentInfo.Sys().(*syscall.Stat_t)
			if !linkOK || !parentOK || linkStat.Uid != 0 || parentStat.Uid != 0 ||
				parentInfo.Mode().Perm()&0o022 != 0 {
				return fmt.Errorf("%w: symbolic-link path component", ErrUnsafe)
			}
		}
	}
	return nil
}

func splitPathComponents(path string) []string {
	volume := filepath.VolumeName(path)
	remainder := path[len(volume):]
	components := make([]string, 0, 8)
	for remainder != "" && remainder != string(filepath.Separator) {
		remainder = filepath.Clean(remainder)
		if remainder == string(filepath.Separator) || remainder == "." {
			break
		}
		directory, base := filepath.Split(remainder)
		if base == "" {
			break
		}
		components = append([]string{base}, components...)
		remainder = filepath.Clean(directory)
	}
	return components
}

func ensurePrivateDirectory(path string) error {
	if path == "" || !filepath.IsAbs(path) {
		return fmt.Errorf("%w: private directory must be absolute", ErrUnsafe)
	}
	if err := rejectSymlinkAncestors(path); err != nil {
		return err
	}
	if info, err := os.Lstat(path); err == nil {
		if _, err := validatePOSIXInfo(info, FilePolicy{
			Directory: true, RequireCurrentOwner: true,
		}); err != nil {
			return err
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if err := os.MkdirAll(path, 0o700); err != nil {
		return err
	}
	if err := os.Chmod(path, 0o700); err != nil {
		return err
	}
	if err := rejectSymlinkAncestors(path); err != nil {
		return err
	}
	_, err := inspectPrivateDirectory(path, false)
	return err
}

func inspectPrivateDirectory(path string, missingOK bool) (bool, error) {
	if path == "" || !filepath.IsAbs(path) {
		return false, fmt.Errorf("%w: private directory must be absolute", ErrUnsafe)
	}
	if err := rejectSymlinkAncestors(path); err != nil {
		return false, err
	}
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) && missingOK {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	policy := FilePolicy{Directory: true, RequireCurrentOwner: true, OwnerOnly: true}
	if _, err := validatePOSIXInfo(info, policy); err != nil {
		return false, err
	}
	return true, nil
}

func validatePOSIXInfo(info os.FileInfo, policy FilePolicy) (FileInfo, error) {
	if info == nil || info.Mode()&os.ModeSymlink != 0 {
		return FileInfo{}, fmt.Errorf("%w: link or missing file", ErrUnsafe)
	}
	if policy.Directory {
		if !info.IsDir() {
			return FileInfo{}, fmt.Errorf("%w: not a directory", ErrUnsafe)
		}
	} else if !info.Mode().IsRegular() {
		return FileInfo{}, fmt.Errorf("%w: not a regular file", ErrUnsafe)
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return FileInfo{}, fmt.Errorf("%w: unavailable file identity", ErrUnsafe)
	}
	uid := uint32(stat.Uid)
	if policy.ExpectedUID != nil && uid != *policy.ExpectedUID {
		return FileInfo{}, fmt.Errorf("%w: owner mismatch", ErrUnsafe)
	}
	if policy.RequireCurrentOwner && uid != uint32(os.Geteuid()) && !(policy.AllowRootOwner && uid == 0) {
		return FileInfo{}, fmt.Errorf("%w: owner mismatch", ErrUnsafe)
	}
	if policy.OwnerOnly && info.Mode().Perm()&0o077 != 0 {
		return FileInfo{}, fmt.Errorf("%w: owner-only permissions required", ErrUnsafe)
	}
	if policy.NoUntrustedWrite && info.Mode().Perm()&0o022 != 0 {
		return FileInfo{}, fmt.Errorf("%w: group/other writable", ErrUnsafe)
	}
	if policy.SingleLink && !policy.Directory && uint64(stat.Nlink) != 1 {
		return FileInfo{}, fmt.Errorf("%w: hard-linked file", ErrUnsafe)
	}
	if policy.Executable && info.Mode().Perm()&0o111 == 0 {
		return FileInfo{}, fmt.Errorf("%w: file is not executable", ErrUnsafe)
	}
	if policy.MinimumBytes > 0 && info.Size() < policy.MinimumBytes {
		return FileInfo{}, fmt.Errorf("%w: file is too small", ErrUnsafe)
	}
	if policy.MaximumBytes > 0 && info.Size() > policy.MaximumBytes {
		return FileInfo{}, fmt.Errorf("%w: file is too large", ErrUnsafe)
	}
	return FileInfo{
		Identity: fmt.Sprintf("%d:%d", uint64(stat.Dev), uint64(stat.Ino)),
		ModTime:  info.ModTime(), Size: info.Size(),
	}, nil
}

func openPrivateFile(path string, flags int) (*os.File, error) {
	if path == "" || !filepath.IsAbs(path) {
		return nil, fmt.Errorf("%w: private file path must be absolute", ErrUnsafe)
	}
	if err := rejectSymlinkAncestors(filepath.Dir(path)); err != nil {
		return nil, err
	}
	fd, err := syscall.Open(path, flags|syscall.O_NOFOLLOW|syscall.O_CLOEXEC, 0)
	if err != nil {
		if errors.Is(err, syscall.ELOOP) {
			return nil, fmt.Errorf("%w: symbolic link", ErrUnsafe)
		}
		return nil, err
	}
	file := os.NewFile(uintptr(fd), path)
	if file == nil {
		_ = syscall.Close(fd)
		return nil, fmt.Errorf("%w: open private file", ErrUnsafe)
	}
	return file, nil
}

func readPrivateFile(path string, policy FilePolicy) ([]byte, FileInfo, error) {
	if policy.MaximumBytes < 1 {
		return nil, FileInfo{}, fmt.Errorf("%w: bounded read required", ErrUnsafe)
	}
	file, err := openPrivateFile(path, syscall.O_RDONLY)
	if err != nil {
		return nil, FileInfo{}, err
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return nil, FileInfo{}, err
	}
	details, err := validatePOSIXInfo(info, policy)
	if err != nil {
		return nil, FileInfo{}, err
	}
	data, err := io.ReadAll(io.LimitReader(file, policy.MaximumBytes+1))
	if err != nil {
		return nil, FileInfo{}, err
	}
	if int64(len(data)) > policy.MaximumBytes || int64(len(data)) < policy.MinimumBytes {
		return nil, FileInfo{}, fmt.Errorf("%w: invalid file size", ErrUnsafe)
	}
	return data, details, nil
}

func inspectPrivateFile(path string, policy FilePolicy) (FileInfo, error) {
	file, err := openPrivateFile(path, syscall.O_RDONLY)
	if err != nil {
		return FileInfo{}, err
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return FileInfo{}, err
	}
	return validatePOSIXInfo(info, policy)
}

func createPrivateFileExclusive(path string, data []byte) (FileInfo, error) {
	parent, err := os.Lstat(filepath.Dir(path))
	if err != nil {
		return FileInfo{}, err
	}
	if _, err := validatePOSIXInfo(parent, FilePolicy{
		Directory: true, RequireCurrentOwner: true, OwnerOnly: true,
	}); err != nil || parent.Mode().Perm()&0o200 == 0 {
		return FileInfo{}, fmt.Errorf("%w: private directory is not writable", ErrUnsafe)
	}
	file, err := openPrivateFile(path, syscall.O_WRONLY|syscall.O_CREAT|syscall.O_EXCL)
	if err != nil {
		if errors.Is(err, syscall.EEXIST) {
			return FileInfo{}, os.ErrExist
		}
		return FileInfo{}, err
	}
	removeOnFailure := true
	defer func() {
		_ = file.Close()
		if removeOnFailure {
			_ = os.Remove(path)
		}
	}()
	if err := file.Chmod(0o600); err != nil {
		return FileInfo{}, err
	}
	if _, err := file.Write(data); err != nil {
		return FileInfo{}, err
	}
	if err := file.Sync(); err != nil {
		return FileInfo{}, err
	}
	info, err := file.Stat()
	if err != nil {
		return FileInfo{}, err
	}
	details, err := validatePOSIXInfo(info, FilePolicy{
		RequireCurrentOwner: true, OwnerOnly: true, SingleLink: true,
	})
	if err != nil {
		return FileInfo{}, err
	}
	if err := file.Close(); err != nil {
		return FileInfo{}, err
	}
	removeOnFailure = false
	return details, nil
}

func atomicWritePrivateFile(path string, data []byte) error {
	if path == "" || !filepath.IsAbs(path) {
		return fmt.Errorf("%w: private file path must be absolute", ErrUnsafe)
	}
	if existing, err := os.Lstat(path); err == nil {
		if _, validateErr := validatePOSIXInfo(existing, FilePolicy{
			RequireCurrentOwner: true, OwnerOnly: true, SingleLink: true,
		}); validateErr != nil {
			return validateErr
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	var randomBytes [12]byte
	if _, err := rand.Read(randomBytes[:]); err != nil {
		return err
	}
	temporary := path + "." + hex.EncodeToString(randomBytes[:]) + ".new"
	if _, err := createPrivateFileExclusive(temporary, data); err != nil {
		return err
	}
	defer os.Remove(temporary)
	if err := os.Rename(temporary, path); err != nil {
		return err
	}
	directory, err := os.Open(filepath.Dir(path))
	if err != nil {
		return err
	}
	err = directory.Sync()
	closeErr := directory.Close()
	if err != nil {
		return err
	}
	return closeErr
}

func removePrivateFileIfIdentity(path, identity string) error {
	info, err := inspectPrivateFile(path, FilePolicy{
		RequireCurrentOwner: true, OwnerOnly: true, SingleLink: true,
	})
	if err != nil {
		return err
	}
	if info.Identity != identity {
		return fmt.Errorf("%w: file identity changed", ErrUnsafe)
	}
	return os.Remove(path)
}

func validatePrivatePath(path string, policy FilePolicy) error {
	if policy.Directory {
		if path == "" || !filepath.IsAbs(path) {
			return fmt.Errorf("%w: path must be absolute", ErrUnsafe)
		}
		if err := rejectSymlinkAncestors(path); err != nil {
			return err
		}
		info, err := os.Lstat(path)
		if err != nil {
			return err
		}
		_, err = validatePOSIXInfo(info, policy)
		return err
	}
	_, err := inspectPrivateFile(path, policy)
	return err
}
