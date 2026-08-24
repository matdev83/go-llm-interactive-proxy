package runtime

import "github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"

type (
	CancellationCauseClass string // explicit | client_gone | context_done | race_loser | none | other
	CancellationModeClass  string // none | provider | transport | close_only | other
	CancellationPhase      string // requested | outcome | forced | terminal | none | other
	CancellationFallback   string // negotiated | legacy | none | other
)

const (
	CancellationCauseExplicit    CancellationCauseClass = "explicit"
	CancellationCauseClientGone  CancellationCauseClass = "client_gone"
	CancellationCauseContextDone CancellationCauseClass = "context_done"
	CancellationCauseRaceLoser   CancellationCauseClass = "race_loser"
	CancellationCauseNone        CancellationCauseClass = "none"
	CancellationCauseOther       CancellationCauseClass = "other"

	CancellationModeNone      CancellationModeClass = "none"
	CancellationModeProvider  CancellationModeClass = "provider"
	CancellationModeTransport CancellationModeClass = "transport"
	CancellationModeCloseOnly CancellationModeClass = "close_only"
	CancellationModeOther     CancellationModeClass = "other"

	CancellationPhaseRequested CancellationPhase = "requested"
	CancellationPhaseOutcome   CancellationPhase = "outcome"
	CancellationPhaseForced    CancellationPhase = "forced"
	CancellationPhaseTerminal  CancellationPhase = "terminal"
	CancellationPhaseNone      CancellationPhase = "none"
	CancellationPhaseOther     CancellationPhase = "other"

	CancellationFallbackNegotiated CancellationFallback = "negotiated"
	CancellationFallbackLegacy     CancellationFallback = "legacy"
	CancellationFallbackNone       CancellationFallback = "none"
	CancellationFallbackOther      CancellationFallback = "other"
)

type CancellationObservation struct {
	CauseClass CancellationCauseClass
	Mode       CancellationModeClass
	Phase      CancellationPhase
	Fallback   CancellationFallback
}

func newCancellationObservation(cause lipapi.CancelKind, mode lipapi.CancelMode, phase string, fallback string) CancellationObservation {
	return CancellationObservation{
		CauseClass: normalizeCancellationCauseClass(cause),
		Mode:       normalizeCancellationModeClass(mode),
		Phase:      normalizeCancellationPhase(phase),
		Fallback:   normalizeCancellationFallback(fallback),
	}
}

func normalizeCancellationCauseClass(cause lipapi.CancelKind) CancellationCauseClass {
	switch cause {
	case lipapi.CancelExplicit:
		return CancellationCauseExplicit
	case lipapi.CancelClientGone:
		return CancellationCauseClientGone
	case lipapi.CancelContextDone:
		return CancellationCauseContextDone
	case lipapi.CancelRaceLoser:
		return CancellationCauseRaceLoser
	case "":
		return CancellationCauseNone
	default:
		return CancellationCauseOther
	}
}

func normalizeCancellationModeClass(mode lipapi.CancelMode) CancellationModeClass {
	switch mode {
	case lipapi.CancelModeNone:
		return CancellationModeNone
	case lipapi.CancelModeProvider:
		return CancellationModeProvider
	case lipapi.CancelModeTransport:
		return CancellationModeTransport
	case lipapi.CancelModeCloseOnly:
		return CancellationModeCloseOnly
	case "":
		return CancellationModeNone
	default:
		return CancellationModeOther
	}
}

func normalizeCancellationPhase(phase string) CancellationPhase {
	switch phase {
	case "requested":
		return CancellationPhaseRequested
	case "outcome":
		return CancellationPhaseOutcome
	case "forced":
		return CancellationPhaseForced
	case "terminal":
		return CancellationPhaseTerminal
	case "", "none":
		return CancellationPhaseNone
	default:
		return CancellationPhaseOther
	}
}

func normalizeCancellationFallback(fallback string) CancellationFallback {
	switch fallback {
	case "negotiated":
		return CancellationFallbackNegotiated
	case "legacy":
		return CancellationFallbackLegacy
	case "", "none":
		return CancellationFallbackNone
	default:
		return CancellationFallbackOther
	}
}
