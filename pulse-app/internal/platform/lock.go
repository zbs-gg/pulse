package platform

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"strings"
	"time"
)

const lockSchema = "pulse.platform_lock.v1"

type lockRecord struct {
	Schema          string `json:"schema"`
	PID             int    `json:"pid"`
	Token           string `json:"token"`
	ProcessIdentity string `json:"process_identity"`
}

type Lock struct {
	path     string
	identity string
	token    string
}

func AcquireLock(path string, wait, staleAfter time.Duration) (*Lock, error) {
	if path == "" || wait < 0 || staleAfter <= 0 {
		return nil, ErrUnsafe
	}
	processIdentity, err := ProcessIdentity(os.Getpid())
	if err != nil || processIdentity == "" {
		return nil, ErrUnsafe
	}
	var tokenBytes [16]byte
	if _, err := rand.Read(tokenBytes[:]); err != nil {
		return nil, ErrUnsafe
	}
	record := lockRecord{
		Schema: lockSchema, PID: os.Getpid(), Token: hex.EncodeToString(tokenBytes[:]),
		ProcessIdentity: processIdentity,
	}
	raw, err := json.Marshal(record)
	if err != nil {
		return nil, ErrUnsafe
	}
	raw = append(raw, '\n')
	deadline := time.Now().Add(wait)
	policy := FilePolicy{
		MinimumBytes: 1, MaximumBytes: 512, RequireCurrentOwner: true, OwnerOnly: true, SingleLink: true,
	}
	for {
		info, createErr := CreatePrivateFileExclusive(path, raw)
		if createErr == nil {
			return &Lock{path: path, identity: info.Identity, token: record.Token}, nil
		}
		if !errors.Is(createErr, os.ErrExist) {
			return nil, ErrUnsafe
		}

		data, current, readErr := ReadPrivateFileWithInfo(path, policy)
		if readErr == nil {
			var holder lockRecord
			valid := json.Unmarshal(data, &holder) == nil && holder.Schema == lockSchema && holder.PID > 0 &&
				len(holder.Token) == 32 && strings.TrimSpace(holder.ProcessIdentity) == holder.ProcessIdentity &&
				holder.ProcessIdentity != ""
			if time.Since(current.ModTime) > staleAfter && (!valid || !ProcessAlive(holder.PID, holder.ProcessIdentity)) {
				if removeErr := RemovePrivateFileIfIdentity(path, current.Identity); removeErr == nil || errors.Is(removeErr, os.ErrNotExist) {
					continue
				}
			}
		} else if errors.Is(readErr, os.ErrNotExist) {
			continue
		} else if errors.Is(readErr, ErrUnsafe) {
			return nil, ErrUnsafe
		}

		if !time.Now().Before(deadline) {
			return nil, ErrLockBusy
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func (lock *Lock) Release() error {
	if lock == nil || lock.path == "" || lock.identity == "" || lock.token == "" {
		return nil
	}
	err := RemovePrivateFileIfIdentity(lock.path, lock.identity)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	return err
}
