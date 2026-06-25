package archtest

import "testing"

var opencodeCommonForbiddenConcreteBackends = []forbiddenDep{
	{
		Substr: "/internal/plugins/backends/anthropic",
		ErrMsg: "opencodecommon must not import concrete anthropic backend; use shared protocol adapter",
	},
	{
		Substr: "/internal/plugins/backends/gemini",
		ErrMsg: "opencodecommon must not import concrete gemini backend; use shared protocol adapter",
	},
	{
		Substr: "/internal/plugins/backends/openrouter",
		ErrMsg: "opencodecommon must not import concrete openrouter backend",
	},
	{
		Substr: "/internal/plugins/backends/nvidia",
		ErrMsg: "opencodecommon must not import concrete nvidia backend",
	},
}

func TestOpenCodeCommonDoesNotImportConcreteProviderBackends(t *testing.T) {
	t.Parallel()
	assertDepsExcludeForbidden(
		t,
		[]string{"./internal/plugins/backends/opencodecommon/..."},
		opencodeCommonForbiddenConcreteBackends,
	)
}
