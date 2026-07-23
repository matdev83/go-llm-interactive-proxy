package main

import (
	"context"
	"fmt"
	"os"
	"strings"
	"unicode/utf8"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/accessmode"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/config"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/configreload"
	"github.com/matdev83/go-llm-interactive-proxy/internal/infra/runtimebundle"
	mgmtreload "github.com/matdev83/go-llm-interactive-proxy/internal/stdhttp/admin/configreload"
)

// reloadManagementTokenEnv is the dedicated startup-fixed bearer source for the
// management reload API (req 12.5). It is never taken from data-plane cookies,
// local API keys, or request bodies.
const reloadManagementTokenEnv = "LIP_RELOAD_MANAGEMENT_TOKEN"

// reloadManagementAddressEnv is the explicit startup-fixed management bind.
// Empty disables the optional listener, preserving existing startup behavior and
// allowing multiple proxy instances on one host without a fixed-port collision.
const reloadManagementAddressEnv = "LIP_RELOAD_MANAGEMENT_ADDRESS"

// managementListenAddress is a test-only bind override. Production obtains the
// startup-fixed value from reloadManagementAddressEnv.
var managementListenAddress string

// resolveManagementOptions builds startup-fixed management options. An empty
// address disables management. Single-user loopback may use local trust;
// multi-user or non-loopback requires a dedicated strong bearer. An explicit
// non-loopback address is the required bind opt-in.
func resolveManagementOptions(cfg *config.Config) (mgmtreload.Options, bool, error) {
	if cfg == nil {
		return mgmtreload.Options{}, false, fmt.Errorf("lipstd: nil config")
	}
	mode, err := cfg.EffectiveAccessMode()
	if err != nil {
		return mgmtreload.Options{}, false, fmt.Errorf("lipstd: access mode: %w", err)
	}
	address := strings.TrimSpace(managementListenAddress)
	if address == "" {
		address = strings.TrimSpace(os.Getenv(reloadManagementAddressEnv))
	}
	if address == "" {
		return mgmtreload.Options{}, false, nil
	}

	loopback := config.IsExplicitLoopbackListenAddress(address)
	token := strings.TrimSpace(os.Getenv(reloadManagementTokenEnv))
	needsBearer := mode == accessmode.ModeMultiUser || !loopback
	if needsBearer && token == "" {
		return mgmtreload.Options{}, false, nil
	}
	if token != "" && utf8.RuneCountInString(token) < mgmtreload.MinBearerSecretRunes {
		return mgmtreload.Options{}, false, fmt.Errorf(
			"lipstd: %s bearer token must be at least %d Unicode code points",
			reloadManagementTokenEnv, mgmtreload.MinBearerSecretRunes,
		)
	}

	opts := mgmtreload.Options{
		Address:          address,
		AccessMode:       mode,
		AllowNonLoopback: !loopback,
	}
	switch mode {
	case accessmode.ModeMultiUser:
		opts.AuthMode = mgmtreload.AuthModeBearer
		opts.BearerToken = token
	case accessmode.ModeSingleUser:
		if loopback && token == "" {
			opts.AuthMode = mgmtreload.AuthModeLocalTrust
		} else {
			opts.AuthMode = mgmtreload.AuthModeBearer
			opts.BearerToken = token
		}
	default:
		return mgmtreload.Options{}, false, fmt.Errorf("lipstd: unknown access mode %q", mode)
	}
	if err := opts.Validate(); err != nil {
		return mgmtreload.Options{}, false, err
	}
	return opts, true, nil
}

// startManagementServer starts the process-owned management adapter against the
// shared reload coordinator using startup-fixed options (task 5.6 / req 12.x).
// It returns (nil, nil) when management is not explicitly enabled, or when a
// required dedicated bearer is absent, so ordinary data-plane serve continues.
func startManagementServer(ctx context.Context, res runtimebundle.BootstrapResult, coord interface {
	Reload(context.Context, configreload.ReloadTrigger) configreload.ReloadResult
	Status() configreload.ReloadStatus
	FixedSourcePath() string
},
) (*mgmtreload.Server, error) {
	if coord == nil {
		return nil, fmt.Errorf("lipstd: nil reload coordinator")
	}
	opts, enable, err := resolveManagementOptions(res.Config)
	if err != nil {
		return nil, err
	}
	if !enable {
		if res.Logger != nil {
			res.Logger.WarnContext(ctx,
				"management reload API disabled: set "+reloadManagementAddressEnv+
					" and, for multi_user/non-loopback, "+reloadManagementTokenEnv,
			)
		}
		return nil, nil
	}
	srv, err := mgmtreload.New(opts, coord)
	if err != nil {
		return nil, err
	}
	if err := srv.Start(ctx); err != nil {
		return nil, err
	}
	if res.Logger != nil {
		res.Logger.InfoContext(ctx, "management listening",
			"addr", srv.Addr(),
			"auth_mode", string(opts.AuthMode),
		)
	}
	return srv, nil
}
