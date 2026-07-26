package protocol

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
)

func FixtureRoot() (string, error) {
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		return "", fmt.Errorf("runtime.Caller failed")
	}
	root := filepath.Clean(filepath.Join(filepath.Dir(file), "..", "testdata", "fixtures"))
	if st, err := os.Stat(root); err != nil || !st.IsDir() {
		return "", fmt.Errorf("fixture root missing: %s", root)
	}
	return root, nil
}

func ReadFixtureBytes(rel string) ([]byte, error) {
	root, err := FixtureRoot()
	if err != nil {
		return nil, err
	}
	path := filepath.Join(root, filepath.FromSlash(rel))
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read fixture %s: %w", rel, err)
	}
	return raw, nil
}

func DecodeFixtureJSON(rel string, dest any) error {
	raw, err := ReadFixtureBytes(rel)
	if err != nil {
		return err
	}
	if err := json.Unmarshal(raw, dest); err != nil {
		return fmt.Errorf("decode fixture %s: %w", rel, err)
	}
	return nil
}
