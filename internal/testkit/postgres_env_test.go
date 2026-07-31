package testkit

import (
	"strings"
	"testing"
)

func TestPostgresDSNEnvContract(t *testing.T) {
	cases := []struct {
		name              string
		admin             string
		runtime           string
		legacy            string
		requirePooler     string
		runtimeIsPooler   string
		wantAdminOK       bool
		wantAdmin         string
		wantRuntimeOK     bool
		wantRuntime       string
		wantLookupErr     bool
		wantFailClosed    bool
		errMustContain    string
		errMustNotContain string
	}{
		{
			name:          "both_missing",
			wantLookupErr: true, errMustContain: LIPTestPostgresAdminDSN, errMustNotContain: "postgres://",
		},
		{
			name: "missing_admin", runtime: "postgres://runtime/x", runtimeIsPooler: "1",
			wantAdminOK: true, wantAdmin: "postgres://runtime/x",
			wantRuntimeOK: true, wantRuntime: "postgres://runtime/x",
		},
		{
			name: "missing_runtime", admin: "postgres://admin/x",
			wantAdminOK: true, wantAdmin: "postgres://admin/x",
			wantLookupErr: true, errMustContain: LIPTestPostgresDSN, errMustNotContain: "postgres://",
		},
		{
			name: "both_present_with_attestation", admin: " postgres://admin/x ", runtime: " postgres://runtime/x ", runtimeIsPooler: "1",
			wantAdminOK: true, wantAdmin: "postgres://admin/x",
			wantRuntimeOK: true, wantRuntime: "postgres://runtime/x",
		},
		{
			// Ambient direct DSNs (make qa / local developer env) must not enter
			// pooled-only helpers without explicit topology attestation.
			name:  "ambient_dsns_without_attestation_skips_not_fail_closed",
			admin: "postgres://admin/x", runtime: "postgres://runtime/x",
			wantAdminOK: true, wantAdmin: "postgres://admin/x",
			wantRuntimeOK: true, wantRuntime: "postgres://runtime/x",
			wantLookupErr: true, wantFailClosed: false, errMustContain: LIPTestPostgresRuntimeIsPooler,
		},
		{
			name: "runtime_prefers_new_over_legacy", runtime: "postgres://a/a", legacy: "postgres://b/b", runtimeIsPooler: "1",
			wantAdminOK: true, wantAdmin: "postgres://a/a",
			wantRuntimeOK: true, wantRuntime: "postgres://a/a",
		},
		{
			name: "legacy_runtime_alias", legacy: " postgres://legacy/x ", runtimeIsPooler: "1",
			wantAdminOK: true, wantAdmin: "postgres://legacy/x",
			wantRuntimeOK: true, wantRuntime: "postgres://legacy/x",
		},
		{
			name: "admin_falls_back_to_preferred_runtime", runtime: "postgres://runtime/x", legacy: "postgres://legacy/x", runtimeIsPooler: "1",
			wantAdminOK: true, wantAdmin: "postgres://runtime/x",
			wantRuntimeOK: true, wantRuntime: "postgres://runtime/x",
		},
		{
			name: "require_pooler_without_attestation", admin: "", runtime: "postgres://runtime/x", requirePooler: "1",
			wantAdminOK: true, wantAdmin: "postgres://runtime/x",
			wantRuntimeOK: true, wantRuntime: "postgres://runtime/x",
			wantLookupErr: true, wantFailClosed: true, errMustContain: LIPTestPostgresRuntimeIsPooler,
		},
		{
			name: "require_pooler_with_attestation", admin: "", runtime: "postgres://runtime/x", requirePooler: "1", runtimeIsPooler: "true",
			wantAdminOK: true, wantAdmin: "postgres://runtime/x",
			wantRuntimeOK: true, wantRuntime: "postgres://runtime/x",
			wantFailClosed: true,
		},
		{
			name: "optional_gate_not_fail_closed", requirePooler: "",
			wantLookupErr: true, wantFailClosed: false,
			errMustContain: LIPTestPostgresAdminDSN,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv(LIPTestPostgresAdminDSN, tc.admin)
			t.Setenv(LIPTestPostgresDSN, tc.runtime)
			t.Setenv(LIPManagedPostgresDSN, tc.legacy)
			t.Setenv(LIPRequirePostgresPooler, tc.requirePooler)
			t.Setenv(LIPTestPostgresRuntimeIsPooler, tc.runtimeIsPooler)

			admin, adminOK := PostgresAdminDSN()
			if adminOK != tc.wantAdminOK || admin != tc.wantAdmin {
				t.Fatalf("PostgresAdminDSN: ok=%v dsn=%q want ok=%v dsn=%q", adminOK, admin, tc.wantAdminOK, tc.wantAdmin)
			}
			runtime, runtimeOK := PostgresRuntimeDSN()
			if runtimeOK != tc.wantRuntimeOK || runtime != tc.wantRuntime {
				t.Fatalf("PostgresRuntimeDSN: ok=%v dsn=%q want ok=%v dsn=%q", runtimeOK, runtime, tc.wantRuntimeOK, tc.wantRuntime)
			}
			gate := EvaluateDualPlanePostgresGate()
			if tc.wantLookupErr {
				if gate.Err == nil {
					t.Fatal("expected lookup error")
				}
				if tc.errMustContain != "" && !strings.Contains(gate.Err.Error(), tc.errMustContain) {
					t.Fatalf("error %v must contain %q", gate.Err, tc.errMustContain)
				}
				if tc.errMustNotContain != "" && strings.Contains(gate.Err.Error(), tc.errMustNotContain) {
					t.Fatalf("error must not contain %q: %v", tc.errMustNotContain, gate.Err)
				}
			} else if gate.Err != nil {
				t.Fatalf("unexpected lookup error: %v", gate.Err)
			}
			if gate.FailClosed != tc.wantFailClosed {
				t.Fatalf("FailClosed=%v want %v", gate.FailClosed, tc.wantFailClosed)
			}
		})
	}
}

func TestPostgresRequired_truthy(t *testing.T) {
	t.Setenv(LIPRequirePostgres, "")
	if PostgresRequired() {
		t.Fatal("empty should not require")
	}
	t.Setenv(LIPRequirePostgres, "1")
	if !PostgresRequired() {
		t.Fatal("1 should require")
	}
}

func TestPostgresPoolerRequired_truthy(t *testing.T) {
	t.Setenv(LIPRequirePostgresPooler, "")
	if PostgresPoolerRequired() {
		t.Fatal("empty should not require")
	}
	t.Setenv(LIPRequirePostgresPooler, "true")
	if !PostgresPoolerRequired() {
		t.Fatal("true should require")
	}
}

func TestLookupDualPlanePostgresDSNs_bothPresent(t *testing.T) {
	t.Setenv(LIPTestPostgresAdminDSN, "postgres://admin/x")
	t.Setenv(LIPTestPostgresDSN, "postgres://runtime/x")
	t.Setenv(LIPManagedPostgresDSN, "")
	admin, runtime, err := LookupDualPlanePostgresDSNs()
	if err != nil {
		t.Fatalf("LookupDualPlanePostgresDSNs: %v", err)
	}
	if admin != "postgres://admin/x" || runtime != "postgres://runtime/x" {
		t.Fatalf("admin=%q runtime=%q", admin, runtime)
	}
}

func TestSkipUnlessPostgresContract(t *testing.T) {
	t.Run("ambient_dsn_without_require_skips", func(t *testing.T) {
		t.Setenv(LIPTestPostgresDSN, "postgres://unreachable:5432/db")
		t.Setenv(LIPRequirePostgres, "")
		skipped := false
		// SkipUnlessPostgres will call t.Skipf when LIP_REQUIRE_POSTGRES is empty
		t.Run("sub", func(st *testing.T) {
			SkipUnlessPostgres(st)
			st.Error("should have skipped")
		})
		_ = skipped
	})

	t.Run("require_1_with_dsn_returns_dsn", func(t *testing.T) {
		expectedDSN := "postgres://user:pass@localhost:5432/db"
		t.Setenv(LIPTestPostgresDSN, expectedDSN)
		t.Setenv(LIPRequirePostgres, "1")
		dsn := SkipUnlessPostgres(t)
		if dsn != expectedDSN {
			t.Fatalf("expected DSN %q, got %q", expectedDSN, dsn)
		}
	})
}
