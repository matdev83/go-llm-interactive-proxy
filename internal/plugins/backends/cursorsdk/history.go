package cursorsdk

type HistoryMarker struct {
	MessageCount      int
	PrefixHash        string
	LastTurnID        string
	AgentIdentityHash string
	ProcessGeneration int64
}

// TranscriptView is a caller-owned fingerprint of the canonical transcript.
// Invariants for PlanHistory against a committed marker with MessageCount=N, PrefixHash=P:
//   - PrefixHash hashes the full current transcript (all MessageCount turns).
//   - HeadPrefixHash must hash turns[0:N] from the current transcript (empty only when N==0).
//   - Same-length edit/reorder: MessageCount==N and HeadPrefixHash!=P => ResetNeeded.
//   - Truncation: MessageCount<N => ResetNeeded.
//   - Append-only: MessageCount>N and HeadPrefixHash==P => Incremental.
type TranscriptView struct {
	MessageCount   int
	PrefixHash     string
	HeadPrefixHash string
	LastTurnID     string
}

type HistoryMode int

const (
	HistoryBootstrap HistoryMode = iota
	HistoryIncremental
	HistoryRetry
)

type HistoryPlan struct {
	Mode          HistoryMode
	ResetNeeded   bool
	UseFullPrompt bool
	NextMarker    HistoryMarker
}

func PlanHistory(view TranscriptView, committed HistoryMarker, key AgentKey, processGen int64) HistoryPlan {
	next := HistoryMarker{
		MessageCount:      view.MessageCount,
		PrefixHash:        view.PrefixHash,
		LastTurnID:        view.LastTurnID,
		AgentIdentityHash: key.IdentityHash(),
		ProcessGeneration: processGen,
	}
	if committed.MessageCount == 0 || committed.PrefixHash == "" {
		return HistoryPlan{Mode: HistoryBootstrap, UseFullPrompt: true, NextMarker: next}
	}
	if committed.AgentIdentityHash != "" && committed.AgentIdentityHash != key.IdentityHash() {
		return HistoryPlan{Mode: HistoryBootstrap, ResetNeeded: true, UseFullPrompt: true, NextMarker: next}
	}
	if committed.ProcessGeneration != 0 && committed.ProcessGeneration != processGen {
		return HistoryPlan{Mode: HistoryBootstrap, ResetNeeded: true, UseFullPrompt: true, NextMarker: next}
	}
	if view.MessageCount < committed.MessageCount || view.HeadPrefixHash != committed.PrefixHash {
		return HistoryPlan{Mode: HistoryBootstrap, ResetNeeded: true, UseFullPrompt: true, NextMarker: next}
	}
	if view.MessageCount == committed.MessageCount {
		if view.PrefixHash != committed.PrefixHash {
			return HistoryPlan{Mode: HistoryBootstrap, ResetNeeded: true, UseFullPrompt: true, NextMarker: next}
		}
		return HistoryPlan{Mode: HistoryRetry, NextMarker: committed}
	}
	return HistoryPlan{Mode: HistoryIncremental, NextMarker: next}
}
