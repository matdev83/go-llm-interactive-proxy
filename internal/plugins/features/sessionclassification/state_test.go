package sessionclassification_test

import (
	"reflect"
	"strings"
	"testing"

	"github.com/matdev83/go-llm-interactive-proxy/internal/plugins/features/sessionclassification"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/session"
)

func TestResolveKeyUsesProxyAuthorityOnly(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		view    session.SessionView
		want    sessionclassification.Key
		wantErr error
	}{
		{
			name: "secure session wins over A-leg and client hint",
			view: session.SessionView{
				AuthoritativeSessionID: "secure-session/opaque-id",
				ALegID:                 "a-leg-1",
				ClientSessionHint:      "client-controlled-hint",
			},
			want: sessionclassification.Key{Kind: sessionclassification.ScopeSecureSession, ID: "secure-session/opaque-id"},
		},
		{
			name: "A-leg is fallback when secure session is absent",
			view: session.SessionView{ALegID: "a-leg-2", ClientSessionHint: "hint-only"},
			want: sessionclassification.Key{Kind: sessionclassification.ScopeALeg, ID: "a-leg-2"},
		},
		{
			name:    "hint alone is not authority",
			view:    session.SessionView{ClientSessionHint: "hint-only"},
			wantErr: sessionclassification.ErrNoAuthority,
		},
		{
			name:    "empty authority is rejected",
			wantErr: sessionclassification.ErrNoAuthority,
		},
		{
			name:    "malformed secure authority does not fall back",
			view:    session.SessionView{AuthoritativeSessionID: "secure\x00id", ALegID: "a-leg-3", ClientSessionHint: "hint-only"},
			wantErr: sessionclassification.ErrInvalidKey,
		},
		{
			name:    "whitespace secure authority does not fall back",
			view:    session.SessionView{AuthoritativeSessionID: " \t", ALegID: "a-leg-4"},
			wantErr: sessionclassification.ErrInvalidKey,
		},
		{
			name:    "oversized A-leg is rejected",
			view:    session.SessionView{ALegID: strings.Repeat("a", sessionclassification.MaxAuthorityIDBytes+1)},
			wantErr: sessionclassification.ErrInvalidKey,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got, err := sessionclassification.ResolveKey(tc.view)
			if tc.wantErr != nil {
				if err != tc.wantErr {
					t.Fatalf("ResolveKey() error = %v, want %v", err, tc.wantErr)
				}
				if (tc.view.AuthoritativeSessionID != "" && strings.Contains(err.Error(), tc.view.AuthoritativeSessionID)) || (tc.view.ALegID != "" && strings.Contains(err.Error(), tc.view.ALegID)) {
					t.Fatalf("ResolveKey() error echoed authority: %q", err)
				}
				return
			}
			if err != nil {
				t.Fatalf("ResolveKey() error = %v", err)
			}
			if got != tc.want {
				t.Fatalf("ResolveKey() = %+v, want %+v", got, tc.want)
			}
		})
	}
}

func TestKeyScopeKindsKeepEqualIdentifiersIsolated(t *testing.T) {
	t.Parallel()

	secure, err := sessionclassification.ResolveKey(session.SessionView{AuthoritativeSessionID: "same-id", ALegID: "same-id"})
	if err != nil {
		t.Fatal(err)
	}
	aLeg, err := sessionclassification.ResolveKey(session.SessionView{ALegID: "same-id"})
	if err != nil {
		t.Fatal(err)
	}
	if secure.ID != aLeg.ID || secure.Kind == aLeg.Kind || secure == aLeg {
		t.Fatalf("secure and A-leg keys were not isolated: secure=%+v a-leg=%+v", secure, aLeg)
	}
}

func TestStateContractTypesContainOnlyBoundedControlData(t *testing.T) {
	t.Parallel()

	allowed := map[reflect.Type]map[string]reflect.Type{
		reflect.TypeOf(sessionclassification.Key{}): {
			"Kind": reflect.TypeOf(sessionclassification.ScopeKind("")),
			"ID":   reflect.TypeOf(""),
		},
		reflect.TypeOf(sessionclassification.Record{}): {
			"Key":                  reflect.TypeOf(sessionclassification.Key{}),
			"Classification":       reflect.TypeOf(session.Classification{}),
			"RemoteAttempts":       reflect.TypeOf(uint32(0)),
			"RemoteLeaseID":        reflect.TypeOf(""),
			"RemoteLeaseUntil":     reflect.TypeOf(sessionclassification.Record{}.RemoteLeaseUntil),
			"RemoteNextEligibleAt": reflect.TypeOf(sessionclassification.Record{}.RemoteNextEligibleAt),
			"UpdatedAt":            reflect.TypeOf(sessionclassification.Record{}.UpdatedAt),
		},
		reflect.TypeOf(sessionclassification.RemoteClaim{}): {
			"Key":          reflect.TypeOf(sessionclassification.Key{}),
			"LeaseID":      reflect.TypeOf(""),
			"Attempt":      reflect.TypeOf(uint32(0)),
			"RetryBackoff": reflect.TypeOf(sessionclassification.RemoteClaim{}.RetryBackoff),
		},
		reflect.TypeOf(sessionclassification.RemoteCompletion{}): {
			"Proposal": reflect.TypeOf(session.Classification{}),
		},
	}
	for typ, expected := range allowed {
		if typ.NumField() != len(expected) {
			t.Errorf("%s has %d fields, want exactly %d bounded fields", typ, typ.NumField(), len(expected))
		}
		for i := 0; i < typ.NumField(); i++ {
			field := typ.Field(i)
			wantType, ok := expected[field.Name]
			if !ok {
				t.Errorf("%s has unapproved field %q", typ, field.Name)
			} else if field.Type != wantType {
				t.Errorf("%s.%s has type %s, want %s", typ, field.Name, field.Type, wantType)
			}
			if field.Type.Kind() == reflect.Slice || field.Type.Kind() == reflect.Map || field.Type.Kind() == reflect.Interface || field.Type.Kind() == reflect.Pointer {
				t.Errorf("%s.%s has variable-content shape %s", typ, field.Name, field.Type)
			}
		}
	}
}
