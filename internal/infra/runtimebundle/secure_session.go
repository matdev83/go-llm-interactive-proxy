package runtimebundle

import (
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/b2bua"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/config"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/runtime"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/securesession/adapters/b2bualineage"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/securesession/adapters/lipapidenial"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/securesession/adapters/memory"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/securesession/adapters/sqlite"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/securesession/app"
	"github.com/matdev83/go-llm-interactive-proxy/internal/infra/metrics"
)

// secureSessionRuntime holds app-layer store/manager and recorder constructed from
// [config.SecureSessionConfig]. Not used when secure session is disabled.
type secureSessionRuntime struct {
	manager            *app.Manager
	appStore           app.Store
	recorder           app.GateRecording
	recordingMandatory bool
	closer             func() error

	requireWorkspaceID         bool
	workspaceResolveFailClosed bool
}

func buildSecureSessionRuntime(cfg *config.Config, b2b b2bua.Store, log *slog.Logger, bundle *metrics.Bundle) (*secureSessionRuntime, error) {
	if cfg == nil || !cfg.SecureSession.Enabled {
		return nil, nil
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
	key := strings.TrimSpace(ss.TokenFingerprintKey)
	if len(key) < 32 {
		return nil, fmt.Errorf("runtimebundle: secure_session enabled requires token_fingerprint_key of at least 32 characters")
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

	storeName := strings.ToLower(strings.TrimSpace(ss.Store))
	if storeName == "" {
		storeName = "memory"
	}

	switch storeName {
	case "memory":
		if log != nil {
			nd := strings.ToLower(strings.TrimSpace(ss.NonDurableWarning))
			if nd == "" {
				nd = "log"
			}
			if nd == "log" {
				log.Info("secure_session: using non-durable memory store; session evidence is lost on process restart")
			}
		}
		mem := memory.New(memory.Options{})
		rec, err := app.NewRecorder(mem)
		if err != nil {
			return nil, err
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
			return nil, err
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
		db, err := sqlite.Open(p)
		if err != nil {
			return nil, fmt.Errorf("runtimebundle: open secure session sqlite: %w", err)
		}
		rec, err := app.NewRecorder(db)
		if err != nil {
			_ = db.Close()
			return nil, err
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
			_ = db.Close()
			return nil, err
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
	default:
		return nil, fmt.Errorf("runtimebundle: secure_session.store: want memory or sqlite, got %q", ss.Store)
	}
}

func applySecureSessionToExecutor(e *runtime.Executor, ss *secureSessionRuntime) {
	if e == nil {
		return
	}
	if ss == nil {
		e.SecureSessionEnabled = false
		e.SecureSession = nil
		e.SecureSessionRecorder = nil
		e.SecureSessionRecordingMandatory = false
		e.SessionDenialMapper = nil
		e.SecureSessionRequireWorkspaceID = false
		e.SecureSessionWorkspaceResolveFailClosed = false
		return
	}
	e.SecureSessionEnabled = true
	e.SecureSession = ss.manager
	e.SecureSessionRecorder = ss.recorder
	e.SecureSessionRecordingMandatory = ss.recordingMandatory
	e.SessionDenialMapper = lipapidenial.MapToSessionDenial
	e.SecureSessionRequireWorkspaceID = ss.requireWorkspaceID
	e.SecureSessionWorkspaceResolveFailClosed = ss.workspaceResolveFailClosed
}
