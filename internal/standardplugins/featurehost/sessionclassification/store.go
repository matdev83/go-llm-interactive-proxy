package sessionclassification

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	feature "github.com/matdev83/go-llm-interactive-proxy/internal/plugins/features/sessionclassification"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/session"
	"github.com/uptrace/bun"
	"github.com/uptrace/bun/dialect"
)

type Store interface {
	feature.State
	EnsureSchema(context.Context) error
}

type MemoryStore struct {
	mu sync.Mutex
	m  map[feature.Key]feature.Record
}

func NewMemoryStore() *MemoryStore { return &MemoryStore{m: make(map[feature.Key]feature.Record)} }
func (*MemoryStore) EnsureSchema(context.Context) error { return nil }

func (s *MemoryStore) Load(ctx context.Context, key feature.Key) (feature.Record, bool, error) {
	if err:=ctx.Err(); err!=nil{return feature.Record{},false,err}
	if err:=key.Validate(); err!=nil{return feature.Record{},false,err}
	s.mu.Lock(); defer s.mu.Unlock()
	r,ok:=s.m[key]; return r,ok,nil
}
func (s *MemoryStore) Promote(ctx context.Context,key feature.Key,p session.Classification,now time.Time)(feature.Record,bool,error){
	if err:=ctx.Err();err!=nil{return feature.Record{},false,err}; if err:=key.Validate();err!=nil{return feature.Record{},false,err}
	if !p.IsCodingAgent(){return feature.Record{},false,fmt.Errorf("session-classification: promotion must be coding_agent")}
	p.Revision=1; if err:=p.Validate();err!=nil{return feature.Record{},false,err}
	s.mu.Lock(); defer s.mu.Unlock(); r:=s.m[key]
	if r.Classification.IsCodingAgent(){return r,false,nil}
	r.Key=key;r.Classification=p;r.UpdatedAt=now.UTC();s.m[key]=r;return r,true,nil
}
func (s *MemoryStore) ClaimRemote(ctx context.Context,key feature.Key,now time.Time,max uint32,ttl,backoff time.Duration)(feature.RemoteClaim,feature.Record,bool,error){
	if err:=ctx.Err();err!=nil{return feature.RemoteClaim{},feature.Record{},false,err}; if err:=key.Validate();err!=nil{return feature.RemoteClaim{},feature.Record{},false,err}
	s.mu.Lock(); defer s.mu.Unlock(); r:=s.m[key]; now=now.UTC()
	if r.Classification.IsCodingAgent()||r.RemoteAttempts>=max||(!r.RemoteLeaseUntil.IsZero()&&now.Before(r.RemoteLeaseUntil))||(!r.RemoteNextEligibleAt.IsZero()&&now.Before(r.RemoteNextEligibleAt)){return feature.RemoteClaim{},r,false,nil}
	id,err:=leaseID();if err!=nil{return feature.RemoteClaim{},r,false,err}
	r.Key=key;r.RemoteAttempts++;r.RemoteLeaseID=id;r.RemoteLeaseUntil=now.Add(ttl);r.RemoteNextEligibleAt=time.Time{};r.UpdatedAt=now;s.m[key]=r
	return feature.RemoteClaim{Key:key,LeaseID:id,Attempt:r.RemoteAttempts},r,true,nil
}
func (s *MemoryStore) CompleteRemote(ctx context.Context,claim feature.RemoteClaim,c feature.RemoteCompletion,now time.Time)(feature.Record,error){
	if err:=ctx.Err();err!=nil{return feature.Record{},err}
	s.mu.Lock(); defer s.mu.Unlock(); r,ok:=s.m[claim.Key]; if !ok||r.RemoteLeaseID==""||r.RemoteLeaseID!=claim.LeaseID{return feature.Record{},fmt.Errorf("session-classification: stale remote lease")}
	r.RemoteLeaseID="";r.RemoteLeaseUntil=time.Time{};r.UpdatedAt=now.UTC()
	if c.Proposal.IsCodingAgent()&&!r.Classification.IsCodingAgent(){p:=c.Proposal;p.Revision=1;if err:=p.Validate();err!=nil{return feature.Record{},err};r.Classification=p}
	s.m[claim.Key]=r;return r,nil
}

type BunStore struct{ db *bun.DB }
func NewBunStore(db *bun.DB)*BunStore{return &BunStore{db:db}}

func (s *BunStore) EnsureSchema(ctx context.Context) error {
	if s==nil||s.db==nil{return fmt.Errorf("session-classification: nil bun db")}
	_,err:=s.db.ExecContext(ctx,`CREATE TABLE IF NOT EXISTS session_classification (
		scope_kind TEXT NOT NULL,
		scope_id TEXT NOT NULL,
		kind TEXT NOT NULL DEFAULT '',
		source TEXT NOT NULL DEFAULT '',
		confidence TEXT NOT NULL DEFAULT '',
		evidence_code TEXT NOT NULL DEFAULT '',
		classification_revision INTEGER NOT NULL DEFAULT 0,
		remote_attempts INTEGER NOT NULL DEFAULT 0,
		remote_lease_id TEXT NOT NULL DEFAULT '',
		remote_lease_until INTEGER NOT NULL DEFAULT 0,
		remote_next_eligible_at INTEGER NOT NULL DEFAULT 0,
		updated_at INTEGER NOT NULL DEFAULT 0,
		PRIMARY KEY(scope_kind, scope_id)
	)`)
	if err!=nil{return fmt.Errorf("session-classification: ensure schema: %w",err)};return nil
}
func (s *BunStore) Load(ctx context.Context,key feature.Key)(feature.Record,bool,error){
	if err:=key.Validate();err!=nil{return feature.Record{},false,err}
	r,ok,err:=s.load(ctx,s.db,key,false);return r,ok,err
}
func (s *BunStore) Promote(ctx context.Context,key feature.Key,p session.Classification,now time.Time)(feature.Record,bool,error){
	if err:=key.Validate();err!=nil{return feature.Record{},false,err};if !p.IsCodingAgent(){return feature.Record{},false,fmt.Errorf("session-classification: promotion must be coding_agent")}
	p.Revision=1;if err:=p.Validate();err!=nil{return feature.Record{},false,err};var out feature.Record;var promoted bool
	err:=s.db.RunInTx(ctx,nil,func(ctx context.Context,tx bun.Tx)error{
		if err:=s.ensureRow(ctx,tx,key);err!=nil{return err}; before,_,err:=s.load(ctx,tx,key,true);if err!=nil{return err}
		if before.Classification.IsCodingAgent(){out=before;return nil}
		res,err:=tx.NewRaw(`UPDATE session_classification SET kind=?, source=?, confidence=?, evidence_code=?, classification_revision=1, updated_at=? WHERE scope_kind=? AND scope_id=? AND kind=''`,
			string(p.Kind),string(p.Source),string(p.Confidence),string(p.Evidence),now.UTC().UnixNano(),string(key.Kind),key.ID).Exec(ctx);if err!=nil{return err}
		if n,_:=res.RowsAffected();n>0{promoted=true};out,_,err=s.load(ctx,tx,key,false);return err
	});return out,promoted,err
}
func (s *BunStore) ClaimRemote(ctx context.Context,key feature.Key,now time.Time,max uint32,ttl,backoff time.Duration)(feature.RemoteClaim,feature.Record,bool,error){
	if err:=key.Validate();err!=nil{return feature.RemoteClaim{},feature.Record{},false,err};var claim feature.RemoteClaim;var out feature.Record;var ok bool
	err:=s.db.RunInTx(ctx,nil,func(ctx context.Context,tx bun.Tx)error{
		if err:=s.ensureRow(ctx,tx,key);err!=nil{return err};r,_,err:=s.load(ctx,tx,key,true);if err!=nil{return err};now=now.UTC()
		if r.Classification.IsCodingAgent()||r.RemoteAttempts>=max||(!r.RemoteLeaseUntil.IsZero()&&now.Before(r.RemoteLeaseUntil))||(!r.RemoteNextEligibleAt.IsZero()&&now.Before(r.RemoteNextEligibleAt)){out=r;return nil}
		id,err:=leaseID();if err!=nil{return err};attempt:=r.RemoteAttempts+1
		_,err=tx.NewRaw(`UPDATE session_classification SET remote_attempts=?, remote_lease_id=?, remote_lease_until=?, remote_next_eligible_at=0, updated_at=? WHERE scope_kind=? AND scope_id=?`,
			attempt,id,now.Add(ttl).UnixNano(),now.UnixNano(),string(key.Kind),key.ID).Exec(ctx);if err!=nil{return err}
		out,_,err=s.load(ctx,tx,key,false);if err==nil{claim=feature.RemoteClaim{Key:key,LeaseID:id,Attempt:attempt};ok=true};return err
	});return claim,out,ok,err
}
func (s *BunStore) CompleteRemote(ctx context.Context,claim feature.RemoteClaim,c feature.RemoteCompletion,now time.Time)(feature.Record,error){
	var out feature.Record;err:=s.db.RunInTx(ctx,nil,func(ctx context.Context,tx bun.Tx)error{
		r,exists,err:=s.load(ctx,tx,claim.Key,true);if err!=nil{return err};if !exists||r.RemoteLeaseID!=claim.LeaseID||claim.LeaseID==""{return fmt.Errorf("session-classification: stale remote lease")}
		kind,source,confidence,evidence,rev:=string(r.Classification.Kind),string(r.Classification.Source),string(r.Classification.Confidence),string(r.Classification.Evidence),r.Classification.Revision
		if c.Proposal.IsCodingAgent()&&!r.Classification.IsCodingAgent(){p:=c.Proposal;p.Revision=1;if err:=p.Validate();err!=nil{return err};kind,source,confidence,evidence,rev=string(p.Kind),string(p.Source),string(p.Confidence),string(p.Evidence),1}
		_,err=tx.NewRaw(`UPDATE session_classification SET kind=?,source=?,confidence=?,evidence_code=?,classification_revision=?,remote_lease_id='',remote_lease_until=0,updated_at=? WHERE scope_kind=? AND scope_id=? AND remote_lease_id=?`,
			kind,source,confidence,evidence,rev,now.UTC().UnixNano(),string(claim.Key.Kind),claim.Key.ID,claim.LeaseID).Exec(ctx);if err!=nil{return err};out,_,err=s.load(ctx,tx,claim.Key,false);return err
	});return out,err
}
type rawQuerier interface{ NewRaw(string,...any)*bun.RawQuery }
func (s *BunStore) ensureRow(ctx context.Context,q rawQuerier,key feature.Key)error{
	_,err:=q.NewRaw(`INSERT INTO session_classification(scope_kind,scope_id) VALUES(?,?) ON CONFLICT(scope_kind,scope_id) DO NOTHING`,string(key.Kind),key.ID).Exec(ctx);return err
}
func (s *BunStore) load(ctx context.Context,q rawQuerier,key feature.Key,lock bool)(feature.Record,bool,error){
	query:=`SELECT kind,source,confidence,evidence_code,classification_revision,remote_attempts,remote_lease_id,remote_lease_until,remote_next_eligible_at,updated_at FROM session_classification WHERE scope_kind=? AND scope_id=?`
	if lock&&s.db.Dialect().Name()==dialect.PG{query+=" FOR UPDATE"}
	var kind,source,confidence,evidence,lease string;var rev int64;var attempts int64;var leaseUntil,next,updated int64
	err:=q.NewRaw(query,string(key.Kind),key.ID).Scan(ctx,&kind,&source,&confidence,&evidence,&rev,&attempts,&lease,&leaseUntil,&next,&updated)
	if errors.Is(err,sql.ErrNoRows){return feature.Record{},false,nil};if err!=nil{return feature.Record{},false,err}
	r:=feature.Record{Key:key,Classification:session.Classification{Kind:session.Kind(kind),Source:session.ClassificationSource(source),Confidence:session.ConfidenceBand(confidence),Evidence:session.EvidenceCode(evidence),Revision:uint64(rev)},RemoteAttempts:uint32(attempts),RemoteLeaseID:lease}
	if leaseUntil>0{r.RemoteLeaseUntil=time.Unix(0,leaseUntil).UTC()};if next>0{r.RemoteNextEligibleAt=time.Unix(0,next).UTC()};if updated>0{r.UpdatedAt=time.Unix(0,updated).UTC()}
	if err:=r.Classification.Validate();err!=nil{return feature.Record{},false,fmt.Errorf("session-classification: stored classification invalid: %w",err)}
	return r,true,nil
}
func leaseID()(string,error){var b [16]byte;if _,err:=rand.Read(b[:]);err!=nil{return "",err};return hex.EncodeToString(b[:]),nil}
var _ = strings.TrimSpace
