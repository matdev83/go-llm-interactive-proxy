package discovery

import (
	"io"
	"io/fs"
	"os"
)

// FS is the minimal filesystem surface discovery needs.
type FS interface {
	Lstat(name string) (fs.FileInfo, error)
	ReadDir(name string) ([]fs.DirEntry, error)
	Open(name string) (*os.File, error)
}

type osFS struct{}

func (osFS) Lstat(name string) (fs.FileInfo, error)     { return os.Lstat(name) }
func (osFS) ReadDir(name string) ([]fs.DirEntry, error) { return os.ReadDir(name) }
func (osFS) Open(name string) (*os.File, error)         { return openRegular(name) }

// SpawnProbe is an optional per-Discoverer hook. Production discovery never calls it.
type SpawnProbe interface {
	NoteLaunchAttempt()
}

// Config configures deterministic non-executing discovery.
type Config struct {
	ExplicitPaths           []string
	PackagerRoots           []string
	IncludeUpstreamDefaults bool
	Development             bool
	FS                      FS
	SpawnProbe              SpawnProbe
}

func openBounded(f *os.File, max int64) ([]byte, error) {
	limited := io.LimitReader(f, max+1)
	raw, err := io.ReadAll(limited)
	if err != nil {
		return nil, err
	}
	if int64(len(raw)) > max {
		return nil, errTooLarge
	}
	return raw, nil
}

var errTooLarge = errString("manifest too large")

type errString string

func (e errString) Error() string { return string(e) }
