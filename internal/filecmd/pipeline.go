package filecmd

import (
	"context"
	"sync"

	"github.com/astralyx/lino/internal/fileio"
	"github.com/astralyx/lino/internal/mutate"
)

// Hooks run after every successful mutation by any file command (edit,
// insert, delete, replace, write, mv, rm), e.g. to re-index and record
// history. Register them before commands run.
var Hooks []mutate.Hook

// Check is the --v check of every line mutation; nil means mutate.Exact.
// Set it before commands run.
var Check mutate.VersionCheck

var pipelines sync.Map // canonical root path -> *mutate.Pipeline

// Pipeline returns the root's shared pipeline, so every mutation of the root
// in this process is serialised by one lock and sees Hooks.
func (w *Workspace) Pipeline() *mutate.Pipeline {
	key := w.Root.Path()
	if p, ok := pipelines.Load(key); ok {
		return p.(*mutate.Pipeline)
	}
	p := &mutate.Pipeline{Root: w.Root, MaxSize: w.Config.MaxFileSize, Hooks: []mutate.Hook{runHooks}, Check: runCheck}
	actual, _ := pipelines.LoadOrStore(key, p)
	return actual.(*mutate.Pipeline)
}

func runHooks(ctx context.Context, c *mutate.Commit) error {
	for _, h := range Hooks {
		if err := h(ctx, c); err != nil {
			return err
		}
	}
	return nil
}

func runCheck(ctx context.Context, f *fileio.File, v string, op mutate.Op, target mutate.Range) error {
	if Check == nil {
		return mutate.Exact(ctx, f, v, op, target)
	}
	return Check(ctx, f, v, op, target)
}

// PreHooks run before a mutation loads the files it touches, e.g. to record
// external edits made since the index last saw them. root is canonical and
// rels are root-relative slash paths. Register them before commands run.
var PreHooks []func(ctx context.Context, root string, rels []string) error

// Prepare runs PreHooks for paths (relative to cwd). Paths that do not
// resolve inside the root are skipped; the mutation itself reports them.
func (w *Workspace) Prepare(ctx context.Context, cwd string, paths ...string) error {
	if len(PreHooks) == 0 {
		return nil
	}
	rels := make([]string, 0, len(paths))
	for _, p := range paths {
		if rel, _, err := w.Root.Jail(cwd, p); err == nil && rel != "." {
			rels = append(rels, rel)
		}
	}
	if len(rels) == 0 {
		return nil
	}
	for _, h := range PreHooks {
		if err := h(ctx, w.Root.Path(), rels); err != nil {
			return err
		}
	}
	return nil
}
