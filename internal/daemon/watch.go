// Package daemon watches sources and reindexes incrementally on change.
package daemon

import (
	"context"
	"log"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/fsnotify/fsnotify"

	"github.com/veridit/muninn/internal/app"
	"github.com/veridit/muninn/internal/index"
)

// Watch reindexes (debounced) whenever a watched source changes. Directories
// are watched, not files, because harnesses replace files atomically.
func Watch(ctx context.Context, a *app.App, mu *sync.Mutex, logf func(string, ...any)) error {
	w, err := fsnotify.NewWatcher()
	if err != nil {
		return err
	}
	defer w.Close()
	o := a.IndexOptions()
	dirs := map[string]bool{}
	add := func(p string) {
		if fi, err := os.Stat(p); err == nil {
			if !fi.IsDir() {
				p = filepath.Dir(p)
			}
			if !dirs[p] {
				dirs[p] = true
				if err := w.Add(p); err != nil {
					logf("watch %s: %v", p, err)
				}
			}
		}
	}
	for _, j := range o.Jcode {
		add(j.Dir)
		add(filepath.Join(j.Dir, "projects"))
	}
	os.MkdirAll(filepath.Dir(o.NativePath), 0o700)
	add(filepath.Dir(o.NativePath))
	for _, f := range index.MarkdownFiles(o.NotesRoots) {
		add(f)
	}
	logf("watching %d directories", len(dirs))

	run := func() {
		mu.Lock()
		defer mu.Unlock()
		st, err := a.Reindex(ctx)
		if err != nil {
			logf("reindex: %v", err)
			return
		}
		if st.Added+st.Changed+st.Removed > 0 {
			logf("reindex: %s", st)
		}
	}
	run()
	var timer *time.Timer
	for {
		select {
		case <-ctx.Done():
			return nil
		case ev, ok := <-w.Events:
			if !ok {
				return nil
			}
			if filepath.Ext(ev.Name) != ".json" && filepath.Ext(ev.Name) != ".md" {
				continue
			}
			if timer != nil {
				timer.Stop()
			}
			timer = time.AfterFunc(500*time.Millisecond, run)
		case err, ok := <-w.Errors:
			if !ok {
				return nil
			}
			logf("watcher: %v", err)
		}
	}
}

// StdLog adapts log.Printf.
func StdLog(format string, args ...any) { log.Printf(format, args...) }
