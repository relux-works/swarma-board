//go:build darwin

package auth

import (
	"io"
	"os"
	"strings"

	"golang.org/x/sys/unix"
)

// Exclude atime, which our integrity reads may change. ctime detects metadata
// changes even if size and mtime are restored. The digest also checks bytes.
type staticFileStamp struct {
	dev             int32
	ino             uint64
	links, mode     uint16
	uid, gid, flags uint32
	size            int64
	mtime, ctime    unix.Timespec
}

func policyStamp(s *unix.Stat_t) staticFileStamp {
	return staticFileStamp{s.Dev, s.Ino, s.Nlink, s.Mode, s.Uid, s.Gid, s.Flags, s.Size, s.Mtim, s.Ctim}
}

// Open each path component relative to its predecessor, without following any
// symlink. O_NONBLOCK prevents a swapped FIFO from blocking before fstat can
// refuse it. Never chmod/chown the operator's file or create missing policy.
func readStaticPolicyFile(path string) ([]byte, staticFileStamp, error) {
	var zero staticFileStamp
	if !strings.HasPrefix(path, "/") {
		return nil, zero, refuse("unavailable")
	}
	dir, e := unix.Open("/", unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC, 0)
	if e != nil {
		return nil, zero, e
	}
	parts := strings.Split(strings.TrimPrefix(path, "/"), "/")
	for _, part := range parts[:len(parts)-1] {
		next, err := unix.Openat(dir, part, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
		unix.Close(dir)
		if err != nil {
			return nil, zero, err
		}
		dir = next
	}
	fd, e := unix.Openat(dir, parts[len(parts)-1], unix.O_RDONLY|unix.O_NOFOLLOW|unix.O_NONBLOCK|unix.O_CLOEXEC, 0)
	unix.Close(dir)
	if e != nil {
		return nil, zero, e
	}
	file := os.NewFile(uintptr(fd), "static policy")
	defer file.Close()
	var before, after unix.Stat_t
	if e = unix.Fstat(fd, &before); e != nil {
		return nil, zero, e
	}
	if before.Mode != unix.S_IFREG|0600 || before.Uid != uint32(os.Geteuid()) || before.Nlink != 1 || before.Size < 0 || before.Size > maxStaticPolicyBytes {
		return nil, zero, refuse("unavailable")
	}
	if e = staticPolicyNoACL(fd); e != nil {
		return nil, zero, e
	}
	data, e := io.ReadAll(io.LimitReader(file, maxStaticPolicyBytes+1))
	if e != nil {
		return nil, zero, e
	}
	if e = staticPolicyNoACL(fd); e != nil {
		return nil, zero, e
	}
	if e = unix.Fstat(fd, &after); e != nil {
		return nil, zero, e
	}
	if len(data) > maxStaticPolicyBytes || int64(len(data)) != before.Size || policyStamp(&before) != policyStamp(&after) {
		return nil, zero, refuse("unavailable")
	}
	return data, policyStamp(&after), nil
}
