//go:build linux

package auth

import (
	"io"
	"os"
	"strings"
	"syscall"
)

// Exclude atime, which our integrity reads may change. ctime detects metadata
// changes even if size and mtime are restored. The digest also checks bytes.
type staticFileStamp struct {
	dev, ino, links uint64
	uid, gid, mode  uint32
	size            int64
	mtime, ctime    syscall.Timespec
}

func policyStamp(s *syscall.Stat_t) staticFileStamp {
	return staticFileStamp{s.Dev, s.Ino, s.Nlink, s.Uid, s.Gid, s.Mode, s.Size, s.Mtim, s.Ctim}
}

// Open each path component relative to its predecessor, without following any
// symlink. O_NONBLOCK prevents a swapped FIFO from blocking before fstat can
// refuse it. Never chmod/chown the operator's file or create missing policy.
func readStaticPolicyFile(path string) ([]byte, staticFileStamp, error) {
	var zero staticFileStamp
	if !strings.HasPrefix(path, "/") {
		return nil, zero, refuse("unavailable")
	}
	dir, e := syscall.Open("/", syscall.O_RDONLY|syscall.O_DIRECTORY|syscall.O_CLOEXEC, 0)
	if e != nil {
		return nil, zero, e
	}
	parts := strings.Split(strings.TrimPrefix(path, "/"), "/")
	for _, part := range parts[:len(parts)-1] {
		next, err := syscall.Openat(dir, part, syscall.O_RDONLY|syscall.O_DIRECTORY|syscall.O_NOFOLLOW|syscall.O_CLOEXEC, 0)
		syscall.Close(dir)
		if err != nil {
			return nil, zero, err
		}
		dir = next
	}
	fd, e := syscall.Openat(dir, parts[len(parts)-1], syscall.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK|syscall.O_CLOEXEC, 0)
	syscall.Close(dir)
	if e != nil {
		return nil, zero, e
	}
	file := os.NewFile(uintptr(fd), "static policy")
	defer file.Close()
	var before, after syscall.Stat_t
	if e = syscall.Fstat(fd, &before); e != nil {
		return nil, zero, e
	}
	if before.Mode != syscall.S_IFREG|0600 || before.Uid != uint32(os.Geteuid()) || before.Nlink != 1 || before.Size > maxStaticPolicyBytes {
		return nil, zero, refuse("unavailable")
	}
	data, e := io.ReadAll(io.LimitReader(file, maxStaticPolicyBytes+1))
	if e != nil {
		return nil, zero, e
	}
	if e = syscall.Fstat(fd, &after); e != nil {
		return nil, zero, e
	}
	if len(data) > maxStaticPolicyBytes || int64(len(data)) != before.Size || policyStamp(&before) != policyStamp(&after) {
		return nil, zero, refuse("unavailable")
	}
	return data, policyStamp(&after), nil
}
