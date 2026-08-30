//go:build linux || darwin

package tools

import (
	"errors"
	"fmt"
	"io/fs"
	"math"
	"os"
	"path/filepath"
	"strings"

	"golang.org/x/sys/unix"
)

type auditDirectoryHandle interface {
	Fd() uintptr
	Sync() error
	Close() error
}

type auditDirectoryOps struct {
	openRoot func() (auditDirectoryHandle, error)
	mkdirAt  func(auditDirectoryHandle, string, uint32) error
	openAt   func(auditDirectoryHandle, string) (auditDirectoryHandle, error)
}

func ensureAuditLogDirectory(path string) error {
	return ensureAuditLogDirectoryWithOps(path, productionAuditDirectoryOps())
}

func ensureAuditLogDirectoryWithOps(path string, ops auditDirectoryOps) error {
	parentPath, err := filepath.Abs(filepath.Dir(path))
	if err != nil {
		return fmt.Errorf("resolve audit log directory for %s: %w", path, err)
	}
	relativePath, err := filepath.Rel(string(filepath.Separator), parentPath)
	if err != nil {
		return fmt.Errorf("resolve audit log directory components for %s: %w", path, err)
	}

	current, err := ops.openRoot()
	if err != nil {
		return fmt.Errorf("open audit log directory / for durability sync: %w", err)
	}
	currentPath := string(filepath.Separator)

	components := strings.Split(relativePath, string(filepath.Separator))
	if relativePath == "." {
		components = nil
	}
	for _, component := range components {
		current, currentPath, err = walkAuditDirectoryComponent(current, currentPath, component, ops)
		if err != nil {
			return err
		}
	}

	return closeAuditDirectory(currentPath, current, nil)
}

func walkAuditDirectoryComponent(
	parent auditDirectoryHandle,
	parentPath string,
	component string,
	ops auditDirectoryOps,
) (auditDirectoryHandle, string, error) {
	childPath := filepath.Join(parentPath, component)
	if err := createAuditDirectoryComponent(parent, component, ops); err != nil {
		return nil, "", closeAuditDirectory(
			parentPath,
			parent,
			fmt.Errorf("create audit log directory %s: %w", childPath, err),
		)
	}

	child, err := ops.openAt(parent, component)
	if err != nil {
		return nil, "", closeAuditDirectory(
			parentPath,
			parent,
			fmt.Errorf("open audit log directory %s for durability sync: %w", childPath, err),
		)
	}

	if err := syncAndCloseAuditDirectory(parentPath, parent); err != nil {
		return nil, "", closeAuditDirectory(
			childPath,
			child,
			err,
		)
	}
	return child, childPath, nil
}

func createAuditDirectoryComponent(parent auditDirectoryHandle, component string, ops auditDirectoryOps) error {
	err := ops.mkdirAt(parent, component, 0o700)
	if errors.Is(err, fs.ErrExist) {
		return nil
	}
	return err
}

func syncAndCloseAuditDirectory(path string, directory auditDirectoryHandle) error {
	var opErr error
	if err := directory.Sync(); err != nil {
		opErr = fmt.Errorf("sync audit log directory %s for durability: %w", path, err)
	}
	if err := directory.Close(); err != nil {
		opErr = errors.Join(opErr, fmt.Errorf("close audit log directory %s: %w", path, err))
	}
	return opErr
}

func closeAuditDirectory(path string, handle auditDirectoryHandle, opErr error) error {
	if err := handle.Close(); err != nil {
		opErr = errors.Join(opErr, fmt.Errorf("close audit log directory %s: %w", path, err))
	}
	return opErr
}

func auditDirectoryFD(handle auditDirectoryHandle) (int, error) {
	fd := handle.Fd()
	if fd > uintptr(math.MaxInt) {
		return 0, fmt.Errorf("audit directory file descriptor %d exceeds int range", fd)
	}
	return int(fd), nil // #nosec G115 -- fd is range-checked above
}

func productionAuditDirectoryOps() auditDirectoryOps {
	return auditDirectoryOps{
		openRoot: func() (auditDirectoryHandle, error) {
			return os.Open(string(filepath.Separator))
		},
		mkdirAt: func(parent auditDirectoryHandle, name string, mode uint32) error {
			fd, err := auditDirectoryFD(parent)
			if err != nil {
				return err
			}
			return unix.Mkdirat(fd, name, mode)
		},
		openAt: func(parent auditDirectoryHandle, name string) (auditDirectoryHandle, error) {
			parentFD, err := auditDirectoryFD(parent)
			if err != nil {
				return nil, err
			}
			fd, err := unix.Openat(
				parentFD,
				name,
				unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC,
				0,
			)
			if err != nil {
				return nil, err
			}
			file := os.NewFile(uintptr(fd), name) // #nosec G115 -- successful unix.Openat fd fits uintptr
			return file, nil
		},
	}
}
