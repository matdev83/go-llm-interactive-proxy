package sessionclassification

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/session"
	sdk "github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/sessionclassification"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/workspace"
)

type memState struct {
	mu sync.Mutex
	r  map[Key]Record
}

func newMemState() *memState { return &memState{r: map[Key]Record{}} }
func (m *memState) Load(_ context.Context, k Key) (Record, bool, error) { m.mu.Lock(); defer m.mu.Unlock(); r, ok := m.r[k]; return r, ok, nil }
func (m *memState) Promote(_ context.Context, k Key, p session.Classification, now time.Time) (Record, bool, error) {
	m.mu.Lock(); defer m.mu.Unlock()
	if r, ok := m.r[k]; ok && r.Classification.IsCodingAgent() { return r, false, nil }
	p.Revision = 1
	r := Record{Key:k, Classification:p, UpdatedAt:now}; m.r[k]=r; return r,true,nil
}
func (m *memState) ClaimRemote(_ context.Context, k Key, now time.Time, max uint32, ttl, backoff time.Duration) (RemoteClaim, Record, bool, error) {
	m.mu.Lock(); defer m.mu.Unlock(); r:=m.r[k]
	if r.Classification.IsCodingAgent() || r.RemoteAttempts>=max || now.Before(r.RemoteLeaseUntil) || now.Before(r.RemoteNextEligibleAt) { return RemoteClaim{},r,false,nil }
	r.Key=k; r.RemoteAttempts++; r.RemoteLeaseID="lease"; r.RemoteLeaseUntil=now.Add(ttl); m.r[k]=r
	return RemoteClaim{Key:k,LeaseID:"lease",Attempt:r.RemoteAttempts},r,true,nil
}
func (m *memState) CompleteRemote(_ context.Context, c RemoteClaim, x RemoteCompletion, now time.Time) (Record,error) {
	m.mu.Lock(); defer m.mu.Unlock(); r:=m.r[c.Key]; r.RemoteLeaseID=""; r.RemoteLeaseUntil=time.Time{}; r.UpdatedAt=now
	if x.Proposal.IsCodingAgent() && !r.Classification.IsCodingAgent(){x.Proposal.Revision=1;r.Classification=x.Proposal}; m.r[c.Key]=r; return r,nil
}

func TestHeuristicSignals(t *testing.T) {
	st:=newMemState(); c,err:=NewClassifier(Config{Mode:ModeHeuristic},st,nil,func()time.Time{return time.Unix(1,0)})
	if err!=nil{t.Fatal(err)}
	cases:=[]struct{name,ua string; tools []string; markers []string; want bool}{
		{"codex","codex_cli_rs/0.1",nil,nil,true},
		{"generic","OpenAI/JS 5.0",nil,nil,false},
		{"cluster","",[]string{"read_file","apply_patch","bash"},nil,true},
		{"weak-project","",[]string{"read_file","bash"},[]string{"go.mod"},true},
		{"weak-no-project","",[]string{"read_file","bash"},[]string{".git"},false},
		{"web-only","",[]string{"web_search"},[]string{"go.mod"},false},
	}
	for i,tc:=range cases{t.Run(tc.name,func(t *testing.T){
		var cats sdk.ToolCategorySet; for _,n:=range tc.tools{cats=cats.AddToolName(n)}
		got,err:=c.Classify(context.Background(),sdk.Input{Session:session.SessionView{ALegID:tc.name+string(rune('a'+i))},Workspace:workspace.WorkspaceView{Markers:tc.markers},Evidence:sdk.Evidence{Operation:lipapi.OperationOpenAIResponses,ClientUserAgent:tc.ua,ToolCategories:cats}})
		if err!=nil{t.Fatal(err)}; if got.IsCodingAgent()!=tc.want{t.Fatalf("coding=%v want %v: %+v",got.IsCodingAgent(),tc.want,got)}
	})}
}
