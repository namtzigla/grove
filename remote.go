package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os/exec"
	"sort"
	"strings"
	"sync"
)

// RemoteRepo is a repository as reported by gh / glab.
type RemoteRepo struct {
	Repo
	SSHURL        string `json:"ssh_url"`
	HTTPURL       string `json:"http_url"`
	WebURL        string `json:"web_url"`
	Archived      bool   `json:"archived"`
	DefaultBranch string `json:"default_branch"`
	OwnerKind     string `json:"owner_kind"` // group | user | organization
}

// RemoteOpts tunes remote enumeration.
type RemoteOpts struct {
	All      bool // gitlab: every visible project, not only memberships
	Archived bool // include archived repositories
}

// ListRemote enumerates the repositories on one host, calling emit for each
// as pages arrive. emit may be called from the calling goroutine only.
func ListRemote(ctx context.Context, host string, kind HostKind, opts RemoteOpts, emit func(RemoteRepo)) error {
	switch kind {
	case KindGitHub:
		return listGitHub(ctx, host, opts, emit)
	case KindGitLab:
		return listGitLab(ctx, host, opts, emit)
	default:
		return fmt.Errorf("host %s: unknown kind; add it to the config with \"kind\": \"github\" or \"gitlab\"", host)
	}
}

// ListRemoteStream enumerates several hosts concurrently; emit is serialized.
func ListRemoteStream(ctx context.Context, cfg *Config, hosts []string, opts RemoteOpts, emit func(RemoteRepo)) error {
	var (
		mu   sync.Mutex
		wg   sync.WaitGroup
		errs []error
	)
	for _, h := range hosts {
		wg.Add(1)
		go func(h string) {
			defer wg.Done()
			err := ListRemote(ctx, h, cfg.HostKind(h), opts, func(r RemoteRepo) {
				mu.Lock()
				emit(r)
				mu.Unlock()
			})
			if err != nil {
				mu.Lock()
				errs = append(errs, fmt.Errorf("%s: %w", h, err))
				mu.Unlock()
			}
		}(h)
	}
	wg.Wait()
	if len(errs) > 0 {
		return errs[0]
	}
	return nil
}

// ListRemoteAll enumerates several hosts and returns a sorted list.
func ListRemoteAll(ctx context.Context, cfg *Config, hosts []string, opts RemoteOpts) ([]RemoteRepo, error) {
	var out []RemoteRepo
	err := ListRemoteStream(ctx, cfg, hosts, opts, func(r RemoteRepo) { out = append(out, r) })
	if err != nil {
		return nil, err
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Full() < out[j].Full() })
	return out, nil
}

func listGitHub(ctx context.Context, host string, opts RemoteOpts, emit func(RemoteRepo)) error {
	q := "user/repos?per_page=100&affiliation=owner,collaborator,organization_member&sort=full_name"
	type ghRepo struct {
		FullName      string `json:"full_name"`
		SSHURL        string `json:"ssh_url"`
		CloneURL      string `json:"clone_url"`
		HTMLURL       string `json:"html_url"`
		Archived      bool   `json:"archived"`
		DefaultBranch string `json:"default_branch"`
		Owner         struct {
			Type string `json:"type"`
		} `json:"owner"`
	}
	return streamTool(ctx, "gh", []string{"api", "--hostname", host, "--paginate", q}, func(r io.Reader) error {
		return decodePages(r, func(it ghRepo) {
			if it.Archived && !opts.Archived {
				return
			}
			emit(RemoteRepo{
				Repo:          Repo{Host: host, Path: it.FullName},
				SSHURL:        it.SSHURL,
				HTTPURL:       it.CloneURL,
				WebURL:        it.HTMLURL,
				Archived:      it.Archived,
				DefaultBranch: it.DefaultBranch,
				OwnerKind:     strings.ToLower(it.Owner.Type),
			})
		})
	})
}

type glRepo struct {
	ID                int    `json:"id"`
	PathWithNamespace string `json:"path_with_namespace"`
	SSHURL            string `json:"ssh_url_to_repo"`
	HTTPURL           string `json:"http_url_to_repo"`
	WebURL            string `json:"web_url"`
	Archived          bool   `json:"archived"`
	DefaultBranch     string `json:"default_branch"`
	Namespace         struct {
		Kind string `json:"kind"`
	} `json:"namespace"`
}

func glToRemote(host string, it glRepo) RemoteRepo {
	return RemoteRepo{
		Repo:          Repo{Host: host, Path: it.PathWithNamespace},
		SSHURL:        it.SSHURL,
		HTTPURL:       it.HTTPURL,
		WebURL:        it.WebURL,
		Archived:      it.Archived,
		DefaultBranch: it.DefaultBranch,
		OwnerKind:     it.Namespace.Kind,
	}
}

func listGitLab(ctx context.Context, host string, opts RemoteOpts, emit func(RemoteRepo)) error {
	q := "projects?per_page=100&order_by=id&sort=asc"
	if !opts.All {
		q += "&membership=true"
	}
	if !opts.Archived {
		q += "&archived=false"
	}
	seen := map[string]bool{}
	return streamTool(ctx, "glab", []string{"api", "--hostname", host, "--paginate", q}, func(r io.Reader) error {
		return decodePages(r, func(it glRepo) {
			if seen[it.PathWithNamespace] { // pagination can repeat rows
				return
			}
			seen[it.PathWithNamespace] = true
			if it.Archived && !opts.Archived {
				return
			}
			emit(glToRemote(host, it))
		})
	})
}

// decodePages decodes the concatenated JSON arrays that `gh api --paginate`
// and `glab api --paginate` print (one array per page), calling each as soon
// as a page has been read.
func decodePages[T any](r io.Reader, each func(T)) error {
	dec := json.NewDecoder(r)
	for {
		var page []T
		err := dec.Decode(&page)
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return err
		}
		for _, it := range page {
			each(it)
		}
	}
}

// streamTool runs gh/glab and hands its stdout to read while it is still
// running; stderr is folded into the error.
func streamTool(ctx context.Context, name string, args []string, read func(io.Reader) error) error {
	if _, err := exec.LookPath(name); err != nil {
		return fmt.Errorf("%s not found in PATH", name)
	}
	cmd := exec.CommandContext(ctx, name, args...)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return err
	}
	if err := cmd.Start(); err != nil {
		return err
	}
	readErr := read(stdout)
	io.Copy(io.Discard, stdout) // drain so Wait does not block
	if err := cmd.Wait(); err != nil {
		msg := strings.TrimSpace(stderr.String())
		if msg == "" {
			msg = err.Error()
		}
		return fmt.Errorf("%s %s: %s", name, strings.Join(args, " "), msg)
	}
	if readErr != nil {
		return fmt.Errorf("decode %s output: %w", name, readErr)
	}
	return nil
}
