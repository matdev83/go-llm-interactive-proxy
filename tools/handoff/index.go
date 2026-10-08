package main

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
)

type indexEntry struct {
	Artifact string      `json:"artifact"`
	Status   string      `json:"status"`
	Source   SourceState `json:"source"`
}

type executionIndex struct {
	Version int                   `json:"version"`
	Entries map[string]indexEntry `json:"entries"`
}

// The controller alone writes the execution index. Atomic replacement protects
// restart recovery, not concurrent writers; agents write their own result files.
func recordResult(name, artifact string, result Result) (returnErr error) {
	index := executionIndex{Version: 1, Entries: make(map[string]indexEntry)}
	file, err := os.Open(name)
	if err == nil {
		decodeErr := decodeStrict(file, &index)
		closeErr := file.Close()
		if err := errors.Join(decodeErr, closeErr); err != nil {
			return err
		}
		if index.Version != 1 || index.Entries == nil {
			return errors.New("invalid execution index; preserve it for diagnosis")
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	abs, err := filepath.Abs(artifact)
	if err != nil {
		return err
	}
	index.Entries[result.Task+"/"+result.Role] = indexEntry{Artifact: abs, Status: result.Status, Source: result.Source}
	if err := os.MkdirAll(filepath.Dir(name), 0o700); err != nil {
		return err
	}
	file, err = os.CreateTemp(filepath.Dir(name), ".handoff-index-*")
	if err != nil {
		return err
	}
	temp := file.Name()
	defer func() {
		if err := os.Remove(temp); err != nil && !errors.Is(err, os.ErrNotExist) {
			returnErr = errors.Join(returnErr, err)
		}
	}()
	encoder := json.NewEncoder(file)
	encoder.SetIndent("", "  ")
	writeErr := encoder.Encode(index)
	syncErr := file.Sync()
	closeErr := file.Close()
	if err := errors.Join(writeErr, syncErr, closeErr); err != nil {
		return err
	}
	return os.Rename(temp, name)
}
