//go:build darwin || linux

package sessionstore

import (
	"errors"
	"os"

	"golang.org/x/sys/unix"
)

func acquireWriterFile(path string) (*os.File, error) {
	// Do not follow a substituted lock symlink. Never unlink lock files: another
	// process might still hold the original inode, defeating writer ownership.
	fd, err := unix.Open(path, unix.O_CREAT|unix.O_RDWR|unix.O_CLOEXEC|unix.O_NOFOLLOW|unix.O_NONBLOCK, 0600)
	if err != nil {
		return nil, err
	}
	file := os.NewFile(uintptr(fd), path)
	info, err := file.Stat()
	if err == nil && !info.Mode().IsRegular() {
		err = errors.New("session lock file must be regular")
	}
	if err == nil {
		err = file.Chmod(0600)
	}
	if err == nil {
		for {
			err = unix.Flock(fd, unix.LOCK_EX|unix.LOCK_NB)
			if !errors.Is(err, unix.EINTR) {
				break
			}
		}
	}
	if err != nil {
		closeErr := file.Close()
		if errors.Is(err, unix.EWOULDBLOCK) || errors.Is(err, unix.EAGAIN) {
			err = ErrInUse
		}
		return nil, errors.Join(err, closeErr)
	}
	return file, nil
}

func releaseWriterFile(file *os.File) error {
	// Closing the descriptor also releases ownership if explicit unlock fails.
	return errors.Join(unix.Flock(int(file.Fd()), unix.LOCK_UN), file.Close())
}
