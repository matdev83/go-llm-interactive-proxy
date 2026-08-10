package contrib

import (
	"testing"
)

func TestDerive_UsesFocusedFacetsAndSyntheticViews(t *testing.T) {
	frontend := FrontendContribution{
		Registration: FrontendRegistrationFacet{ID: "synthetic-frontend"},
		Routes:       &RouteFacet{Declared: true, Claims: []RouteClaimFacet{{Method: "post", Path: "/synthetic/invoke", OperationID: "contoso.invoke"}}},
		Diagnostics:  &DiagnosticFacet{Declared: true},
		Contract:     ContractFacet{Subject: ContractSubject{ID: "synthetic-frontend", Kind: "frontend"}},
	}
	backend := BackendContribution{
		Registration: BackendRegistrationFacet{ID: "synthetic-backend"},
		Contract:     ContractFacet{Subject: ContractSubject{ID: "synthetic-backend", Kind: "backend"}},
		Compatible:   &CompatibleFamilyFacet{FamilyID: "synthetic-family", ProfileIDs: []string{"synthetic-profile"}},
	}

	views, err := Derive(ContributionSet{Frontends: []FrontendContribution{frontend}, Backends: []BackendContribution{backend}})
	if err != nil {
		t.Fatal(err)
	}
	if got := views.FrontendIDs(); len(got) != 1 || got[0] != "synthetic-frontend" {
		t.Fatalf("frontend ids = %v", got)
	}
	if got := views.BackendIDs(); len(got) != 1 || got[0] != "synthetic-backend" {
		t.Fatalf("backend ids = %v", got)
	}
	if len(views.Routes) != 1 || len(views.Diagnostics) != 1 || len(views.ContractSubjects) != 2 {
		t.Fatalf("derived facets = routes %d diagnostics %d contracts %d", len(views.Routes), len(views.Diagnostics), len(views.ContractSubjects))
	}
	if len(views.RouteClaims) != 1 || views.RouteClaims[0].OperationID != "contoso.invoke" {
		t.Fatalf("route claims = %v", views.RouteClaims)
	}
	if views.ProfileFamilies["synthetic-profile"] != "synthetic-family" {
		t.Fatalf("profile families = %v", views.ProfileFamilies)
	}
}

func TestDerive_RejectsDuplicateContractAndRouteClaims(t *testing.T) {
	_, err := Derive(ContributionSet{Frontends: []FrontendContribution{
		{Registration: FrontendRegistrationFacet{ID: "one"}, Contract: ContractFacet{Subject: ContractSubject{ID: "same", Kind: "frontend"}}},
		{Registration: FrontendRegistrationFacet{ID: "two"}, Contract: ContractFacet{Subject: ContractSubject{ID: "same", Kind: "frontend"}}},
	}})
	if err == nil {
		t.Fatal("expected duplicate contract subject error")
	}
	_, err = Derive(ContributionSet{Frontends: []FrontendContribution{
		{Registration: FrontendRegistrationFacet{ID: "one"}, Routes: &RouteFacet{Declared: true, Claims: []RouteClaimFacet{{Method: "POST", Path: "/same", OperationID: "one"}}}},
		{Registration: FrontendRegistrationFacet{ID: "two"}, Routes: &RouteFacet{Declared: true, Claims: []RouteClaimFacet{{Method: "post", Path: "/same/", OperationID: "two"}}}},
	}})
	if err == nil {
		t.Fatal("expected duplicate route claim error")
	}
}

func TestDerive_RejectsDuplicateIDsAndFamilies(t *testing.T) {
	_, err := Derive(ContributionSet{
		Frontends: []FrontendContribution{
			{Registration: FrontendRegistrationFacet{ID: "duplicate"}},
			{Registration: FrontendRegistrationFacet{ID: "duplicate"}},
		},
	})
	if err == nil {
		t.Fatal("expected duplicate frontend id error")
	}

	_, err = Derive(ContributionSet{Backends: []BackendContribution{
		{Registration: BackendRegistrationFacet{ID: "one"}, Compatible: &CompatibleFamilyFacet{FamilyID: "family"}},
		{Registration: BackendRegistrationFacet{ID: "two"}, Compatible: &CompatibleFamilyFacet{FamilyID: "family"}},
	}})
	if err == nil {
		t.Fatal("expected duplicate family error")
	}
}

func TestDerive_PreservesDeclarationOrderAndExcludesOptionalBackends(t *testing.T) {
	views, err := Derive(ContributionSet{
		Frontends: []FrontendContribution{
			{Registration: FrontendRegistrationFacet{ID: "z-frontend"}},
			{Registration: FrontendRegistrationFacet{ID: "a-frontend"}},
		},
		Backends: []BackendContribution{
			{Registration: BackendRegistrationFacet{ID: "builtin", Source: SourceBuiltin}},
			{Registration: BackendRegistrationFacet{ID: "optional", Source: "discovered"}},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if got := views.FrontendIDs(); len(got) != 2 || got[0] != "z-frontend" || got[1] != "a-frontend" {
		t.Fatalf("frontend order = %v", got)
	}
	if len(views.EssentialIDs) != 1 || views.EssentialIDs[0] != "builtin" {
		t.Fatalf("essential ids = %v", views.EssentialIDs)
	}
}
