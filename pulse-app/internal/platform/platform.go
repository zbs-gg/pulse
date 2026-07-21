package platform

import (
	"errors"
	"os"
	"time"
)

var (
	ErrUnsafe      = errors.New("unsafe private state")
	ErrUnsupported = errors.New("platform security contract is unsupported")
	ErrLockBusy    = errors.New("private state lock is busy")
)

type FilePolicy struct {
	MinimumBytes        int64
	MaximumBytes        int64
	RequireCurrentOwner bool
	ExpectedUID         *uint32
	AllowRootOwner      bool
	OwnerOnly           bool
	NoUntrustedWrite    bool
	SingleLink          bool
	Directory           bool
	Executable          bool
}

type FileInfo struct {
	Identity string
	ModTime  time.Time
	Size     int64
}

func EnsurePrivateDirectory(path string) error {
	return ensurePrivateDirectory(path)
}

func InspectPrivateDirectory(path string, missingOK bool) (bool, error) {
	return inspectPrivateDirectory(path, missingOK)
}

func ReadPrivateFile(path string, policy FilePolicy) ([]byte, error) {
	data, _, err := readPrivateFile(path, policy)
	return data, err
}

func ReadPrivateFileWithInfo(path string, policy FilePolicy) ([]byte, FileInfo, error) {
	return readPrivateFile(path, policy)
}

func InspectPrivateFile(path string, policy FilePolicy) (FileInfo, error) {
	return inspectPrivateFile(path, policy)
}

func CreatePrivateFileExclusive(path string, data []byte) (FileInfo, error) {
	return createPrivateFileExclusive(path, data)
}

func AtomicWritePrivateFile(path string, data []byte) error {
	return atomicWritePrivateFile(path, data)
}

func RemovePrivateFileIfIdentity(path, identity string) error {
	return removePrivateFileIfIdentity(path, identity)
}

func ValidatePrivatePath(path string, policy FilePolicy) error {
	return validatePrivatePath(path, policy)
}

func CurrentUserID() (uint32, bool) {
	return currentUserID()
}

func ShutdownSignals() []os.Signal {
	return shutdownSignals()
}
