package comparison

import (
	"bytes"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
)

const (
	// MaxInputBytes bounds comparison input documents.
	MaxInputBytes = 64 << 10
	// MaxReportBytes bounds marshaled report JSON.
	MaxReportBytes = 256 << 10
)

var forbiddenKeyExact = map[string]struct{}{
	"prompt":         {},
	"prompts":        {},
	"api_key":        {},
	"apikey":         {},
	"cursor_api_key": {},
	"agent_id":       {},
	"run_id":         {},
	"workspace":      {},
	"workspace_path": {},
	"project_dir":    {},
	"path":           {},
	"cwd":            {},
	"tool_arguments": {},
	"tool_result":    {},
	"tool_content":   {},
	"reasoning_text": {},
	"raw_payload":    {},
	"secret":         {},
	"password":       {},
	"access_token":   {},
}

var forbiddenKeySubstr = []string{
	"prompt",
	"api_key",
	"apikey",
	"agent_id",
	"run_id",
	"tool_arg",
	"tool_result",
	"workspace",
	"secret",
	"password",
	"token",
}

var (
	reFileURI    = regexp.MustCompile(`(?i)\bfile:/+`)
	reUNC        = regexp.MustCompile(`\\\\[^\s"'` + "`" + `]+`)
	reWinDrive   = regexp.MustCompile(`(?i)(?:^|[\s"'` + "`" + `])[A-Z]:(?:\\|/)[^\s"'` + "`" + `]*`)
	reUnixAbs    = regexp.MustCompile(`(?:^|[\s"'` + "`" + `])(/[^\s"'` + "`" + `]+)`)
	reAPIKeyish  = regexp.MustCompile(`(?i)\b(sk-|crsr_|cursor_api_key=)[A-Za-z0-9_\-]{8,}`)
	reAgentRunID = regexp.MustCompile(`(?i)\b(agent|run)[-_][A-Za-z0-9]{8,}\b`)
)

// ScanForbiddenRawJSON rejects input documents that contain disallowed keys or secret-like values.
func ScanForbiddenRawJSON(raw []byte) error {
	if len(raw) > MaxInputBytes {
		return fmt.Errorf("comparison: input size exceeds %d bytes", MaxInputBytes)
	}
	return scanForbiddenContent(raw)
}

func scanForbiddenContent(raw []byte) error {
	var root any
	if err := json.Unmarshal(raw, &root); err != nil {
		return fmt.Errorf("comparison: redact scan: %w", err)
	}
	if err := walkAny("", root); err != nil {
		return err
	}
	text := string(raw)
	if reAPIKeyish.MatchString(text) {
		return fmt.Errorf("comparison: forbidden secret-like token in input")
	}
	if looksLikeForbiddenPath(text) {
		return fmt.Errorf("comparison: forbidden absolute path in input")
	}
	if reAgentRunID.MatchString(text) {
		return fmt.Errorf("comparison: forbidden SDK agent/run id pattern in input")
	}
	return nil
}

func walkAny(path string, v any) error {
	switch t := v.(type) {
	case map[string]any:
		for k, child := range t {
			lk := strings.ToLower(strings.TrimSpace(k))
			if forbiddenKey(lk) {
				return fmt.Errorf("comparison: forbidden field %q", joinPath(path, k))
			}
			if err := walkAny(joinPath(path, k), child); err != nil {
				return err
			}
		}
	case []any:
		for i, child := range t {
			if err := walkAny(fmt.Sprintf("%s[%d]", path, i), child); err != nil {
				return err
			}
		}
	case string:
		return scanStringValue(path, t)
	}
	return nil
}

func scanStringValue(path, s string) error {
	if reAPIKeyish.MatchString(s) || looksLikeForbiddenPath(s) || reAgentRunID.MatchString(s) {
		return fmt.Errorf("comparison: forbidden value at %s", path)
	}
	low := strings.ToLower(s)
	for _, bad := range []string{"tool_arguments", "tool_result", "prompt="} {
		if strings.Contains(low, bad) {
			return fmt.Errorf("comparison: forbidden content marker at %s", path)
		}
	}
	return nil
}

func looksLikeForbiddenPath(s string) bool {
	if reFileURI.MatchString(s) {
		return true
	}
	if reUNC.MatchString(s) {
		return true
	}
	if reWinDrive.MatchString(s) {
		return true
	}
	return reUnixAbs.MatchString(s)
}

func forbiddenKey(lk string) bool {
	if _, ok := forbiddenKeyExact[lk]; ok {
		return true
	}
	for _, sub := range forbiddenKeySubstr {
		if strings.Contains(lk, sub) {
			return true
		}
	}
	return false
}

func joinPath(base, key string) string {
	if base == "" {
		return key
	}
	return base + "." + key
}

// LoadInputBytes scans then decodes a comparison input document.
func LoadInputBytes(raw []byte) (InputDocument, error) {
	if len(raw) > MaxInputBytes {
		return InputDocument{}, fmt.Errorf("comparison: input size exceeds %d bytes", MaxInputBytes)
	}
	if err := ScanForbiddenRawJSON(raw); err != nil {
		return InputDocument{}, err
	}
	doc, err := DecodeInputJSON(bytes.NewReader(raw))
	if err != nil {
		return InputDocument{}, err
	}
	if err := ValidateInput(doc); err != nil {
		return InputDocument{}, err
	}
	return doc, nil
}
