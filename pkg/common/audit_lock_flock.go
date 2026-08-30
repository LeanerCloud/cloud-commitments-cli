//go:build linux || darwin

package common

import (
	"errors"

	"golang.org/x/sys/unix"
)

func (f *auditOSFile) Lock() error {
	fd, err := auditFileDescriptor(f.Fd())
	if err != nil {
		return err
	}
	for {
		if err := unix.Flock(fd, unix.LOCK_EX); err != nil {
			if errors.Is(err, unix.EINTR) {
				continue
			}
			return err
		}
		return nil
	}
}

func (f *auditOSFile) Unlock() error {
	fd, err := auditFileDescriptor(f.Fd())
	if err != nil {
		return err
	}
	for {
		if err := unix.Flock(fd, unix.LOCK_UN); err != nil {
			if errors.Is(err, unix.EINTR) {
				continue
			}
			return err
		}
		return nil
	}
}
