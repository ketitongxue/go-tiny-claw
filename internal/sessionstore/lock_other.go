//go:build !darwin && !linux

package sessionstore

import (
	"errors"
	"os"
)

func acquireWriterFile(string) (*os.File, error) {
	return nil, errors.New("session writer locks require macOS or Linux")
}

func releaseWriterFile(file *os.File) error { return file.Close() }
