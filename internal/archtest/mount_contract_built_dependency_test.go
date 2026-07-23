package archtest

import (
	"strings"
	"testing"
)

// TestMountContract_DesiredStandardHTTPInputGroupsDeclared is RED until Task 3.2
// introduces production StandardHTTPInput / HTTP*Input groups in stdhttp.
func TestMountContract_DesiredStandardHTTPInputGroupsDeclared(t *testing.T) {
	t.Parallel()
	got := scanStdhttpMountContract(t)
	var missing []string
	for _, name := range desiredStandardHTTPTypes {
		if !got.DeclaredTypes[name] {
			missing = append(missing, name)
		}
	}
	if len(missing) > 0 {
		t.Fatalf("Task 3.1 RED: production stdhttp missing desired cohesive HTTP input types %v (introduce in Task 3.2)", missing)
	}
	for field, wantType := range desiredStandardHTTPGroupFields {
		if got.StandardHTTPFields[field] != wantType {
			t.Fatalf("StandardHTTPInput.%s type=%q want %s", field, got.StandardHTTPFields[field], wantType)
		}
		if got.StandardHTTPFieldIsPointer[field] {
			t.Fatalf("StandardHTTPInput.%s must be value %s, not pointer", field, wantType)
		}
	}
	var ptrFindings []string
	for _, f := range got.Findings {
		if f.Kind == "pointer_group_field" {
			ptrFindings = append(ptrFindings, f.String())
		}
	}
	if len(ptrFindings) > 0 {
		t.Fatalf("StandardHTTPInput group fields must be non-pointer values:\n%s", strings.Join(ptrFindings, "\n"))
	}
}

// TestBuiltDependency_StdhttpMountSignaturesProhibitBuilt is a ratchet gate:
// fails while strict mount helpers / focused composer / their input structs still
// accept *runtimebundle.Built or RequestPlane; passes once Task 3.2 clears those
// findings. Transitional adapters (NewStandardHandler) may retain broad source
// signatures until Phase 4 and are excluded from this strict failure set.
// ComposeRequestPlane was deleted in Task 3.5. Intentionally not allowlisted —
// evidence for Task 3.2 / 3.5.
func TestBuiltDependency_StdhttpMountSignaturesProhibitBuilt(t *testing.T) {
	t.Parallel()
	got := scanStdhttpMountContract(t)
	bad := collectStrictTask32Findings(got.Findings, broadBagFindingKinds)
	if len(bad) > 0 {
		t.Fatalf("Task 3.1 RED: strict stdhttp mount/composer surfaces still depend on broad Built/RequestPlane (%d findings):\n%s",
			len(bad), strings.Join(bad, "\n"))
	}
}

// TestMountContract_DesiredNoLifecycleOwnersInGroups fails when desired groups
// are absent (RED now) or when present groups carry prohibited lifecycle fields
// or generic dependency getters.
func TestMountContract_DesiredNoLifecycleOwnersInGroups(t *testing.T) {
	t.Parallel()
	got := scanStdhttpMountContract(t)
	var missing []string
	for _, name := range desiredStandardHTTPTypes {
		if name == "StandardHTTPInput" {
			continue
		}
		if !got.DeclaredTypes[name] {
			missing = append(missing, name)
		}
	}
	if len(missing) > 0 {
		t.Fatalf("Task 3.1 RED: cannot verify lifecycle-free groups; missing declarations %v", missing)
	}
	var life []string
	for _, f := range got.Findings {
		switch f.Kind {
		case "lifecycle_field", "lifecycle_owner", "service_locator",
			"arbitrary_any_field", "arbitrary_map_field", "generic_getter_field":
			life = append(life, f.String())
		}
	}
	if len(life) > 0 {
		t.Fatalf("prohibited lifecycle/service-locator/any/map/getter fields in HTTP mount groups:\n%s", strings.Join(life, "\n"))
	}
}

// TestMountContract_DesiredMountHelpersAcceptOnlyAllowedGroups is RED until
// strict mount helpers / prepareStandardHandler take cohesive groups (and only
// the groups they need). Transitional adapter source signatures are out of scope.
func TestMountContract_DesiredMountHelpersAcceptOnlyAllowedGroups(t *testing.T) {
	t.Parallel()
	got := scanStdhttpMountContract(t)
	broad := collectStrictTask32Findings(got.Findings, task32StrictFailureKinds)
	if len(broad) == 0 {
		// After Task 3.2, also require each under-contract helper to reference
		// only allowlisted groups (excess_group findings). If no findings and
		// types exist, pass.
		for _, name := range desiredStandardHTTPTypes {
			if !got.DeclaredTypes[name] {
				t.Fatalf("Task 3.1 RED: focused group contract not satisfied; missing %s", name)
			}
		}
		return
	}
	t.Fatalf("Task 3.1 RED: strict mount helpers must accept only cohesive groups (no Built/RequestPlane, no excess groups) (%d findings):\n%s",
		len(broad), strings.Join(broad, "\n"))
}
