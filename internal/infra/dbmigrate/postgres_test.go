package dbmigrate

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/uptrace/bun"
)

func TestParseComponents(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		raw     string
		want    []string
		wantErr string
	}{
		{
			name: "empty defaults to all",
			want: []string{ComponentUsageAuthority, ComponentConcurrency, ComponentMetering, ComponentBilling},
		},
		{
			name: "whitespace defaults to all",
			raw:  "  \t  ",
			want: []string{ComponentUsageAuthority, ComponentConcurrency, ComponentMetering, ComponentBilling},
		},
		{
			name: "selected preserves order",
			raw:  "metering,usage-authority",
			want: []string{"metering", "usage-authority"},
		},
		{
			name: "whitespace around names",
			raw:  " concurrency , metering ",
			want: []string{"concurrency", "metering"},
		},
		{
			name: "dedupe preserves first-seen order",
			raw:  "metering,usage-authority,metering,concurrency",
			want: []string{"metering", "usage-authority", "concurrency"},
		},
		{
			name:    "unknown",
			raw:     "not-a-component",
			wantErr: `unknown component "not-a-component"`,
		},
		{
			name:    "unknown among valid",
			raw:     "metering,not-a-component",
			wantErr: `unknown component "not-a-component"`,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got, err := ParseComponents(tt.raw)
			if tt.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("error=%v want containing %q", err, tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if len(got) != len(tt.want) {
				t.Fatalf("got %v want %v", got, tt.want)
			}
			for i := range tt.want {
				if got[i] != tt.want[i] {
					t.Fatalf("got %v want %v", got, tt.want)
				}
			}
		})
	}
}

func TestPostgresComponentCatalogResolves(t *testing.T) {
	t.Parallel()
	for _, component := range postgresComponentCatalog {
		t.Run(component.name, func(t *testing.T) {
			t.Parallel()
			migrate, verify, err := postgresComponent(component.name)
			if err != nil {
				t.Fatal(err)
			}
			if migrate == nil || verify == nil {
				t.Fatalf("component %q has nil lifecycle function", component.name)
			}
		})
	}
}

func TestMigrateAndVerifyComponents_OrderAndErrors(t *testing.T) {
	orig := lookupPostgresComponent
	t.Cleanup(func() { lookupPostgresComponent = orig })

	var calls []string
	migrateOK := func(name string) func(context.Context, *bun.DB) error {
		return func(context.Context, *bun.DB) error {
			calls = append(calls, name+":migrate")
			return nil
		}
	}
	verifyOK := func(name string) func(context.Context, *bun.DB) error {
		return func(context.Context, *bun.DB) error {
			calls = append(calls, name+":verify")
			return nil
		}
	}
	migrateFail := func(name string) func(context.Context, *bun.DB) error {
		return func(context.Context, *bun.DB) error {
			calls = append(calls, name+":migrate")
			return errors.New("migrate boom")
		}
	}
	verifyFail := func(name string) func(context.Context, *bun.DB) error {
		return func(context.Context, *bun.DB) error {
			calls = append(calls, name+":verify")
			return errors.New("verify boom")
		}
	}

	tests := []struct {
		name       string
		components []string
		lookup     func(string) (func(context.Context, *bun.DB) error, func(context.Context, *bun.DB) error, error)
		wantCalls  []string
		wantErr    string
	}{
		{
			name:       "migrate then verify per component",
			components: []string{"a", "b"},
			lookup: func(c string) (func(context.Context, *bun.DB) error, func(context.Context, *bun.DB) error, error) {
				return migrateOK(c), verifyOK(c), nil
			},
			wantCalls: []string{"a:migrate", "a:verify", "b:migrate", "b:verify"},
		},
		{
			name:       "migrate failure wraps and stops",
			components: []string{"a", "b"},
			lookup: func(c string) (func(context.Context, *bun.DB) error, func(context.Context, *bun.DB) error, error) {
				if c == "a" {
					return migrateFail(c), verifyOK(c), nil
				}
				return migrateOK(c), verifyOK(c), nil
			},
			wantCalls: []string{"a:migrate"},
			wantErr:   "a migration failed",
		},
		{
			name:       "verify failure wraps and stops",
			components: []string{"a", "b"},
			lookup: func(c string) (func(context.Context, *bun.DB) error, func(context.Context, *bun.DB) error, error) {
				if c == "a" {
					return migrateOK(c), verifyFail(c), nil
				}
				return migrateOK(c), verifyOK(c), nil
			},
			wantCalls: []string{"a:migrate", "a:verify"},
			wantErr:   "a verification failed",
		},
		{
			name:       "unknown component from lookup",
			components: []string{"nope"},
			lookup: func(string) (func(context.Context, *bun.DB) error, func(context.Context, *bun.DB) error, error) {
				return nil, nil, errors.New(`unknown component "nope"`)
			},
			wantErr: `unknown component "nope"`,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			calls = nil
			lookupPostgresComponent = tt.lookup
			err := migrateAndVerifyComponents(context.Background(), nil, tt.components)
			if tt.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("error=%v want containing %q", err, tt.wantErr)
				}
			} else if err != nil {
				t.Fatal(err)
			}
			if len(calls) != len(tt.wantCalls) {
				t.Fatalf("calls=%v want %v", calls, tt.wantCalls)
			}
			for i := range tt.wantCalls {
				if calls[i] != tt.wantCalls[i] {
					t.Fatalf("calls=%v want %v", calls, tt.wantCalls)
				}
			}
		})
	}
}
