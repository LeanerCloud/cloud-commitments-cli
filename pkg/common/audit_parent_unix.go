//go:build linux || darwin

package common

import (
	"errors"
	"fmt"
	"io/fs"
	"math"
	"os"
	"path/filepath"

	"golang.org/x/sys/unix"
)

type auditParentDescriptor interface {
	auditParentHandle
	Stat() (fs.FileInfo, error)
	Fd() uintptr
}

type auditFileIdentity struct {
	device int64
	inode  uint64
}

type auditParentOps struct {
	abs             func(string) (string, error)
	evalSymlinks    func(string) (string, error)
	openDirectory   func(string) (auditParentDescriptor, error)
	fileIdentity    func(*os.File) (auditFileIdentity, error)
	entryIdentityAt func(auditParentDescriptor, string) (auditFileIdentity, error)
}

func bindAuditLogParents(path string, auditFile *os.File) ([]auditParentDir, error) {
	return bindAuditLogParentsWithOps(path, auditFile, productionAuditParentOps())
}

func bindAuditLogParentsWithOps(path string, auditFile *os.File, ops auditParentOps) ([]auditParentDir, error) {
	absolutePath, err := ops.abs(path)
	if err != nil {
		return nil, fmt.Errorf("resolve absolute audit log path %q: %w", path, err)
	}
	resolvedPath, err := ops.evalSymlinks(absolutePath)
	if err != nil {
		return nil, fmt.Errorf("resolve audit log path %q for durability: %w", path, err)
	}
	auditIdentity, err := ops.fileIdentity(auditFile)
	if err != nil {
		return nil, fmt.Errorf("identify opened audit log %q for durability: %w", path, err)
	}

	var parents []auditParentDir
	var parentInfos []fs.FileInfo
	for _, candidate := range []string{absolutePath, resolvedPath} {
		current, parentInfo, err := bindAuditLogParent(candidate, path, auditIdentity, ops)
		if err != nil {
			return nil, closeBoundAuditParents(parents, err)
		}

		if sameAuditParent(parentInfos, parentInfo) {
			if err := current.handle.Close(); err != nil {
				return nil, closeBoundAuditParents(parents, fmt.Errorf(
					"close duplicate audit log parent directory %q: %w", current.path, err,
				))
			}
			continue
		}

		parents = append(parents, current)
		parentInfos = append(parentInfos, parentInfo)
	}
	return parents, nil
}

func bindAuditLogParent(
	candidate string,
	auditPath string,
	auditIdentity auditFileIdentity,
	ops auditParentOps,
) (auditParentDir, fs.FileInfo, error) {
	parentPath := filepath.Dir(candidate)
	parent, err := ops.openDirectory(parentPath)
	if err != nil {
		return auditParentDir{}, nil, fmt.Errorf(
			"open audit log parent directory %q for durability sync: %w", parentPath, err,
		)
	}
	current := auditParentDir{path: parentPath, handle: parent}

	parentInfo, err := parent.Stat()
	if err != nil {
		return auditParentDir{}, nil, closeBoundAuditParents([]auditParentDir{current}, fmt.Errorf(
			"stat audit log parent directory %q for durability: %w", parentPath, err,
		))
	}
	if !parentInfo.IsDir() {
		return auditParentDir{}, nil, closeBoundAuditParents([]auditParentDir{current}, fmt.Errorf(
			"audit log parent %q is not a directory", parentPath,
		))
	}

	entryIdentity, err := ops.entryIdentityAt(parent, filepath.Base(candidate))
	if err != nil {
		return auditParentDir{}, nil, closeBoundAuditParents([]auditParentDir{current}, fmt.Errorf(
			"identify audit log entry %q for durability: %w", candidate, err,
		))
	}
	if entryIdentity != auditIdentity {
		return auditParentDir{}, nil, closeBoundAuditParents([]auditParentDir{current}, fmt.Errorf(
			"audit log path %q changed while binding its parent directory; retry", auditPath,
		))
	}
	return current, parentInfo, nil
}

func sameAuditParent(existing []fs.FileInfo, candidate fs.FileInfo) bool {
	for _, info := range existing {
		if os.SameFile(info, candidate) {
			return true
		}
	}
	return false
}

func openAuditParentDirectory(path string) (auditParentDescriptor, error) {
	fd, err := unix.Open(path, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, err
	}
	file := os.NewFile(uintptr(fd), path) // #nosec G115 -- successful unix.Open fd fits uintptr
	return file, nil
}

func productionAuditParentOps() auditParentOps {
	return auditParentOps{
		abs:           filepath.Abs,
		evalSymlinks:  filepath.EvalSymlinks,
		openDirectory: openAuditParentDirectory,
		fileIdentity:  auditDescriptorIdentity,
		entryIdentityAt: func(parent auditParentDescriptor, name string) (auditFileIdentity, error) {
			var stat unix.Stat_t
			fd, err := auditFileDescriptor(parent.Fd())
			if err != nil {
				return auditFileIdentity{}, err
			}
			if err := unix.Fstatat(fd, name, &stat, 0); err != nil {
				return auditFileIdentity{}, err
			}
			return auditIdentityFromStat(&stat), nil
		},
	}
}

func auditDescriptorIdentity(file *os.File) (auditFileIdentity, error) {
	var stat unix.Stat_t
	fd, err := auditFileDescriptor(file.Fd())
	if err != nil {
		return auditFileIdentity{}, err
	}
	if err := unix.Fstat(fd, &stat); err != nil {
		return auditFileIdentity{}, err
	}
	return auditIdentityFromStat(&stat), nil
}

func auditFileDescriptor(fd uintptr) (int, error) {
	if fd > uintptr(math.MaxInt) {
		return 0, fmt.Errorf("audit file descriptor %d exceeds int range", fd)
	}
	return int(fd), nil // #nosec G115 -- fd is range-checked above
}

func auditIdentityFromStat(stat *unix.Stat_t) auditFileIdentity {
	return auditFileIdentity{
		device: int64(stat.Dev), // #nosec G115 -- same-platform equality token preserves all device ID bits
		inode:  stat.Ino,
	}
}

func closeBoundAuditParents(parents []auditParentDir, opErr error) error {
	for _, parent := range parents {
		if err := parent.handle.Close(); err != nil {
			opErr = errors.Join(opErr, fmt.Errorf("close audit log parent directory %q: %w", parent.path, err))
		}
	}
	return opErr
}
