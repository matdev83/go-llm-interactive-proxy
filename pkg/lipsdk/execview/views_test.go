package execview_test

import (
	"testing"

	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/execview"
)

func TestViewsAreConstructible(t *testing.T) {
	t.Parallel()
	_ = execview.PrincipalView{
		ID: "p1", DisplayName: "n", Roles: []string{"r"},
		Claims: map[string]string{"k": "v"},
	}
	_ = execview.AttemptView{
		TraceID: "t", BLegID: "b", AttemptSeq: 1,
		BackendID: "be", RouteRole: "primary",
	}
}
