package openaicodex

import (
	"encoding/json"
	"fmt"
	"hash/fnv"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/matdev83/go-llm-interactive-proxy/internal/plugins/backends/credpool"
)

const (
	selectionFirstAvailable  = "first-available"
	selectionRoundRobin      = "round-robin"
	selectionSessionAffinity = "session-affinity"
)

type affinityEntry struct {
	accountIdx int
	boundAt    time.Time
}

type managedAccount struct {
	poolID       string
	ID           string
	AccessToken  string
	RefreshToken string
	FilePath     string
	PlanType     string
}

type accountMeta struct {
	poolID       string
	ID           string
	RefreshToken string
	FilePath     string
	PlanType     string
}

type accountStore struct {
	mu            sync.Mutex
	pool          *credpool.Pool
	meta          []accountMeta
	strategy      string
	rrIndex       int
	fallback      time.Duration
	affinityTTL   time.Duration
	affinityMax   int
	affinity      map[string]affinityEntry
	affinityOrder []string
	now           func() time.Time
}

func newAccountStore(cfg Config) (*accountStore, error) {
	path := strings.TrimSpace(cfg.ManagedOAuthStoragePath)
	if path == "" {
		return nil, fmt.Errorf("%s: managed_oauth_storage_path is empty", ID)
	}
	accounts, err := loadManagedAccounts(path, cfg.ManagedOAuthAccounts)
	if err != nil {
		return nil, err
	}
	creds := make([]credpool.Credential, len(accounts))
	meta := make([]accountMeta, len(accounts))
	for i, a := range accounts {
		poolID := a.FilePath
		creds[i] = credpool.Credential{
			ID:              poolID,
			Secret:          a.AccessToken,
			RemoteAccountID: a.ID,
		}
		meta[i] = accountMeta{
			poolID:       poolID,
			ID:           a.ID,
			RefreshToken: a.RefreshToken,
			FilePath:     a.FilePath,
			PlanType:     a.PlanType,
		}
	}
	pool, err := credpool.New(creds)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", ID, err)
	}
	strategy := strings.TrimSpace(cfg.ManagedOAuthSelectionStrategy)
	if strategy == "" {
		strategy = selectionFirstAvailable
	}
	fallback := cfg.RateLimitFallback
	if fallback <= 0 {
		fallback = 60 * time.Second
	}
	return &accountStore{
		pool:          pool,
		meta:          meta,
		strategy:      strategy,
		rrIndex:       0,
		fallback:      fallback,
		affinityTTL:   cfg.ManagedOAuthSessionAffinityTTL,
		affinityMax:   cfg.ManagedOAuthSessionAffinityMaxEntries,
		affinity:      make(map[string]affinityEntry),
		affinityOrder: []string{},
		now:           time.Now,
	}, nil
}

func (s *accountStore) hasUsable() bool {
	_, err := s.pool.Acquire(s.now(), nil)
	return err == nil
}

func (s *accountStore) indexUsable(idx int, now time.Time) bool {
	if idx < 0 || idx >= len(s.meta) {
		return false
	}
	_, err := s.pool.AcquireByID(now, s.meta[idx].poolID)
	return err == nil
}

func (s *accountStore) accountAt(idx int, now time.Time) (managedAccount, error) {
	if idx < 0 || idx >= len(s.meta) {
		return managedAccount{}, fmt.Errorf("%s: invalid account index", ID)
	}
	cred, err := s.pool.AcquireByID(now, s.meta[idx].poolID)
	if err != nil {
		return managedAccount{}, err
	}
	m := s.meta[idx]
	return managedAccount{
		poolID:       m.poolID,
		ID:           m.ID,
		AccessToken:  cred.Secret,
		RefreshToken: m.RefreshToken,
		FilePath:     m.FilePath,
		PlanType:     m.PlanType,
	}, nil
}

func (s *accountStore) selectAccountForSession(session string) (managedAccount, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.now()
	session = strings.TrimSpace(session)
	s.evictExpiredAffinityLocked(now)

	if s.strategy == selectionSessionAffinity && session == "" {
		idx, err := s.pickUsableIndexLocked(now, selectionFirstAvailable)
		if err != nil {
			return managedAccount{}, err
		}
		return s.accountAt(idx, now)
	}

	if s.strategy == selectionSessionAffinity {
		if entry, ok := s.affinity[session]; ok {
			if s.affinityFresh(entry, now) && s.indexUsable(entry.accountIdx, now) {
				return s.accountAt(entry.accountIdx, now)
			}
			delete(s.affinity, session)
			s.removeAffinityOrderLocked(session)
		}
		idx, err := s.pickSessionAffinityIndexLocked(session, now)
		if err != nil {
			return managedAccount{}, err
		}
		s.bindAffinityLocked(session, idx, now)
		return s.accountAt(idx, now)
	}

	idx, err := s.pickUsableIndexLocked(now, s.strategy)
	if err != nil {
		return managedAccount{}, err
	}
	return s.accountAt(idx, now)
}

func (s *accountStore) affinityFresh(entry affinityEntry, now time.Time) bool {
	if s.affinityTTL <= 0 {
		return true
	}
	return !entry.boundAt.Add(s.affinityTTL).Before(now)
}

func (s *accountStore) evictExpiredAffinityLocked(now time.Time) {
	if s.affinityTTL <= 0 {
		return
	}
	for session, entry := range s.affinity {
		if !s.affinityFresh(entry, now) {
			delete(s.affinity, session)
			s.removeAffinityOrderLocked(session)
		}
	}
}

func (s *accountStore) bindAffinityLocked(session string, accountIdx int, now time.Time) {
	if _, ok := s.affinity[session]; !ok {
		s.enforceAffinityMaxLocked()
		s.affinityOrder = append(s.affinityOrder, session)
	}
	s.affinity[session] = affinityEntry{accountIdx: accountIdx, boundAt: now}
}

func (s *accountStore) enforceAffinityMaxLocked() {
	if s.affinityMax <= 0 {
		return
	}
	for len(s.affinity) >= s.affinityMax && len(s.affinityOrder) > 0 {
		oldest := s.affinityOrder[0]
		s.affinityOrder = s.affinityOrder[1:]
		delete(s.affinity, oldest)
	}
}

func (s *accountStore) removeAffinityOrderLocked(session string) {
	for i, key := range s.affinityOrder {
		if key == session {
			s.affinityOrder = append(s.affinityOrder[:i], s.affinityOrder[i+1:]...)
			return
		}
	}
}

func (s *accountStore) pickSessionAffinityIndexLocked(session string, now time.Time) (int, error) {
	usable, err := s.usableIndicesLocked(now)
	if err != nil {
		return 0, err
	}
	if len(usable) == 1 {
		return usable[0], nil
	}
	h := fnv.New32a()
	_, _ = h.Write([]byte(session))
	pos := int(h.Sum32() % uint32(len(usable)))
	return usable[pos], nil
}

func (s *accountStore) pickUsableIndexLocked(now time.Time, strategy string) (int, error) {
	usable, err := s.usableIndicesLocked(now)
	if err != nil {
		return 0, err
	}
	switch strategy {
	case selectionRoundRobin:
		pos := s.rrIndex % len(usable)
		s.rrIndex++
		return usable[pos], nil
	default:
		return usable[0], nil
	}
}

func (s *accountStore) usableIndicesLocked(now time.Time) ([]int, error) {
	usable := make([]int, 0, len(s.meta))
	for i := range s.meta {
		if s.indexUsable(i, now) {
			usable = append(usable, i)
		}
	}
	if len(usable) == 0 {
		return nil, fmt.Errorf("%s: no usable managed oauth accounts", ID)
	}
	return usable, nil
}

func (s *accountStore) markAuthInvalid(acct managedAccount) {
	s.pool.MarkAuthInvalid(acct.poolID)
}

func (s *accountStore) markRateLimited(acct managedAccount, until time.Time) {
	s.pool.MarkRateLimited(acct.poolID, until)
}

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

func loadManagedAccounts(dir string, filter []string) ([]managedAccount, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, fmt.Errorf("%s: read managed oauth storage: %w", ID, err)
	}
	allowAll := accountFilterAllowsAll(filter)
	allowed := accountFilterSet(filter)
	var accounts []managedAccount
	var skipped int
	for _, ent := range entries {
		if ent.IsDir() || !strings.HasSuffix(strings.ToLower(ent.Name()), ".json") {
			continue
		}
		path := filepath.Join(dir, ent.Name())
		acct, ok, err := parseManagedAccountFile(path)
		if err != nil {
			return nil, err
		}
		if !ok {
			skipped++
			continue
		}
		if !allowAll && !allowed[acct.ID] && !allowed[strings.TrimSuffix(ent.Name(), ".json")] {
			continue
		}
		accounts = append(accounts, acct)
	}
	if len(accounts) == 0 {
		if skipped > 0 {
			return nil, fmt.Errorf("%s: no usable managed oauth accounts in %q", ID, dir)
		}
		return nil, fmt.Errorf("%s: no managed oauth account files in %q", ID, dir)
	}
	slices.SortFunc(accounts, func(a, b managedAccount) int {
		return strings.Compare(a.FilePath, b.FilePath)
	})
	return accounts, nil
}

func parseManagedAccountFile(path string) (managedAccount, bool, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return managedAccount{}, false, err
	}
	var root map[string]json.RawMessage
	if err := json.Unmarshal(b, &root); err != nil {
		return managedAccount{}, false, fmt.Errorf("%s: %s: invalid JSON: %w", ID, path, err)
	}
	acct := managedAccount{
		FilePath:     path,
		ID:           firstNonEmpty(jsonRawString(root, "account_id", "accountID", "id")),
		AccessToken:  jsonRawString(root, "access_token", "accessToken"),
		RefreshToken: jsonRawString(root, "refresh_token", "refreshToken"),
	}
	if tokensRaw, ok := root["tokens"]; ok {
		var tokens map[string]json.RawMessage
		if err := json.Unmarshal(tokensRaw, &tokens); err == nil {
			acct.AccessToken = firstNonEmpty(acct.AccessToken, jsonRawString(tokens, "access_token", "accessToken"))
			acct.RefreshToken = firstNonEmpty(acct.RefreshToken, jsonRawString(tokens, "refresh_token", "refreshToken"))
			acct.ID = firstNonEmpty(acct.ID, jsonRawString(tokens, "account_id", "accountID", "id"))
		}
	}
	if acct.ID == "" || acct.AccessToken == "" {
		return managedAccount{}, false, nil
	}
	if qhRaw, ok := root["quota_headers"]; ok {
		var qh map[string]string
		if err := json.Unmarshal(qhRaw, &qh); err == nil {
			acct.PlanType = strings.TrimSpace(qh["x-codex-plan-type"])
		}
	}
	return acct, true, nil
}

func accountFilterAllowsAll(filter []string) bool {
	if len(filter) == 0 {
		return true
	}
	for _, f := range filter {
		if strings.EqualFold(strings.TrimSpace(f), "all") {
			return true
		}
	}
	return false
}

func accountFilterSet(filter []string) map[string]bool {
	out := make(map[string]bool, len(filter))
	for _, f := range filter {
		f = strings.TrimSpace(f)
		if f == "" || strings.EqualFold(f, "all") {
			continue
		}
		out[f] = true
	}
	return out
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
