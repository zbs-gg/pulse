//go:build windows

package platform

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

func currentUserID() (uint32, bool) {
	return 0, false
}

func currentUserSID() (*windows.SID, error) {
	user, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil || user == nil || user.User.Sid == nil {
		return nil, fmt.Errorf("%w: current Windows user", ErrUnsafe)
	}
	return user.User.Sid, nil
}

func privateSecurityAttributes(directory bool) (*windows.SecurityAttributes, error) {
	user, err := currentUserSID()
	if err != nil {
		return nil, err
	}
	inheritance := ""
	if directory {
		inheritance = "OICI"
	}
	sddl := fmt.Sprintf("O:%sD:P(A;%s;FA;;;SY)(A;%s;FA;;;BA)(A;%s;FA;;;%s)",
		user.String(), inheritance, inheritance, inheritance, user.String())
	descriptor, err := windows.SecurityDescriptorFromString(sddl)
	if err != nil {
		return nil, err
	}
	return &windows.SecurityAttributes{
		Length: uint32(unsafe.Sizeof(windows.SecurityAttributes{})), SecurityDescriptor: descriptor,
	}, nil
}

func windowsPathParts(path string) (string, []string, error) {
	absolute, err := filepath.Abs(path)
	if err != nil || !filepath.IsAbs(absolute) {
		return "", nil, fmt.Errorf("%w: absolute Windows path required", ErrUnsafe)
	}
	absolute = filepath.Clean(absolute)
	volume := filepath.VolumeName(absolute)
	if volume == "" {
		return "", nil, fmt.Errorf("%w: Windows volume required", ErrUnsafe)
	}
	remainder := strings.TrimPrefix(absolute, volume)
	remainder = strings.TrimLeft(remainder, `\/`)
	parts := strings.FieldsFunc(remainder, func(char rune) bool { return char == '\\' || char == '/' })
	return volume + string(filepath.Separator), parts, nil
}

func rejectWindowsReparseAncestors(path string) error {
	root, parts, err := windowsPathParts(path)
	if err != nil {
		return err
	}
	current := root
	for _, part := range parts {
		current = filepath.Join(current, part)
		pointer, err := windows.UTF16PtrFromString(current)
		if err != nil {
			return err
		}
		attributes, err := windows.GetFileAttributes(pointer)
		if errors.Is(err, windows.ERROR_FILE_NOT_FOUND) || errors.Is(err, windows.ERROR_PATH_NOT_FOUND) {
			return nil
		}
		if err != nil {
			return err
		}
		if attributes&windows.FILE_ATTRIBUTE_REPARSE_POINT != 0 {
			return fmt.Errorf("%w: reparse-point path component", ErrUnsafe)
		}
	}
	return nil
}

func ensurePrivateDirectory(path string) error {
	root, parts, err := windowsPathParts(path)
	if err != nil {
		return err
	}
	current := root
	for _, part := range parts {
		current = filepath.Join(current, part)
		pointer, err := windows.UTF16PtrFromString(current)
		if err != nil {
			return err
		}
		attributes, inspectErr := windows.GetFileAttributes(pointer)
		if inspectErr == nil {
			if attributes&windows.FILE_ATTRIBUTE_REPARSE_POINT != 0 || attributes&windows.FILE_ATTRIBUTE_DIRECTORY == 0 {
				return fmt.Errorf("%w: unsafe private directory component", ErrUnsafe)
			}
			continue
		}
		if !errors.Is(inspectErr, windows.ERROR_FILE_NOT_FOUND) && !errors.Is(inspectErr, windows.ERROR_PATH_NOT_FOUND) {
			return inspectErr
		}
		security, err := privateSecurityAttributes(true)
		if err != nil {
			return err
		}
		if err := windows.CreateDirectory(pointer, security); err != nil && !errors.Is(err, windows.ERROR_ALREADY_EXISTS) {
			return err
		}
	}
	_, err = inspectPrivateDirectory(path, false)
	return err
}

func inspectPrivateDirectory(path string, missingOK bool) (bool, error) {
	policy := FilePolicy{Directory: true, RequireCurrentOwner: true, OwnerOnly: true}
	_, err := validateWindowsPath(path, policy)
	if missingOK && (errors.Is(err, os.ErrNotExist) || errors.Is(err, windows.ERROR_FILE_NOT_FOUND) || errors.Is(err, windows.ERROR_PATH_NOT_FOUND)) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return true, nil
}

func openWindowsPath(path string, directory, write, exclusive bool, security *windows.SecurityAttributes) (windows.Handle, error) {
	if path == "" || !filepath.IsAbs(path) {
		return windows.InvalidHandle, fmt.Errorf("%w: absolute Windows path required", ErrUnsafe)
	}
	if err := rejectWindowsReparseAncestors(filepath.Dir(path)); err != nil {
		return windows.InvalidHandle, err
	}
	pointer, err := windows.UTF16PtrFromString(filepath.Clean(path))
	if err != nil {
		return windows.InvalidHandle, err
	}
	access := uint32(windows.GENERIC_READ | windows.READ_CONTROL)
	creation := uint32(windows.OPEN_EXISTING)
	share := uint32(windows.FILE_SHARE_READ | windows.FILE_SHARE_WRITE | windows.FILE_SHARE_DELETE)
	attributes := uint32(windows.FILE_FLAG_OPEN_REPARSE_POINT)
	if directory {
		access = windows.FILE_LIST_DIRECTORY | windows.FILE_READ_ATTRIBUTES | windows.READ_CONTROL
		attributes |= windows.FILE_FLAG_BACKUP_SEMANTICS
	}
	if write {
		access |= windows.GENERIC_WRITE
		share = windows.FILE_SHARE_READ
	}
	if exclusive {
		creation = windows.CREATE_NEW
		attributes |= windows.FILE_ATTRIBUTE_NORMAL | windows.FILE_FLAG_WRITE_THROUGH
	}
	handle, err := windows.CreateFile(pointer, access, share, security, creation, attributes, 0)
	if errors.Is(err, windows.ERROR_FILE_NOT_FOUND) || errors.Is(err, windows.ERROR_PATH_NOT_FOUND) {
		return windows.InvalidHandle, os.ErrNotExist
	}
	if exclusive && (errors.Is(err, windows.ERROR_FILE_EXISTS) || errors.Is(err, windows.ERROR_ALREADY_EXISTS)) {
		return windows.InvalidHandle, os.ErrExist
	}
	if err != nil {
		return windows.InvalidHandle, err
	}
	return handle, nil
}

func validateWindowsHandle(handle windows.Handle, path string, policy FilePolicy) (FileInfo, error) {
	if policy.ExpectedUID != nil {
		return FileInfo{}, ErrUnsupported
	}
	var raw windows.ByHandleFileInformation
	if err := windows.GetFileInformationByHandle(handle, &raw); err != nil {
		return FileInfo{}, err
	}
	if raw.FileAttributes&windows.FILE_ATTRIBUTE_REPARSE_POINT != 0 {
		return FileInfo{}, fmt.Errorf("%w: reparse point", ErrUnsafe)
	}
	isDirectory := raw.FileAttributes&windows.FILE_ATTRIBUTE_DIRECTORY != 0
	if policy.Directory != isDirectory {
		return FileInfo{}, fmt.Errorf("%w: unexpected path kind", ErrUnsafe)
	}
	if policy.SingleLink && !isDirectory && raw.NumberOfLinks != 1 {
		return FileInfo{}, fmt.Errorf("%w: hard-linked file", ErrUnsafe)
	}
	size := int64(uint64(raw.FileSizeHigh)<<32 | uint64(raw.FileSizeLow))
	if policy.MinimumBytes > 0 && size < policy.MinimumBytes {
		return FileInfo{}, fmt.Errorf("%w: file is too small", ErrUnsafe)
	}
	if policy.MaximumBytes > 0 && size > policy.MaximumBytes {
		return FileInfo{}, fmt.Errorf("%w: file is too large", ErrUnsafe)
	}
	if policy.Executable && !isWindowsExecutable(path) {
		return FileInfo{}, fmt.Errorf("%w: file is not executable", ErrUnsafe)
	}
	if policy.RequireCurrentOwner || policy.AllowRootOwner || policy.OwnerOnly || policy.NoUntrustedWrite {
		if err := validateWindowsSecurity(handle, policy); err != nil {
			return FileInfo{}, err
		}
	}
	return FileInfo{
		Identity: fmt.Sprintf("%08x:%08x%08x", raw.VolumeSerialNumber, raw.FileIndexHigh, raw.FileIndexLow),
		ModTime:  time.Unix(0, raw.LastWriteTime.Nanoseconds()), Size: size,
	}, nil
}

func validateWindowsSecurity(handle windows.Handle, policy FilePolicy) error {
	descriptor, err := windows.GetSecurityInfo(handle, windows.SE_FILE_OBJECT,
		windows.OWNER_SECURITY_INFORMATION|windows.DACL_SECURITY_INFORMATION)
	if err != nil {
		return err
	}
	owner, _, err := descriptor.Owner()
	if err != nil || owner == nil {
		return fmt.Errorf("%w: missing owner", ErrUnsafe)
	}
	user, err := currentUserSID()
	if err != nil {
		return err
	}
	dacl, _, err := descriptor.DACL()
	if err != nil || dacl == nil {
		return fmt.Errorf("%w: missing DACL", ErrUnsafe)
	}
	system, err := windows.CreateWellKnownSid(windows.WinLocalSystemSid)
	if err != nil {
		return err
	}
	administrators, err := windows.CreateWellKnownSid(windows.WinBuiltinAdministratorsSid)
	if err != nil {
		return err
	}
	currentOwner := owner.Equals(user)
	rootOwner := owner.Equals(system) || owner.Equals(administrators)
	if policy.RequireCurrentOwner && !currentOwner && !(policy.AllowRootOwner && rootOwner) {
		return fmt.Errorf("%w: owner mismatch", ErrUnsafe)
	}
	if !policy.RequireCurrentOwner && policy.AllowRootOwner && !rootOwner {
		return fmt.Errorf("%w: trusted OS owner required", ErrUnsafe)
	}
	writeMask := windows.ACCESS_MASK(windows.GENERIC_WRITE | windows.GENERIC_ALL | windows.DELETE |
		windows.WRITE_DAC | windows.WRITE_OWNER | windows.FILE_WRITE_DATA | windows.FILE_APPEND_DATA |
		windows.FILE_WRITE_ATTRIBUTES | windows.FILE_WRITE_EA)
	for index := uint16(0); index < dacl.AceCount; index++ {
		var ace *windows.ACCESS_ALLOWED_ACE
		if err := windows.GetAce(dacl, uint32(index), &ace); err != nil || ace == nil {
			return fmt.Errorf("%w: invalid DACL", ErrUnsafe)
		}
		if ace.Header.AceType == windows.ACCESS_DENIED_ACE_TYPE {
			continue
		}
		if ace.Header.AceType != windows.ACCESS_ALLOWED_ACE_TYPE {
			return fmt.Errorf("%w: unsupported allow ACE", ErrUnsafe)
		}
		sid := (*windows.SID)(unsafe.Pointer(&ace.SidStart))
		trusted := sid.IsValid() && (sid.Equals(user) || sid.Equals(system) || sid.Equals(administrators))
		if !trusted && ((policy.OwnerOnly && ace.Mask != 0) || (policy.NoUntrustedWrite && ace.Mask&writeMask != 0)) {
			return fmt.Errorf("%w: DACL grants untrusted access", ErrUnsafe)
		}
	}
	return nil
}

func isWindowsExecutable(path string) bool {
	switch strings.ToLower(filepath.Ext(path)) {
	case ".exe", ".com", ".bat", ".cmd":
		return true
	default:
		return false
	}
}

func validateWindowsPath(path string, policy FilePolicy) (FileInfo, error) {
	handle, err := openWindowsPath(path, policy.Directory, false, false, nil)
	if err != nil {
		return FileInfo{}, err
	}
	defer windows.CloseHandle(handle)
	return validateWindowsHandle(handle, path, policy)
}

// InspectWindowsPathIdentity returns an opaque volume/file identity after
// rejecting reparse points and enforcing the requested path kind. It exists
// for the pre-download npm bootstrap adapter; callers must not interpret the
// token or use it as an authorization decision by itself.
func InspectWindowsPathIdentity(path string, directory bool) (FileInfo, error) {
	return validateWindowsPath(path, FilePolicy{Directory: directory})
}

func readPrivateFile(path string, policy FilePolicy) ([]byte, FileInfo, error) {
	if policy.MaximumBytes < 1 || policy.Directory {
		return nil, FileInfo{}, fmt.Errorf("%w: bounded file read required", ErrUnsafe)
	}
	handle, err := openWindowsPath(path, false, false, false, nil)
	if err != nil {
		return nil, FileInfo{}, err
	}
	file := os.NewFile(uintptr(handle), path)
	if file == nil {
		_ = windows.CloseHandle(handle)
		return nil, FileInfo{}, ErrUnsafe
	}
	defer file.Close()
	details, err := validateWindowsHandle(handle, path, policy)
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
	return validateWindowsPath(path, policy)
}

func createPrivateFileExclusive(path string, data []byte) (FileInfo, error) {
	security, err := privateSecurityAttributes(false)
	if err != nil {
		return FileInfo{}, err
	}
	handle, err := openWindowsPath(path, false, true, true, security)
	if err != nil {
		return FileInfo{}, err
	}
	file := os.NewFile(uintptr(handle), path)
	if file == nil {
		_ = windows.CloseHandle(handle)
		_ = os.Remove(path)
		return FileInfo{}, ErrUnsafe
	}
	removeOnFailure := true
	defer func() {
		_ = file.Close()
		if removeOnFailure {
			_ = os.Remove(path)
		}
	}()
	if _, err := file.Write(data); err != nil {
		return FileInfo{}, err
	}
	if err := file.Sync(); err != nil {
		return FileInfo{}, err
	}
	details, err := validateWindowsHandle(handle, path, FilePolicy{
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
		return fmt.Errorf("%w: absolute path required", ErrUnsafe)
	}
	if _, err := inspectPrivateFile(path, FilePolicy{
		RequireCurrentOwner: true, OwnerOnly: true, SingleLink: true,
	}); err != nil && !errors.Is(err, os.ErrNotExist) {
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
	from, err := windows.UTF16PtrFromString(temporary)
	if err != nil {
		return err
	}
	to, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return err
	}
	if err := windows.MoveFileEx(from, to, windows.MOVEFILE_REPLACE_EXISTING|windows.MOVEFILE_WRITE_THROUGH); err != nil {
		return err
	}
	_, err = inspectPrivateFile(path, FilePolicy{
		RequireCurrentOwner: true, OwnerOnly: true, SingleLink: true,
	})
	return err
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
	pointer, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return err
	}
	if err := windows.DeleteFile(pointer); errors.Is(err, windows.ERROR_FILE_NOT_FOUND) {
		return os.ErrNotExist
	} else {
		return err
	}
}

func validatePrivatePath(path string, policy FilePolicy) error {
	_, err := validateWindowsPath(path, policy)
	return err
}
