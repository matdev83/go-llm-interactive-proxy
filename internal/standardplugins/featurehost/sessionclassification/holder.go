package sessionclassification

import (
	"context"
	"fmt"
	"sync"
	"time"

	feature "github.com/matdev83/go-llm-interactive-proxy/internal/plugins/features/sessionclassification"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/session"
	"github.com/uptrace/bun"
)

const defaultPositiveCacheCapacity = 4096

type Holder struct {
	mu          sync.RWMutex
	db          *bun.DB
	store       Store
	initialized bool
	closed      bool
	positives   map[feature.Key]session.Classification
}

func NewHolder(db *bun.DB)*Holder{return &Holder{db:db,positives:make(map[feature.Key]session.Classification)}}

func (h *Holder) Ensure(ctx context.Context) error {
	if h==nil{return fmt.Errorf("session-classification: nil holder")}
	h.mu.Lock();defer h.mu.Unlock()
	if h.closed{return fmt.Errorf("session-classification: holder closed")}
	if h.initialized{return nil}
	var s Store
	if h.db!=nil{s=NewBunStore(h.db)}else{s=NewMemoryStore()}
	if err:=s.EnsureSchema(ctx);err!=nil{return err}
	h.store=s;h.initialized=true;return nil
}

func (h *Holder) currentStore()(Store,error){
	h.mu.RLock();defer h.mu.RUnlock()
	if h.closed{return nil,fmt.Errorf("session-classification: holder closed")}
	if !h.initialized||h.store==nil{return nil,fmt.Errorf("session-classification: holder not initialized")}
	return h.store,nil
}
func (h *Holder) cached(k feature.Key)(session.Classification,bool){h.mu.RLock();defer h.mu.RUnlock();c,ok:=h.positives[k];return c,ok}
func (h *Holder) cache(k feature.Key,c session.Classification){if !c.IsCodingAgent(){return};h.mu.Lock();defer h.mu.Unlock();if len(h.positives)>=defaultPositiveCacheCapacity{for x:=range h.positives{delete(h.positives,x);break}};h.positives[k]=c}

func (h *Holder) Load(ctx context.Context,k feature.Key)(feature.Record,bool,error){
	if c,ok:=h.cached(k);ok{return feature.Record{Key:k,Classification:c},true,nil};s,err:=h.currentStore();if err!=nil{return feature.Record{},false,err};r,ok,err:=s.Load(ctx,k);if err==nil&&ok{h.cache(k,r.Classification)};return r,ok,err
}
func (h *Holder) Promote(ctx context.Context,k feature.Key,p session.Classification,now time.Time)(feature.Record,bool,error){s,err:=h.currentStore();if err!=nil{return feature.Record{},false,err};r,prom,err:=s.Promote(ctx,k,p,now);if err==nil{h.cache(k,r.Classification)};return r,prom,err}
func (h *Holder) ClaimRemote(ctx context.Context,k feature.Key,now time.Time,max uint32,ttl,backoff time.Duration)(feature.RemoteClaim,feature.Record,bool,error){
	if c,ok:=h.cached(k);ok{return feature.RemoteClaim{},feature.Record{Key:k,Classification:c},false,nil};s,err:=h.currentStore();if err!=nil{return feature.RemoteClaim{},feature.Record{},false,err};return s.ClaimRemote(ctx,k,now,max,ttl,backoff)
}
func (h *Holder) CompleteRemote(ctx context.Context,c feature.RemoteClaim,x feature.RemoteCompletion,now time.Time)(feature.Record,error){s,err:=h.currentStore();if err!=nil{return feature.Record{},err};r,err:=s.CompleteRemote(ctx,c,x,now);if err==nil{h.cache(c.Key,r.Classification)};return r,err}
func (h *Holder) Close()error{if h==nil{return nil};h.mu.Lock();defer h.mu.Unlock();h.closed=true;h.store=nil;h.positives=nil;return nil}

type GenerationLifecycle struct{ Holder *Holder }
func (l *GenerationLifecycle) Start(ctx context.Context)error{if l==nil||l.Holder==nil{return fmt.Errorf("session-classification: nil generation holder")};return l.Holder.Ensure(ctx)}
func (*GenerationLifecycle) Stop(context.Context)error{return nil}
