// Package workspace implements core-owned workspace resolution chains for the execution snapshot (design §9, R5).
package workspace

import (
	"context"
	"maps"
	"strings"

	lipworkspace "github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/workspace"
)

// ResolverChain runs workspace [lipworkspace.Resolver] entries in registration order with fail-open
// semantics: resolver errors are ignored and the next resolver runs.
type ResolverChain struct {
	list []lipworkspace.Resolver
}

// NewResolverChain returns a [lipworkspace.Resolver] backed by resolvers (nil entries skipped).
// An empty list returns [lipworkspace.DisabledResolver].
func NewResolverChain(resolvers []lipworkspace.Resolver) lipworkspace.Resolver {
	var filtered []lipworkspace.Resolver
	for _, r := range resolvers {
		if r == nil {
			continue
		}
		filtered = append(filtered, r)
	}
	if len(filtered) == 0 {
		return lipworkspace.DisabledResolver{}
	}
	return ResolverChain{list: filtered}
}

// Resolve implements [lipworkspace.Resolver].
func (c ResolverChain) Resolve(ctx context.Context) (lipworkspace.WorkspaceView, error) {
	var out lipworkspace.WorkspaceView
	for _, r := range c.list {
		v, err := r.Resolve(ctx)
		if err != nil {
			continue
		}
		out = mergeWorkspaceViews(out, v)
	}
	return out, nil
}

func mergeWorkspaceViews(base, add lipworkspace.WorkspaceView) lipworkspace.WorkspaceView {
	if strings.TrimSpace(add.ProjectRoot) != "" {
		base.ProjectRoot = strings.TrimSpace(add.ProjectRoot)
	}
	base.DirtyTree = base.DirtyTree || add.DirtyTree
	if len(add.Markers) > 0 {
		seen := map[string]struct{}{}
		for _, m := range base.Markers {
			seen[m] = struct{}{}
		}
		for _, m := range add.Markers {
			if _, ok := seen[m]; ok {
				continue
			}
			seen[m] = struct{}{}
			base.Markers = append(base.Markers, m)
		}
	}
	if len(add.Labels) > 0 {
		if base.Labels == nil {
			base.Labels = map[string]string{}
		}
		maps.Copy(base.Labels, add.Labels)
	}
	return base
}
