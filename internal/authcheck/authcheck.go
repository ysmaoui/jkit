// Package authcheck checks the credentials of every configured host.
package authcheck

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/ysmaoui/jkit/internal/api"
	"github.com/ysmaoui/jkit/internal/config"
)

const (
	ResultValid       = "valid"
	ResultInvalid     = "invalid"
	ResultForbidden   = "forbidden"
	ResultUnreachable = "unreachable"
	ResultTimeout     = "timeout"
	// ResultAnonymous is a 2xx answer that does not prove the credentials
	// authenticated anyone: Jenkins treated the request as anonymous, or the
	// body is not whoAmI JSON (e.g. a proxy or login page).
	ResultAnonymous = "anonymous"
)

const (
	DefaultTimeout     = 10 * time.Second
	DefaultConcurrency = 8
)

// Entry is the check result for one config key. Result is one of the Result
// constants or "http_<code>" for any other status. Problems holds KeyProblems
// as text, plus notes that do not fail the entry.
type Entry struct {
	Host        string              `json:"host"`
	Alias       string              `json:"alias"`
	User        string              `json:"user"`
	Default     bool                `json:"default"`
	Result      string              `json:"result"`
	StatusCode  int                 `json:"status_code"`
	Problems    []string            `json:"problems"`
	KeyProblems []config.KeyProblem `json:"-"`
}

// Failed reports a defect that a retry cannot fix: rejected or unproven
// credentials, or a malformed, duplicate or ambiguous key.
func (e Entry) Failed() bool {
	switch e.Result {
	case ResultInvalid, ResultForbidden, ResultAnonymous:
		return true
	}
	return len(e.KeyProblems) > 0
}

// Unverified reports an entry that is not Failed but whose credentials could
// not be confirmed, e.g. the host was down or answered 5xx.
func (e Entry) Unverified() bool {
	return !e.Failed() && e.Result != ResultValid
}

type Options struct {
	Timeout     time.Duration
	Concurrency int
}

// Check asks host who the credentials belong to, once, within timeout. name is
// the identity Jenkins reports when result is ResultValid.
func Check(ctx context.Context, host, user, token string, timeout time.Duration) (result string, statusCode int, name string) {
	client := api.NewClient(host, user, token, api.WithTimeout(timeout))
	code, who, err := client.WhoAmIStatus(ctx)
	if err != nil {
		var ne net.Error
		if errors.Is(err, context.DeadlineExceeded) || errors.As(err, &ne) && ne.Timeout() {
			return ResultTimeout, 0, ""
		}
		return ResultUnreachable, 0, ""
	}
	switch {
	case code >= 200 && code < 300:
		if who == nil || who.Anonymous || who.Name == "" {
			return ResultAnonymous, code, ""
		}
		return ResultValid, code, who.Name
	case code == http.StatusUnauthorized:
		return ResultInvalid, code, ""
	case code == http.StatusForbidden:
		return ResultForbidden, code, ""
	}
	return fmt.Sprintf("http_%d", code), code, ""
}

// CheckAll checks every config key concurrently and returns one entry per
// key, sorted by key.
func CheckAll(ctx context.Context, cfg *config.Config, opts Options) []Entry {
	if opts.Timeout <= 0 {
		opts.Timeout = DefaultTimeout
	}
	if opts.Concurrency <= 0 {
		opts.Concurrency = DefaultConcurrency
	}
	problems := cfg.KeyProblems()
	hosts := cfg.HostURLs()
	entries := make([]Entry, len(hosts))
	sem := make(chan struct{}, opts.Concurrency)
	var wg sync.WaitGroup
	for i, host := range hosts {
		hc := cfg.Hosts[host]
		e := Entry{
			Host:        host,
			Alias:       hc.Alias,
			User:        hc.User,
			Default:     hc.Default,
			Problems:    []string{},
			KeyProblems: problems[host],
		}
		for _, p := range e.KeyProblems {
			e.Problems = append(e.Problems, p.String())
		}
		entries[i] = e
		wg.Add(1)
		go func(i int, hc *config.HostConfig) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			// A fragment or query left in the key would swallow the request path,
			// so the credentials are checked against the normalised host.
			result, code, name := Check(ctx, config.NormalizeHost(entries[i].Host), hc.User, hc.Token, opts.Timeout)
			entries[i].Result, entries[i].StatusCode = result, code
			if result == ResultValid && !strings.EqualFold(name, hc.User) {
				entries[i].Problems = append(entries[i].Problems, "authenticated as "+name)
			}
		}(i, hc)
	}
	wg.Wait()
	return entries
}
