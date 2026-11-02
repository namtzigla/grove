package main

import (
	"context"
	"os"
	"os/exec"
	"sync"
	"time"
)

// Result is the outcome of running something in one repository.
type Result struct {
	Repo LocalRepo
	Out  []byte
	Err  error
	Dur  time.Duration
}

// forEach runs fn in up to jobs repositories at a time. done is called
// serially (under a mutex) as results complete, in completion order.
func forEach(ctx context.Context, repos []LocalRepo, jobs int, fn func(context.Context, LocalRepo) ([]byte, error), done func(Result) (stop bool)) {
	if jobs < 1 {
		jobs = 1
	}
	var (
		wg   sync.WaitGroup
		mu   sync.Mutex
		sem  = make(chan struct{}, jobs)
		stop = false
	)
	for _, r := range repos {
		mu.Lock()
		s := stop
		mu.Unlock()
		if s {
			break
		}
		select {
		case <-ctx.Done():
			wg.Wait()
			return
		case sem <- struct{}{}:
		}
		wg.Add(1)
		go func(r LocalRepo) {
			defer wg.Done()
			defer func() { <-sem }()
			start := time.Now()
			out, err := fn(ctx, r)
			res := Result{Repo: r, Out: out, Err: err, Dur: time.Since(start)}
			mu.Lock()
			if done(res) {
				stop = true
			}
			mu.Unlock()
		}(r)
	}
	wg.Wait()
}

// repoEnv is exported to commands run by `each` / `git`.
func repoEnv(r LocalRepo) []string {
	return []string{
		"GROVE_HOST=" + r.Host,
		"GROVE_PATH=" + r.Path,
		"GROVE_NAME=" + r.Name(),
		"GROVE_DIR=" + r.Dir,
		"GROVE_FULL=" + r.Full(),
		"GIT_TERMINAL_PROMPT=0",
	}
}

// execIn runs a command in dir and returns combined stdout+stderr.
func execIn(ctx context.Context, dir string, env []string, name string, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), env...)
	return cmd.CombinedOutput()
}

// execOut runs a command and returns stdout only (stderr discarded).
func execOut(dir string, name string, args ...string) ([]byte, error) {
	cmd := exec.Command(name, args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0")
	return cmd.Output()
}

func gitIn(ctx context.Context, r LocalRepo, args ...string) ([]byte, error) {
	return execIn(ctx, r.Dir, repoEnv(r), "git", args...)
}
