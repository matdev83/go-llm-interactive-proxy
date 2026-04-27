package runtimebundle

import (
	"context"
	crand "crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/b2bua"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/config"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/runtime"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/securesession/adapters/b2bualineage"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/securesession/adapters/bunstore"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/securesession/adapters/lipapidenial"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/securesession/adapters/memory"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/securesession/adapters/sqlite"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/securesession/app"
	"github.com/matdev83/go-llm-interactive-proxy/internal/infra/db"
	"github.com/matdev83/go-llm-interactive-proxy/internal/infra/metrics"
)

type secureSessionRuntime struct {
	manager                    *app.Manager
	appStore                   app.Store
	recorder                   app.GateRecording
	recordingMandatory         bool
	closer                     func() error
	requireWorkspaceID         bool
	workspaceResolveFailClosed bool
}

// secureSessionBuildInput groups dependencies for [buildSecureSessionRuntime] (keeps arity small at call sites).
type secureSessionBuildInput struct {
	StartupContext context.Context
	Cfg            *config.Config
	B2B            b2bua.Store
	Log            *slog.Logger
	Bundle         *metrics.Bundle
}

func buildSecureSessionRuntime(in secureSessionBuildInput) (*secureSessionRuntime, error) {
	startupCtx, cfg, b2b := in.StartupContext, in.Cfg, in.B2B
	log, bundle := in.Log, in.Bundle
	if startupCtx == nil {
		return nil, fmt.Errorf("runtimebundle: nil startup context")
	}
	if cfg == nil {
		return nil, fmt.Errorf("runtimebundle: nil config")
	}
	if !cfg.SecureSessionEffectivelyEnabled() {
		return nil, fmt.Errorf("runtimebundle: secure_session must be enabled (reject explicit enabled: false at config validation)")
	}
	if b2b == nil {
		return nil, fmt.Errorf("runtimebundle: b2bua store is required for secure_session")
	}
	ss := &cfg.SecureSession
	wsOnErr := strings.ToLower(strings.TrimSpace(ss.WorkspaceResolveOnError))
	if wsOnErr == "" {
		wsOnErr = "fail_open"
	}
	failClosedWS := wsOnErr == "fail_closed"

	storeName := strings.ToLower(strings.TrimSpace(ss.Store))
	if storeName == "" {
		storeName = "memory"
	}
	key := strings.TrimSpace(ss.TokenFingerprintKey)
	if storeName == "memory" {
		if key == "" {
			buf := make([]byte, 32)
			if _, err := crand.Read(buf); err != nil {
				return nil, fmt.Errorf("runtimebundle: secure_session ephemeral token_fingerprint_key: %w", err)
			}
			key = base64.RawURLEncoding.EncodeToString(buf)
			if log != nil {
				log.InfoContext(startupCtx, "secure_session: memory store token_fingerprint_key omitted; using ephemeral process-local key (resume proofs reset on restart)",
					slog.String("component", "secure_session"),
					slog.String("store", "memory"),
					slog.String("notice", "ephemeral_token_fingerprint_key"),
				)
			}
		} else if len(key) < 32 {
			return nil, fmt.Errorf("runtimebundle: secure_session.token_fingerprint_key: when set, must be at least 32 characters (memory store may omit the key for a process-local ephemeral fingerprint)")
		}
	} else if len(key) < 32 {
		return nil, fmt.Errorf("runtimebundle: secure_session requires token_fingerprint_key of at least 32 characters for durable store (sqlite or postgres)")
	}
	fp := []byte(key)
	gen := app.NewRandGenerator(fp)
	lin := b2bualineage.New(b2b)
	if lin == nil {
		return nil, fmt.Errorf("runtimebundle: lineage store is required for secure_session (nil b2bua store)")
	}

	var rw time.Duration
	if s := strings.TrimSpace(ss.ResumeWindow); s != "" {
		d, err := time.ParseDuration(s)
		if err != nil {
			return nil, fmt.Errorf("runtimebundle: secure_session.resume_window: %w", err)
		}
		if d <= 0 {
			return nil, fmt.Errorf("runtimebundle: secure_session.resume_window must be positive when set")
		}
		rw = d
	}
	audit := strings.ToLower(strings.TrimSpace(ss.AuditDurability))
	if audit == "" {
		audit = "best_effort"
	}
	requireDurable := audit == "durable"

	var touchCB func(float64)
	if bundle != nil && bundle.SecureSession != nil {
		p := bundle.SecureSession
		touchCB = p.RecordActivityTouchSeconds
	}

	switch storeName {
	case "memory":
		if log != nil {
			nd := strings.ToLower(strings.TrimSpace(ss.NonDurableWarning))
			if nd == "" {
				nd = "log"
			}
			if nd == "log" {
				log.InfoContext(startupCtx, "secure_session: using non-durable memory store; session evidence is lost on process restart",
					slog.String("component", "secure_session"),
					slog.String("store", "memory"),
					slog.String("notice", "non_durable_store"),
				)
			}
		}
		mem := memory.New(memory.Options{})
		rec, err := app.NewRecorder(mem)
		if err != nil {
			return nil, fmt.Errorf("runtimebundle: secure_session: new recorder: %w", err)
		}
		mgr, err := app.NewManager(mem, gen, lin, app.ManagerConfig{
			ResumeWindow:                   rw,
			StoreDurable:                   false,
			RequireDurableStore:            requireDurable,
			FingerprintKey:                 fp,
			ObserveActivityTouch:           touchCB,
			ResumeFingerprintPrincipalOnly: ss.ResumeTokenBindPrincipalOnly,
		})
		if err != nil {
			return nil, fmt.Errorf("runtimebundle: secure_session: new manager: %w", err)
		}
		return &secureSessionRuntime{
			manager:                    mgr,
			appStore:                   mem,
			recorder:                   rec,
			recordingMandatory:         requireDurable,
			closer:                     nil,
			requireWorkspaceID:         ss.RequireWorkspaceID,
			workspaceResolveFailClosed: failClosedWS,
		}, nil
	case "sqlite":
		p := strings.TrimSpace(ss.SQLitePath)
		if p == "" {
			return nil, fmt.Errorf("runtimebundle: secure_session.sqlite_path is required for store sqlite")
		}
		sqlOpts := sqlite.Options{}
		if ttl, maxE, ok := config.EffectiveSecureSessionSQLQueryCache(*ss); ok {
			sqlOpts.SQLQueryCacheTTL = ttl
			sqlOpts.SQLQueryCacheMaxEntries = int(maxE)
		}
		db, err := sqlite.OpenContextWithOptions(startupCtx, p, sqlOpts)
		if err != nil {
			return nil, fmt.Errorf("runtimebundle: open secure session sqlite: %w", err)
		}
		rec, err := app.NewRecorder(db)
		if err != nil {
			wrapped := fmt.Errorf("runtimebundle: secure_session: new recorder: %w", err)
			if cerr := db.Close(); cerr != nil {
				return nil, errors.Join(wrapped, fmt.Errorf("runtimebundle: close sqlite after recorder error: %w", cerr))
			}
			return nil, wrapped
		}
		mgr, err := app.NewManager(db, gen, lin, app.ManagerConfig{
			ResumeWindow:                   rw,
			StoreDurable:                   true,
			RequireDurableStore:            requireDurable,
			FingerprintKey:                 fp,
			ObserveActivityTouch:           touchCB,
			ResumeFingerprintPrincipalOnly: ss.ResumeTokenBindPrincipalOnly,
		})
		if err != nil {
			wrapped := fmt.Errorf("runtimebundle: secure_session: new manager: %w", err)
			if cerr := db.Close(); cerr != nil {
				return nil, errors.Join(wrapped, fmt.Errorf("runtimebundle: close sqlite after manager error: %w", cerr))
			}
			return nil, wrapped
		}
		return &secureSessionRuntime{
			manager:                    mgr,
			appStore:                   db,
			recorder:                   rec,
			recordingMandatory:         requireDurable,
			closer:                     func() error { return db.Close() },
			requireWorkspaceID:         ss.RequireWorkspaceID,
			workspaceResolveFailClosed: failClosedWS,
		}, nil
	case "postgres":
		dsn := strings.TrimSpace(ss.PostgresDSN)
		if dsn == "" {
			return nil, fmt.Errorf("runtimebundle: secure_session.postgres_dsn is required for store postgres")
		}
		poolCfg, err := config.ParseDatabasePoolSettings(cfg.Database)
		if err != nil {
			return nil, fmt.Errorf("runtimebundle: secure_session: %w", err)
		}
		pool := db.PoolSettings{
			MaxOpenConns:    poolCfg.MaxOpenConns,
			MaxIdleConns:    poolCfg.MaxIdleConns,
			ConnMaxLifetime: poolCfg.ConnMaxLifetime,
			ConnMaxIdleTime: poolCfg.ConnMaxIdleTime,
		}
		child, cancel := context.WithTimeout(startupCtx, db.DefaultPostgresOpenMigrateTimeout)
		defer cancel()
		bunDB, err := db.OpenPostgresBun(child, dsn, pool)
		if err != nil {
			return nil, fmt.Errorf("runtimebundle: secure_session: open postgres store: %w", err)
		}
		bunOpts := bunstore.Options{}
		if ttl, maxE, ok := config.EffectiveSecureSessionSQLQueryCache(*ss); ok {
			bunOpts.SQLQueryCacheTTL = ttl
			bunOpts.SQLQueryCacheMaxEntries = int(maxE)
		}
		st, err := bunstore.NewContextWithOptions(child, bunDB, bunOpts)
		if err != nil {
			schemaErr := fmt.Errorf("runtimebundle: secure_session: prepare postgres schema: %w", err)
			if cerr := bunDB.Close(); cerr != nil {
				return nil, errors.Join(schemaErr, fmt.Errorf("runtimebundle: close postgres bun db after schema error: %w", cerr))
			}
			return nil, schemaErr
		}
		rec, err := app.NewRecorder(st)
		if err != nil {
			wrapped := fmt.Errorf("runtimebundle: secure_session: new recorder: %w", err)
			if cerr := st.Close(); cerr != nil {
				return nil, errors.Join(wrapped, fmt.Errorf("runtimebundle: close postgres store after recorder error: %w", cerr))
			}
			return nil, wrapped
		}
		mgr, err := app.NewManager(st, gen, lin, app.ManagerConfig{
			ResumeWindow:                   rw,
			StoreDurable:                   true,
			RequireDurableStore:            requireDurable,
			FingerprintKey:                 fp,
			ObserveActivityTouch:           touchCB,
			ResumeFingerprintPrincipalOnly: ss.ResumeTokenBindPrincipalOnly,
		})
		if err != nil {
			wrapped := fmt.Errorf("runtimebundle: secure_session: new manager: %w", err)
			if cerr := st.Close(); cerr != nil {
				return nil, errors.Join(wrapped, fmt.Errorf("runtimebundle: close postgres store after manager error: %w", cerr))
			}
			return nil, wrapped
		}
		return &secureSessionRuntime{
			manager:                    mgr,
			appStore:                   st,
			recorder:                   rec,
			recordingMandatory:         requireDurable,
			closer:                     func() error { return st.Close() },
			requireWorkspaceID:         ss.RequireWorkspaceID,
			workspaceResolveFailClosed: failClosedWS,
		}, nil
	default:
		return nil, fmt.Errorf("runtimebundle: secure_session.store: want memory, sqlite, or postgres, got %q", ss.Store)
	}
}

func applySecureSessionToExecutor(e *runtime.Executor, ss *secureSessionRuntime) {
	if e == nil || ss == nil {
		return
	}
	e.SecureSession = ss.manager
	e.SecureSessionRecorder = ss.recorder
	e.SecureSessionRecordingMandatory = ss.recordingMandatory
	e.SessionDenialMapper = lipapidenial.MapToSessionDenial
	e.SecureSessionRequireWorkspaceID = ss.requireWorkspaceID
	e.SecureSessionWorkspaceResolveFailClosed = ss.workspaceResolveFailClosed
}
