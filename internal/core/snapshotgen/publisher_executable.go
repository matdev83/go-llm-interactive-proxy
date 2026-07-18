package snapshotgen

import (
	"fmt"
	"time"

	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/economics"
)

// PublishExecutable validates and publishes an executable generation.
// On validation failure the prior executable generation remains active (9.6).
func (p *Publisher) PublishExecutable(gen *ExecutableGeneration) (*ExecutableGeneration, error) {
	if p == nil {
		return nil, fmt.Errorf("snapshotgen: nil publisher")
	}
	if gen == nil {
		return nil, fmt.Errorf("snapshotgen: nil executable generation")
	}
	if err := gen.ValidateComplete(); err != nil {
		return p.CurrentExecutable(), err
	}
	if gen.PublishedAt.IsZero() {
		gen.PublishedAt = time.Now().UTC()
	}
	if gen.State == "" {
		gen.State = economics.SnapshotReady
	}
	gen.ID = p.seq.Add(1)
	p.executable.Store(gen)
	p.retainExecutable(gen)
	meta := RuntimeGeneration{
		ID:          gen.ID,
		PublishedAt: gen.PublishedAt,
		State:       gen.State,
		Reason:      gen.Reason,
		Usage:       gen.Usage,
		Concurrency: gen.Concurrency,
		Rating:      gen.Rating,
	}
	p.active.Store(&meta)
	return gen, nil
}

// CurrentExecutable returns the active executable generation, if any.
func (p *Publisher) CurrentExecutable() *ExecutableGeneration {
	if p == nil {
		return nil
	}
	return p.executable.Load()
}

// LookupExecutable returns a retained generation by ID (in-flight / pending drain).
func (p *Publisher) LookupExecutable(id int64) *ExecutableGeneration {
	if p == nil || id <= 0 {
		return nil
	}
	if v, ok := p.retained.Load(id); ok {
		return v.(*ExecutableGeneration)
	}
	return nil
}

// UnresolvedProviderIDs aggregates pending provider IDs across retained generations.
func (p *Publisher) UnresolvedProviderIDs() []string {
	if p == nil {
		return nil
	}
	seen := map[string]struct{}{}
	var out []string
	p.retained.Range(func(_, value any) bool {
		gen := value.(*ExecutableGeneration)
		for _, id := range gen.PendingProviderIDs() {
			if _, ok := seen[id]; ok {
				continue
			}
			seen[id] = struct{}{}
			out = append(out, id)
		}
		return true
	})
	return out
}

func (p *Publisher) retainExecutable(gen *ExecutableGeneration) {
	if p == nil || gen == nil {
		return
	}
	p.retained.Store(gen.ID, gen)
}
