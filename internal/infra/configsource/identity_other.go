//go:build !unix

package configsource

import (
	"fmt"
	"os"
)

func identityFromFileInfo(fi os.FileInfo) (FileIdentity, error) {
	_ = fi
	return FileIdentity{}, fmt.Errorf("configsource: %s", CategoryUnsupportedType)
}
