package economics

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"testing"
)

const (
	v1TariffHashPreimage = `{"ref":{"id":"advisory-tariff","version":"v1","rater_id":"advisory-rater"},"currency":"USD","rules":[],"support_advisory_version":"component-support-advisory-v1"}`
	v1TariffContentHash  = "c002e8deb44d3eb9fcf9b2406736fc71fc575c3b6a06c0f97edd49c6ae72882e"
)

func TestSupportAdvisoryVersionViewToTariffRoundTrip(t *testing.T) {
	view := RatingCatalogView{Currency: "USD", SupportAdvisoryVersion: SupportAdvisoryVersionV1}
	encoded, err := json.Marshal(view)
	if err != nil {
		t.Fatalf("marshal rating catalog view: %v", err)
	}
	var decoded RatingCatalogView
	if err := json.Unmarshal(encoded, &decoded); err != nil {
		t.Fatalf("unmarshal rating catalog view: %v", err)
	}
	if decoded.SupportAdvisoryVersion != SupportAdvisoryVersionV1 {
		t.Fatalf("view JSON round-trip version = %q, want %q", decoded.SupportAdvisoryVersion, SupportAdvisoryVersionV1)
	}

	cloned := decoded.Clone()
	if cloned.SupportAdvisoryVersion != SupportAdvisoryVersionV1 {
		t.Fatalf("view clone version = %q, want %q", cloned.SupportAdvisoryVersion, SupportAdvisoryVersionV1)
	}
	ref := supportAdvisoryTestRef("v1")
	tariff, err := cloned.Tariff(ref)
	if err != nil {
		t.Fatalf("materialize tariff: %v", err)
	}
	if tariff.SupportAdvisoryVersion != SupportAdvisoryVersionV1 {
		t.Fatalf("view-to-tariff version = %q, want %q", tariff.SupportAdvisoryVersion, SupportAdvisoryVersionV1)
	}
	if got := tariff.Clone().SupportAdvisoryVersion; got != SupportAdvisoryVersionV1 {
		t.Fatalf("tariff clone version = %q, want %q", got, SupportAdvisoryVersionV1)
	}
	canonical, err := tariff.Canonical()
	if err != nil {
		t.Fatalf("canonicalize tariff: %v", err)
	}
	if canonical.SupportAdvisoryVersion != SupportAdvisoryVersionV1 {
		t.Fatalf("canonical tariff version = %q, want %q", canonical.SupportAdvisoryVersion, SupportAdvisoryVersionV1)
	}
	snapshotJSON, err := json.Marshal(canonical)
	if err != nil {
		t.Fatalf("marshal canonical tariff: %v", err)
	}
	var snapshotRoundTrip TariffSnapshot
	if err := json.Unmarshal(snapshotJSON, &snapshotRoundTrip); err != nil {
		t.Fatalf("unmarshal canonical tariff: %v", err)
	}
	if snapshotRoundTrip.SupportAdvisoryVersion != SupportAdvisoryVersionV1 {
		t.Fatalf("snapshot JSON round-trip version = %q, want %q", snapshotRoundTrip.SupportAdvisoryVersion, SupportAdvisoryVersionV1)
	}
}

func TestSupportAdvisoryVersionContentHash(t *testing.T) {
	base := supportAdvisoryTestTariff("")
	legacy, err := base.Canonical()
	if err != nil {
		t.Fatalf("canonicalize legacy tariff: %v", err)
	}
	v1 := supportAdvisoryTestTariff(SupportAdvisoryVersionV1)
	canonical, err := v1.Canonical()
	if err != nil {
		t.Fatalf("canonicalize v1 tariff: %v", err)
	}
	if canonical.ContentHash() == legacy.ContentHash() {
		t.Fatalf("v1 and legacy content hashes are equal: %s", canonical.ContentHash())
	}
	if got := canonical.ContentHash(); got != v1TariffContentHash {
		t.Fatalf("v1 content hash = %s, want independently pinned %s", got, v1TariffContentHash)
	}
	if got := sha256.Sum256([]byte(v1TariffHashPreimage)); hex.EncodeToString(got[:]) != v1TariffContentHash {
		t.Fatalf("independent v1 preimage digest = %s, want %s", hex.EncodeToString(got[:]), v1TariffContentHash)
	}
}

func TestSupportAdvisoryVersionRejectsUnknownValues(t *testing.T) {
	for _, version := range []string{
		"component-support-advisory-v2",
		" component-support-advisory-v1",
		"component-support-advisory-v1 ",
	} {
		t.Run(version, func(t *testing.T) {
			tariff := supportAdvisoryTestTariff(version)
			if err := tariff.Validate(); !errors.Is(err, ErrInvalidTariffSnapshot) {
				t.Errorf("snapshot validation error = %v, want ErrInvalidTariffSnapshot", err)
			}
			view := RatingCatalogView{Currency: "USD", SupportAdvisoryVersion: version}
			if err := view.Validate(); !errors.Is(err, ErrInvalidTariffSnapshot) {
				t.Errorf("view validation error = %v, want ErrInvalidTariffSnapshot", err)
			}
		})
	}
}

func TestSupportAdvisoryVersionFrozenIdentity(t *testing.T) {
	legacy, err := supportAdvisoryTestTariff("").Canonical()
	if err != nil {
		t.Fatalf("canonicalize legacy tariff: %v", err)
	}
	changed := legacy.Clone()
	changed.SupportAdvisoryVersion = SupportAdvisoryVersionV1
	if _, err := changed.Canonical(); !errors.Is(err, ErrTariffSnapshotConflict) {
		t.Errorf("changing version under frozen identity error = %v, want ErrTariffSnapshotConflict", err)
	}

	newIdentity := changed.Clone()
	newIdentity.Ref.Version = "v2"
	newIdentity.Content = SnapshotContentRef{}
	canonical, err := newIdentity.Canonical()
	if err != nil {
		t.Fatalf("canonicalize v1 tariff under new identity: %v", err)
	}
	wantRef := TariffSnapshotContentRefV1 + "://" + newIdentity.Ref.ID + "/v2"
	if canonical.Content.ContentRef != wantRef {
		t.Fatalf("new content ref = %q, want %q", canonical.Content.ContentRef, wantRef)
	}
}

func supportAdvisoryTestRef(version string) RatingSnapshotRef {
	return RatingSnapshotRef{
		VersionRef: VersionRef{ID: "advisory-tariff", Version: version},
		RaterID:    "advisory-rater",
	}
}

func supportAdvisoryTestTariff(version string) TariffSnapshot {
	return TariffSnapshot{
		Ref:                    supportAdvisoryTestRef("v1"),
		Currency:               "USD",
		Rules:                  []RatingRule{},
		SupportAdvisoryVersion: version,
	}
}
