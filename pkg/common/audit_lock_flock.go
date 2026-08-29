//go:build linux || darwin

package common

import (
	"errors"

	"golang.org/x/sys/unix"
)

func (f *auditOSFile) Lock() error {
	for {
		if err := unix.Flock(int(f.Fd()), unix.LOCK_EX); err != nil {
			if errors.Is(err, unix.EINTR) {
				continue
			}
			return err
		}
		return nil
	}
}

func (f *auditOSFile) Unlock() error {
	for {
		if err := unix.Flock(int(f.Fd()), unix.LOCK_UN); err != nil {
			if errors.Is(err, unix.EINTR) {
				continue
			}
			return err
		}
		return nil
	}
}
