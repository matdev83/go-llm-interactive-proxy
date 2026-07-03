package runtime

import (
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/execctx"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/execview"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/request"
)

func decisionViewsFromRequestMeta(meta request.RequestMeta, attempt execview.AttemptView) execctx.Views {
	return execctx.Views{
		Principal:   meta.Principal,
		Scope:       meta.Scope,
		Session:     meta.Session,
		Attempt:     attempt,
		Workspace:   meta.Workspace,
		Annotations: meta.Annotations,
	}
}
