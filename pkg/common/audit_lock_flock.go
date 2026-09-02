//go:build linux || darwin

package common

import (
	"errors"
	"fmt"
	"time"

	"golang.org/x/sys/unix"
)

const (
	auditLockTimeout       = 5 * time.Second
	auditLockRetryInterval = 10 * time.Millisecond
)

func (f *auditOSFile) Lock() error {
	fd, err := auditFileDescriptor(f.Fd())
	if err != nil {
		return err
	}
	deadline := time.Now().Add(auditLockTimeout)
	var lastErr error
	for {
		remaining := time.Until(deadline)
		if remaining <= 0 {
			return auditLockTimeoutError(lastErr)
		}

		err := unix.Flock(fd, unix.LOCK_EX|unix.LOCK_NB)
		if err == nil {
			if time.Until(deadline) <= 0 {
				timeoutErr := auditLockTimeoutError(lastErr)
				if unlockErr := f.Unlock(); unlockErr != nil {
					return errors.Join(timeoutErr, fmt.Errorf("release audit lock acquired after timeout: %w", unlockErr))
				}
				return timeoutErr
			}
			return nil
		}
		delay, retryErr := auditLockRetryDelay(err, time.Until(deadline))
		if retryErr != nil {
			return retryErr
		}
		lastErr = err
		time.Sleep(delay)
	}
}

func auditLockRetryDelay(err error, remaining time.Duration) (time.Duration, error) {
	switch {
	case errors.Is(err, unix.EINTR):
		return 0, nil
	case errors.Is(err, unix.EWOULDBLOCK), errors.Is(err, unix.EAGAIN):
		return min(auditLockRetryInterval, max(0, remaining)), nil
	default:
		return 0, err
	}
}

func auditLockTimeoutError(cause error) error {
	if cause == nil {
		return fmt.Errorf("timed out acquiring audit lock after %s", auditLockTimeout)
	}
	return fmt.Errorf("timed out acquiring audit lock after %s: %w", auditLockTimeout, cause)
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
