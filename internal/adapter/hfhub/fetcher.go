// SPDX-License-Identifier: Apache-2.0

package hfhub

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net"
	"net/http"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/thnhdatle/triage/internal/domain/artifact"
)

// DefaultBaseURL is the Hugging Face hub.
const DefaultBaseURL = "https://huggingface.co"

// maxRedirects bounds the redirect chain (resolve -> CDN is one or two hops).
const maxRedirects = 5

// headerTimeout bounds the wait for response headers; the body is bounded by
// the context only because checkpoints are large.
const headerTimeout = 30 * time.Second

// forbiddenFile is never downloaded (2.22 GB, not needed).
const forbiddenFile = "optimizer.pt"

var (
	// ErrDownload is returned for a failed transfer (bad status, redirect to a
	// host that is not allow-listed, short or oversized body).
	ErrDownload = errors.New("hfhub: download failed")
	// ErrInvalid is returned for an invalid entry, file or option.
	ErrInvalid = errors.New("hfhub: invalid argument")

	revisionRE = regexp.MustCompile(`^[0-9a-f]{40}$`)
	repoRE     = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]*/[A-Za-z0-9][A-Za-z0-9._-]*$`)
)

// defaultHosts are the hosts a download may touch: the hub and its CDNs
// (cdn-lfs*.hf.co, cas-bridge.xethub.hf.co). A leading dot matches subdomains.
func defaultHosts() []string { return []string{"huggingface.co", ".hf.co"} }

// Fetcher implements artifact.Fetcher.
type Fetcher struct {
	base   *url.URL
	hosts  []string
	client *http.Client
}

var _ artifact.Fetcher = (*Fetcher)(nil)

// Option configures a Fetcher.
type Option func(*config)

type config struct {
	base   string
	hosts  []string
	client *http.Client
}

// WithBaseURL overrides the hub URL (tests). Its host must also be allow-listed
// with WithAllowedHosts.
func WithBaseURL(u string) Option { return func(c *config) { c.base = u } }

// WithAllowedHosts replaces the host allow-list. An entry starting with "." matches
// every subdomain; other entries match exactly. Ports are ignored.
func WithAllowedHosts(hosts ...string) Option { return func(c *config) { c.hosts = hosts } }

// WithHTTPClient sets the transport (default: a client with a header timeout).
// Its redirect policy is replaced by the allow-list check.
func WithHTTPClient(h *http.Client) Option { return func(c *config) { c.client = h } }

// New returns a Fetcher.
func New(opts ...Option) (*Fetcher, error) {
	c := config{base: DefaultBaseURL, hosts: defaultHosts()}
	for _, o := range opts {
		if o == nil {
			return nil, fmt.Errorf("%w: nil option", ErrInvalid)
		}
		o(&c)
	}
	base, err := url.Parse(c.base)
	if err != nil || base.Host == "" {
		return nil, fmt.Errorf("%w: base url %q", ErrInvalid, c.base)
	}
	f := &Fetcher{base: base, hosts: c.hosts}
	if err := f.checkURL(base); err != nil {
		return nil, fmt.Errorf("%w: base url: %w", ErrInvalid, err)
	}
	var client http.Client
	if c.client != nil {
		client = *c.client
	} else {
		tr := http.DefaultTransport.(*http.Transport).Clone() //nolint:forcetypeassert // stdlib guarantee
		tr.ResponseHeaderTimeout = headerTimeout
		client = http.Client{Transport: tr}
	}
	client.CheckRedirect = func(req *http.Request, via []*http.Request) error {
		if len(via) >= maxRedirects {
			return fmt.Errorf("%w: more than %d redirects", ErrDownload, maxRedirects)
		}
		if err := f.checkURL(req.URL); err != nil {
			return fmt.Errorf("%w: redirect: %w", ErrDownload, err)
		}
		return nil
	}
	f.client = &client
	return f, nil
}

// checkURL enforces https (http only for loopback, for tests) and the host
// allow-list.
func (f *Fetcher) checkURL(u *url.URL) error {
	host := strings.ToLower(u.Hostname())
	switch {
	case u.Scheme == "https":
	case u.Scheme == "http" && isLoopback(host):
	default:
		return fmt.Errorf("scheme %q not allowed for host %q", u.Scheme, host)
	}
	for _, h := range f.hosts {
		h = strings.ToLower(h)
		if h == host || (strings.HasPrefix(h, ".") && strings.HasSuffix(host, h)) {
			return nil
		}
	}
	return fmt.Errorf("host %q is not allow-listed", host)
}

func isLoopback(host string) bool {
	if host == "localhost" {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

// validate guards the request before any network access.
func validate(e artifact.Entry, f artifact.File) error {
	switch {
	case !repoRE.MatchString(e.HFRepo):
		return fmt.Errorf("%w: repo %q", ErrInvalid, e.HFRepo)
	case !revisionRE.MatchString(e.Revision):
		return fmt.Errorf("%w: revision %q is not a pinned 40-hex commit", ErrInvalid, e.Revision)
	case f.SHA256 == "" || f.Size <= 0:
		return fmt.Errorf("%w: %s/%s has no sha256 and size to verify against", ErrInvalid, e.Name, f.Path)
	}
	clean := path.Clean(f.Path)
	if clean != f.Path || !fs.ValidPath(clean) || path.Base(clean) == forbiddenFile || !listed(e, f.Path) {
		return fmt.Errorf("%w: %s/%s", artifact.ErrNotAllowed, e.Name, f.Path)
	}
	return nil
}

func listed(e artifact.Entry, p string) bool {
	for _, g := range e.Files {
		if g.Path == p {
			return true
		}
	}
	return false
}

func (f *Fetcher) fileURL(e artifact.Entry, file artifact.File) string {
	segs := strings.Split(file.Path, "/")
	for i, s := range segs {
		segs[i] = url.PathEscape(s)
	}
	u := *f.base
	u.Path = strings.TrimRight(u.Path, "/") + "/" + e.HFRepo + "/resolve/" + e.Revision + "/" + file.Path
	u.RawPath = strings.TrimRight(f.base.EscapedPath(), "/") + "/" + e.HFRepo + "/resolve/" + e.Revision + "/" +
		strings.Join(segs, "/")
	return u.String()
}

// Fetch implements artifact.Fetcher. The file is written to dir/<file.Path>
// only after its size and SHA-256 match; otherwise nothing is left behind.
func (f *Fetcher) Fetch(ctx context.Context, e artifact.Entry, file artifact.File, dir string) error {
	if err := validate(e, file); err != nil {
		return err
	}
	target := filepath.Join(dir, filepath.FromSlash(file.Path))
	if err := os.MkdirAll(filepath.Dir(target), 0o750); err != nil {
		return fmt.Errorf("create %s: %w", filepath.Dir(target), err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, f.fileURL(e, file), http.NoBody)
	if err != nil {
		return fmt.Errorf("%w: %w", ErrInvalid, err)
	}
	resp, err := f.client.Do(req)
	if err != nil {
		return fmt.Errorf("get %s/%s: %w", e.HFRepo, file.Path, err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("%w: get %s/%s: status %s", ErrDownload, e.HFRepo, file.Path, resp.Status)
	}
	return writeVerified(resp.Body, target, file)
}

// writeVerified streams r into a temp file next to target, verifies it and
// renames it into place.
func writeVerified(r io.Reader, target string, file artifact.File) (err error) {
	tmp, err := os.CreateTemp(filepath.Dir(target), filepath.Base(target)+".*.part")
	if err != nil {
		return fmt.Errorf("create temp file for %s: %w", target, err)
	}
	defer func() {
		if err != nil {
			_ = tmp.Close()
			_ = os.Remove(tmp.Name())
		}
	}()
	h := sha256.New()
	// Read one byte more than expected to detect an oversized body.
	n, err := io.Copy(io.MultiWriter(tmp, h), io.LimitReader(r, file.Size+1))
	if err != nil {
		return fmt.Errorf("download %s: %w", file.Path, err)
	}
	if n != file.Size {
		return fmt.Errorf("%w: %s size %d, want %d", artifact.ErrChecksum, file.Path, n, file.Size)
	}
	if got := hex.EncodeToString(h.Sum(nil)); !strings.EqualFold(got, file.SHA256) {
		return fmt.Errorf("%w: %s sha256 %s, want %s", artifact.ErrChecksum, file.Path, got, file.SHA256)
	}
	serr := tmp.Sync()
	cerr := tmp.Close()
	if err = errors.Join(serr, cerr); err != nil {
		return fmt.Errorf("flush %s: %w", tmp.Name(), err)
	}
	if err = os.Rename(tmp.Name(), target); err != nil {
		return fmt.Errorf("replace %s: %w", target, err)
	}
	return nil
}
