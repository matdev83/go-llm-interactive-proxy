//go:build !unix && !windows

package configsource

import (
	"fmt"
	"os"
)

func identityFromFile(f *os.File) (FileIdentity, error) {
	_ = f
	return FileIdentity{}, fmt.Errorf("configsource: %s", CategoryUnsupportedType)
}

func identityFromPath(path string) (FileIdentity, error) {
	_ = path
	return FileIdentity{}, fmt.Errorf("configsource: %s", CategoryUnsupportedType)
}
