package openaicodex

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

func (s *accountStore) persistQuotaHeaders(acct managedAccount, headers map[string]string) error {
	if len(headers) == 0 {
		return nil
	}
	if err := writeQuotaHeadersToFile(acct.FilePath, headers); err != nil {
		return err
	}
	planType := strings.TrimSpace(headers["x-codex-plan-type"])
	if planType == "" {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for i := range s.meta {
		if s.meta[i].FilePath == acct.FilePath {
			s.meta[i].PlanType = planType
			return nil
		}
	}
	return nil
}

func writeQuotaHeadersToFile(filePath string, headers map[string]string) error {
	b, err := os.ReadFile(filePath)
	if err != nil {
		return err
	}
	var root map[string]json.RawMessage
	if err := json.Unmarshal(b, &root); err != nil {
		return fmt.Errorf("decode account file: %w", err)
	}
	qh, err := json.Marshal(headers)
	if err != nil {
		return err
	}
	root["quota_headers"] = qh
	out, err := json.MarshalIndent(root, "", "  ")
	if err != nil {
		return err
	}
	out = append(out, '\n')
	dir := filepath.Dir(filePath)
	tmp, err := os.CreateTemp(dir, ".quota-*.tmp")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	defer func() { _ = os.Remove(tmpPath) }()
	if _, err := tmp.Write(out); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmpPath, filePath); err == nil {
		return nil
	}
	return os.WriteFile(filePath, out, 0o600)
}

func codexQuotaHeaders(h map[string][]string) map[string]string {
	out := make(map[string]string)
	for k, vals := range h {
		lk := strings.ToLower(k)
		if !strings.HasPrefix(lk, "x-codex-") || len(vals) == 0 {
			continue
		}
		if v := strings.TrimSpace(vals[0]); v != "" {
			out[lk] = v
		}
	}
	return out
}
