package diag

import (
	"cmp"
	"context"
	"fmt"
	"log/slog"
	"slices"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/config"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/extensions"
	"github.com/matdev83/go-llm-interactive-proxy/internal/featurebundle"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk"
	lipfeature "github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/feature"
)

type FeatureRegistry interface {
	BuildFeatureBundle(factoryKey string, n yaml.Node) (lipfeature.FeatureBundle, error)
}

// InventoryProjection contains one shared diagnostic projection pass.
// InventoryProjection is the complete, sanitized diagnostic read model shared
// by inventory and route inspection. Callers must not rebuild its filtered
// views independently.
type InventoryProjection struct {
	CompatibleBackends     []CompatibleBackendRow
	InstanceDiagnostics    []InstanceDiagnostic
	OpenResponsesFrontends []InstanceDiagnostic
}

// ProjectInventoryDiagnostics assembles and sanitizes the complete diagnostic
// read model used by inventory and route inspection. This is the only place
// that combines compatible-backend rows with contribution-owned projectors.
func ProjectInventoryDiagnostics(cfg *config.Config, compatible []CompatibleBackendRow, projectors []InstanceDiagnosticProjector) InventoryProjection {
	raw := CompatibleBackendInstanceDiagnostics(compatible)
	for _, project := range projectors {
		if project != nil {
			raw = append(raw, project(cfg)...)
		}
	}
	projected := ProjectSanitizedInstanceDiagnostics(raw)
	return InventoryProjection{
		CompatibleBackends:     compatible,
		InstanceDiagnostics:    projected.Instances,
		OpenResponsesFrontends: projected.OpenResponsesRows,
	}
}

type InventoryExtras struct {
	Reg                FeatureRegistry
	Registrations      []lipsdk.Registration
	CompatibleBackends CompatibleBackendProjector
	// Precomputed is supplied by a composition root when related operator views
	// already share one projection pass. A non-nil value is authoritative.
	Precomputed                  *InventoryProjection
	InstanceDiagnosticProjectors []InstanceDiagnosticProjector
	SecretGuardCatalogEntryCount int
	SecretGuardSourceCategories  []string
	SecretGuardAccessMode        string
	SecretGuardAction            string
}
type InventorySecretGuard struct {
	InstanceID        string   `json:"instance_id"`
	Action            string   `json:"action,omitempty"`
	CatalogEntryCount int      `json:"catalog_entry_count"`
	SourceCategories  []string `json:"source_categories,omitempty"`
	AccessMode        string   `json:"access_mode,omitempty"`
}

type InventoryExtensions struct {
	LegalPipeline []string                     `json:"legal_pipeline"`
	Stages        []InventoryExtensionStage    `json:"stages"`
	Features      []InventoryFeatureExtensions `json:"features"`
	// GenericPorts aggregates attempt-transform / final-stream observer occupancy
	// across features (counts/flags only; no payloads or session partitions).
	GenericPorts InventoryGenericPorts `json:"generic_ports"`
}

// InventoryGenericPorts is process-local aggregate posture for generic extension ports.
type InventoryGenericPorts struct {
	AttemptTransformOccupied       bool `json:"attempt_transform_occupied"`
	AttemptTransformHandlers       int  `json:"attempt_transform_handlers"`
	FinalStreamObservationOccupied bool `json:"final_stream_observation_occupied"`
	FinalStreamObservationHandlers int  `json:"final_stream_observation_handlers"`
}

type InventoryExtensionStage struct {
	ID             string `json:"id"`
	DefaultFailure string `json:"default_failure"`
}

type InventoryFeatureExtensions struct {
	InstanceID     string                    `json:"instance_id"`
	FactoryKind    string                    `json:"factory_kind"`
	Enabled        bool                      `json:"enabled"`
	BundleError    string                    `json:"bundle_error,omitempty"`
	StageOccupancy []InventoryStageOccupancy `json:"stage_occupancy"`
	Privileges     InventoryPrivileges       `json:"privileges"`
	SecretGuard    *InventorySecretGuard     `json:"secret_guard,omitempty"`
}

type InventoryStageOccupancy struct {
	StageID    string   `json:"stage_id"`
	HandlerIDs []string `json:"handler_ids"`
	Count      int      `json:"count"`
}

// InventoryPrivileges surfaces privileged contract boundaries (all false until bundles declare them).
type InventoryPrivileges struct {
	RawCapture        bool `json:"raw_capture"`
	AuxiliaryRequests bool `json:"auxiliary_requests"`
	AuthProvider      bool `json:"auth_provider"`
	CompletionGate    bool `json:"completion_gate"`
}

func buildInventoryExtensions(ctx context.Context, cfg *config.Config, extras *InventoryExtras) InventoryExtensions {
	pipeline := extensions.LegalPipelineStageNames()
	stages := make([]InventoryExtensionStage, 0, len(pipeline))
	for _, id := range pipeline {
		stages = append(stages, InventoryExtensionStage{
			ID:             id,
			DefaultFailure: extensions.FailurePolicyLabel(extensions.DefaultFailurePolicyForStage(id)),
		})
	}
	if cfg == nil {
		return InventoryExtensions{
			LegalPipeline: pipeline,
			Stages:        stages,
			Features:      []InventoryFeatureExtensions{},
			GenericPorts:  InventoryGenericPorts{},
		}
	}

	var reg FeatureRegistry
	var regs []lipsdk.Registration
	if extras != nil {
		reg = extras.Reg
		if len(extras.Registrations) > 0 {
			regs = extras.Registrations
		}
	}
	if len(regs) == 0 {
		regs = config.RegistrationsFromConfig(cfg)
	}

	featureRows := cfg.Plugins.Features
	feats := make([]InventoryFeatureExtensions, 0, len(featureRows))
	for _, pc := range featureRows {
		entry := InventoryFeatureExtensions{
			InstanceID:     pc.InstanceID(),
			FactoryKind:    pc.FactoryID(),
			Enabled:        pc.Enabled,
			StageOccupancy: []InventoryStageOccupancy{},
			Privileges:     InventoryPrivileges{},
		}
		if strings.EqualFold(strings.TrimSpace(pc.FactoryID()), "secrets-guard") {
			sg := &InventorySecretGuard{InstanceID: pc.InstanceID()}
			if extras != nil {
				sg.CatalogEntryCount = extras.SecretGuardCatalogEntryCount
				if extras.SecretGuardAccessMode != "" {
					sg.AccessMode = extras.SecretGuardAccessMode
				}
				if len(extras.SecretGuardSourceCategories) > 0 {
					sg.SourceCategories = append([]string(nil), extras.SecretGuardSourceCategories...)
				}
				if extras.SecretGuardAction != "" {
					sg.Action = strings.TrimSpace(extras.SecretGuardAction)
				}
			}
			entry.SecretGuard = sg
		}
		if reg != nil && pc.Enabled {
			if err := ctx.Err(); err != nil {
				entry.BundleError = err.Error()
			} else if r, ok := findFeatureRegistration(regs, pc.InstanceID()); !ok {
				msg := "diag: feature registration row missing for inventory snapshot (extras vs config mismatch)"
				entry.BundleError = msg
				slog.Default().Warn(
					"inventory extensions",
					"bundle_error", msg,
					"instance_id", entry.InstanceID,
					"factory_kind", entry.FactoryKind,
				)
			} else {
				b, err := reg.BuildFeatureBundle(r.RegistryFactoryKey(), r.Config.Node)
				if err != nil {
					entry.BundleError = err.Error()
					slog.Default().Warn(
						"inventory extensions",
						"bundle_error", err.Error(),
						"instance_id", entry.InstanceID,
						"factory_kind", entry.FactoryKind,
					)
				} else if vErr := b.Validate(); vErr != nil {
					entry.BundleError = vErr.Error()
					slog.Default().Warn(
						"inventory extensions",
						"bundle_error", vErr.Error(),
						"instance_id", entry.InstanceID,
						"factory_kind", entry.FactoryKind,
					)
				} else {
					frozen, err := featurebundle.FreezeBundle(b, entry.InstanceID)
					if err != nil {
						entry.BundleError = err.Error()
						slog.Default().Warn(
							"inventory extensions",
							"bundle_error", err.Error(),
							"instance_id", entry.InstanceID,
							"factory_kind", entry.FactoryKind,
						)
					} else {
						projections := lipfeature.ProjectDiagnostics(frozen)
						stageOcc, privs, redErr := reduceDiagnosticProjections(projections)
						if redErr != nil {
							entry.BundleError = redErr.Error()
							slog.Default().Warn(
								"inventory extensions",
								"bundle_error", redErr.Error(),
								"instance_id", entry.InstanceID,
								"factory_kind", entry.FactoryKind,
							)
						} else {
							entry.StageOccupancy = stageOcc
							entry.Privileges = privs
						}
					}
				}
			}
		}
		feats = append(feats, entry)
	}
	var ports InventoryGenericPorts
	for _, f := range feats {
		for _, occ := range f.StageOccupancy {
			switch occ.StageID {
			case extensions.StageCandidateAttemptTransform:
				ports.AttemptTransformHandlers += occ.Count
			case extensions.StageFinalStreamObservation:
				ports.FinalStreamObservationHandlers += occ.Count
			}
		}
	}
	ports.AttemptTransformOccupied = ports.AttemptTransformHandlers > 0
	ports.FinalStreamObservationOccupied = ports.FinalStreamObservationHandlers > 0
	return InventoryExtensions{LegalPipeline: pipeline, Stages: stages, Features: feats, GenericPorts: ports}
}

func findFeatureRegistration(regs []lipsdk.Registration, instanceID string) (lipsdk.Registration, bool) {
	for _, r := range regs {
		if r.Kind == lipsdk.PluginKindFeature && r.ID == instanceID {
			return r, true
		}
	}
	return lipsdk.Registration{}, false
}

func reduceDiagnosticProjections(projections []lipfeature.DiagnosticPlaneProjection) ([]InventoryStageOccupancy, InventoryPrivileges, error) {
	if len(projections) == 0 {
		return []InventoryStageOccupancy{}, InventoryPrivileges{}, nil
	}

	sorted := make([]lipfeature.DiagnosticPlaneProjection, len(projections))
	copy(sorted, projections)
	slices.SortStableFunc(sorted, func(a, b lipfeature.DiagnosticPlaneProjection) int {
		if c := cmp.Compare(a.Order, b.Order); c != 0 {
			return c
		}
		return cmp.Compare(a.PlaneID, b.PlaneID)
	})

	stageOccupancy := make([]InventoryStageOccupancy, 0, len(sorted))
	coalescedGroupIndex := make(map[string]int)
	var privs InventoryPrivileges

	for _, p := range sorted {
		for _, f := range p.Privileges.Flags {
			if err := applyPrivilegeFlag(&privs, f); err != nil {
				return nil, InventoryPrivileges{}, err
			}
		}
		for _, occ := range p.Occupants {
			for _, f := range occ.Privileges {
				if err := applyPrivilegeFlag(&privs, f); err != nil {
					return nil, InventoryPrivileges{}, err
				}
			}
		}

		if len(p.Occupants) == 0 {
			continue
		}

		labels := make([]string, len(p.Occupants))
		for i, occ := range p.Occupants {
			labels[i] = occ.Label
		}

		if p.CoalesceGroup == "" {
			stageOccupancy = append(stageOccupancy, InventoryStageOccupancy{
				StageID:    p.StageID,
				HandlerIDs: labels,
				Count:      len(labels),
			})
		} else {
			if idx, exists := coalescedGroupIndex[p.CoalesceGroup]; exists {
				stageOccupancy[idx].HandlerIDs = append(stageOccupancy[idx].HandlerIDs, labels...)
				stageOccupancy[idx].Count = len(stageOccupancy[idx].HandlerIDs)
			} else {
				newIdx := len(stageOccupancy)
				coalescedGroupIndex[p.CoalesceGroup] = newIdx
				stageOccupancy = append(stageOccupancy, InventoryStageOccupancy{
					StageID:    p.StageID,
					HandlerIDs: labels,
					Count:      len(labels),
				})
			}
		}
	}

	return stageOccupancy, privs, nil
}

func applyPrivilegeFlag(privs *InventoryPrivileges, flag string) error {
	switch flag {
	case lipfeature.PrivilegeRawCapture:
		privs.RawCapture = true
	case lipfeature.PrivilegeAuxiliaryRequests:
		privs.AuxiliaryRequests = true
	case lipfeature.PrivilegeCompletionGate:
		privs.CompletionGate = true
	case lipfeature.PrivilegeAuthProvider:
		privs.AuthProvider = true
	default:
		return fmt.Errorf("diag: unknown privilege flag %q", flag)
	}
	return nil
}

func stageOccupancyFromBundle(b lipfeature.FeatureBundle) []InventoryStageOccupancy {
	frozen, err := featurebundle.FreezeBundle(b, "feature")
	if err != nil {
		return []InventoryStageOccupancy{}
	}
	projections := lipfeature.ProjectDiagnostics(frozen)
	occ, _, _ := reduceDiagnosticProjections(projections)
	if occ == nil {
		return []InventoryStageOccupancy{}
	}
	return occ
}
