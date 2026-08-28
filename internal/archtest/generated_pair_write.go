package archtest

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

type pairWriteOps struct {
	rename func(oldpath, newpath string) error
}

var defaultPairWriteOps = pairWriteOps{
	rename: os.Rename,
}

// WriteGeneratedPairAtomic atomically installs two generated files as a pair.
// If either write or install fails, both files are left in their original state.
func WriteGeneratedPairAtomic(path1 string, data1 []byte, path2 string, data2 []byte) error {
	return writeGeneratedPairAtomicInternal(path1, data1, path2, data2, defaultPairWriteOps)
}

func writeGeneratedPairAtomicInternal(path1 string, data1 []byte, path2 string, data2 []byte, ops pairWriteOps) (err error) {
	dir1 := filepath.Dir(path1)
	dir2 := filepath.Dir(path2)

	if _, err := os.Stat(dir1); err != nil {
		return fmt.Errorf("directory %s does not exist: %w", dir1, err)
	}
	if _, err := os.Stat(dir2); err != nil {
		return fmt.Errorf("directory %s does not exist: %w", dir2, err)
	}

	var toClean []string
	defer func() {
		for _, p := range toClean {
			_ = os.Remove(p)
		}
	}()

	base1 := filepath.Base(path1)
	base2 := filepath.Base(path2)

	// Create prepared temp file 1
	f1, err := os.CreateTemp(dir1, fmt.Sprintf(".%s.tmp-*", base1))
	if err != nil {
		return fmt.Errorf("failed to create temp file in %s: %w", dir1, err)
	}
	tmp1Path := f1.Name()
	toClean = append(toClean, tmp1Path)

	if _, err := f1.Write(data1); err != nil {
		_ = f1.Close()
		return fmt.Errorf("failed to write temp file %s: %w", tmp1Path, err)
	}
	if err := f1.Sync(); err != nil {
		_ = f1.Close()
		return fmt.Errorf("failed to sync temp file %s: %w", tmp1Path, err)
	}
	if err := f1.Chmod(0o644); err != nil {
		_ = f1.Close()
		return fmt.Errorf("failed to chmod temp file %s: %w", tmp1Path, err)
	}
	if err := f1.Close(); err != nil {
		return fmt.Errorf("failed to close temp file %s: %w", tmp1Path, err)
	}

	// Create prepared temp file 2
	f2, err := os.CreateTemp(dir2, fmt.Sprintf(".%s.tmp-*", base2))
	if err != nil {
		return fmt.Errorf("failed to create temp file in %s: %w", dir2, err)
	}
	tmp2Path := f2.Name()
	toClean = append(toClean, tmp2Path)

	if _, err := f2.Write(data2); err != nil {
		_ = f2.Close()
		return fmt.Errorf("failed to write temp file %s: %w", tmp2Path, err)
	}
	if err := f2.Sync(); err != nil {
		_ = f2.Close()
		return fmt.Errorf("failed to sync temp file %s: %w", tmp2Path, err)
	}
	if err := f2.Chmod(0o644); err != nil {
		_ = f2.Close()
		return fmt.Errorf("failed to chmod temp file %s: %w", tmp2Path, err)
	}
	if err := f2.Close(); err != nil {
		return fmt.Errorf("failed to close temp file %s: %w", tmp2Path, err)
	}

	// Retain original bytes and existence
	origBytes1, err1 := os.ReadFile(path1)
	origExists1 := err1 == nil
	origBytes2, err2 := os.ReadFile(path2)
	origExists2 := err2 == nil

	// Install file 1
	var bak1Path string
	if origExists1 {
		bf1, err := os.CreateTemp(dir1, fmt.Sprintf(".%s.bak-*", base1))
		if err != nil {
			return fmt.Errorf("failed to create backup temp in %s: %w", dir1, err)
		}
		bak1Path = bf1.Name()
		_ = bf1.Close()
		_ = os.Remove(bak1Path)
		toClean = append(toClean, bak1Path)

		if err := ops.rename(path1, bak1Path); err != nil {
			return fmt.Errorf("failed to backup %s: %w", path1, err)
		}
	}

	if err := ops.rename(tmp1Path, path1); err != nil {
		var rbErr error
		if origExists1 {
			rbErr = ops.rename(bak1Path, path1)
		}
		return errors.Join(fmt.Errorf("failed to install %s: %w", path1, err), rbErr)
	}

	// Install file 2
	var bak2Path string
	if origExists2 {
		bf2, err := os.CreateTemp(dir2, fmt.Sprintf(".%s.bak-*", base2))
		if err != nil {
			rbErr := restoreFile(path1, dir1, base1, origExists1, origBytes1, ops)
			return errors.Join(fmt.Errorf("failed to create backup temp in %s: %w", dir2, err), rbErr)
		}
		bak2Path = bf2.Name()
		_ = bf2.Close()
		_ = os.Remove(bak2Path)
		toClean = append(toClean, bak2Path)

		if err := ops.rename(path2, bak2Path); err != nil {
			rbErr := restoreFile(path1, dir1, base1, origExists1, origBytes1, ops)
			return errors.Join(fmt.Errorf("failed to backup %s: %w", path2, err), rbErr)
		}
	}

	if err := ops.rename(tmp2Path, path2); err != nil {
		var rb2Err error
		if origExists2 {
			rb2Err = restoreFile(path2, dir2, base2, origExists2, origBytes2, ops)
		}
		rb1Err := restoreFile(path1, dir1, base1, origExists1, origBytes1, ops)
		return errors.Join(fmt.Errorf("failed to install %s: %w", path2, err), rb2Err, rb1Err)
	}

	return nil
}

func restoreFile(destPath, dir, base string, origExists bool, origBytes []byte, ops pairWriteOps) error {
	if !origExists {
		return os.Remove(destPath)
	}
	rf, err := os.CreateTemp(dir, fmt.Sprintf(".%s.restore-*", base))
	if err != nil {
		return fmt.Errorf("rollback failed to create temp in %s: %w", dir, err)
	}
	rfPath := rf.Name()
	defer func() { _ = os.Remove(rfPath) }()

	if _, err := rf.Write(origBytes); err != nil {
		_ = rf.Close()
		return fmt.Errorf("rollback failed to write temp %s: %w", rfPath, err)
	}
	if err := rf.Sync(); err != nil {
		_ = rf.Close()
		return fmt.Errorf("rollback failed to sync temp %s: %w", rfPath, err)
	}
	if err := rf.Chmod(0o644); err != nil {
		_ = rf.Close()
		return fmt.Errorf("rollback failed to chmod temp %s: %w", rfPath, err)
	}
	if err := rf.Close(); err != nil {
		return fmt.Errorf("rollback failed to close temp %s: %w", rfPath, err)
	}

	_ = os.Remove(destPath)
	if err := os.Rename(rfPath, destPath); err != nil {
		return fmt.Errorf("rollback failed to restore %s: %w", destPath, err)
	}
	return nil
}
