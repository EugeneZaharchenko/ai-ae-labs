// Package ratecache is a cached, rate-limited transport for outbound
// rate-provider HTTP calls.
//
// Every GET through this RoundTripper is served from cache while fresh (TTL,
// 1 minute by default) and limited to one real upstream hit per MinInterval
// (1 minute by default). The two knobs are deliberately separate:
//
//   - TTL answers "how stale may a cached answer be?" — the 1-minute cache
//     the lab asks for.
//   - MinInterval answers "how soon may we hit the upstream again after a
//     cache miss?" — monobank's published contract is one call per 5 minutes
//     per /bank/currency, so a limiter is the correct complement: without it,
//     every cache expiry becomes an immediate retry.
//
// While one caller fetches a URL, other concurrent callers for the same URL
// wait and then read the fresh entry (singleflight), instead of racing the
// upstream — a concurrent compare run still produces one upstream call.
//
// Why a RoundTripper and not a cache inside the providers: both providers
// already take an *http.Client, so the cache composes exactly like a timeout
// or a tracing transport would — the agent, the compare tool and tests get it
// without touching the Provider interface. stdlib only.
//
// This lives outside the lab package because it is infrastructure the exercise
// never asks anyone to edit: the student reads rates.go, not the cache.
package ratecache

import (
	"bytes"
	"io"
	"net/http"
	"sync"
	"time"
)

// cacheEntry is one buffered GET response.
type cacheEntry struct {
	status  int
	header  http.Header
	body    []byte
	fetched time.Time
}

// CachedTransport wraps an inner RoundTripper: same-URL GETs are served from
// cache while fresh; a stale entry triggers at most one upstream hit per
// MinInterval.
type CachedTransport struct {
	// Inner is the transport to wrap. Default: http.DefaultTransport.
	Inner http.RoundTripper
	// TTL is how long a cached response stays fresh. Default: 1 minute.
	TTL time.Duration
	// MinInterval is the minimum gap between two real upstream hits on the
	// same URL. Default: 1 minute.
	MinInterval time.Duration
	// Now is overridable in tests. Default: time.Now.
	Now func() time.Time

	mu       sync.Mutex
	entries  map[string]*cacheEntry
	lastHit  map[string]time.Time
	inflight map[string]*sync.WaitGroup
}

// NewCachedTransport returns a transport with a 1-minute cache TTL and a
// 1-minute minimum interval between upstream hits, wrapping the default
// transport.
func NewCachedTransport() *CachedTransport {
	return &CachedTransport{TTL: time.Minute, MinInterval: time.Minute}
}

func (c *CachedTransport) inner() http.RoundTripper {
	if c.Inner != nil {
		return c.Inner
	}
	return http.DefaultTransport
}

func (c *CachedTransport) now() time.Time {
	if c.Now != nil {
		return c.Now()
	}
	return time.Now()
}

func (c *CachedTransport) ttl() time.Duration {
	if c.TTL > 0 {
		return c.TTL
	}
	return time.Minute
}

func (c *CachedTransport) minInterval() time.Duration {
	if c.MinInterval > 0 {
		return c.MinInterval
	}
	return time.Minute
}

func cloneHeader(h http.Header) http.Header {
	out := make(http.Header, len(h))
	for k, v := range h {
		out[k] = append([]string(nil), v...)
	}
	return out
}

func cachedResponse(req *http.Request, e *cacheEntry) *http.Response {
	return &http.Response{
		StatusCode:    e.status,
		Status:        http.StatusText(e.status),
		Header:        cloneHeader(e.header),
		Body:          io.NopCloser(bytes.NewReader(e.body)),
		ContentLength: int64(len(e.body)),
		Request:       req,
	}
}

// RoundTrip implements http.RoundTripper. Only GET is cached: the rate
// providers are read-only, and caching a non-GET for a minute would be a
// correctness bug, not an optimization.
func (c *CachedTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	if req.Method != http.MethodGet {
		return c.inner().RoundTrip(req)
	}
	key := req.URL.String()

	c.mu.Lock()
	// Self-initialize so a hand-built &CachedTransport{...} (tests) works
	// without a constructor call.
	if c.entries == nil {
		c.entries = map[string]*cacheEntry{}
		c.lastHit = map[string]time.Time{}
		c.inflight = map[string]*sync.WaitGroup{}
	}
	c.mu.Unlock()

	for {
		c.mu.Lock()
		if e, ok := c.entries[key]; ok && c.now().Sub(e.fetched) < c.ttl() {
			c.mu.Unlock()
			return cachedResponse(req, e), nil
		}
		// Too soon after the last real hit? Serve stale if we have it;
		// otherwise wait for the in-flight fetch and re-check.
		if lh, ok := c.lastHit[key]; ok {
			if wait := c.minInterval() - c.now().Sub(lh); wait > 0 {
				if stale, ok := c.entries[key]; ok {
					c.mu.Unlock()
					return cachedResponse(req, stale), nil
				}
				wg := c.inflight[key]
				if wg == nil {
					wg = &sync.WaitGroup{}
					wg.Add(1)
					c.inflight[key] = wg
				}
				c.mu.Unlock()
				wg.Wait()
				continue
			}
		}
		if wg, busy := c.inflight[key]; busy {
			c.mu.Unlock()
			wg.Wait()
			continue
		}
		// We are the caller that will hit the upstream.
		wg := &sync.WaitGroup{}
		wg.Add(1)
		c.inflight[key] = wg
		c.mu.Unlock()

		resp, err := c.inner().RoundTrip(req)
		c.mu.Lock()
		delete(c.inflight, key)
		wg.Done()
		var out *http.Response
		if err == nil && resp.StatusCode == http.StatusOK {
			body, rerr := io.ReadAll(resp.Body)
			resp.Body.Close()
			if rerr == nil {
				c.entries[key] = &cacheEntry{
					status:  resp.StatusCode,
					header:  cloneHeader(resp.Header),
					body:    body,
					fetched: c.now(),
				}
				c.lastHit[key] = c.now()
				// The caller needs a readable body, but we just drained
				// resp's for the cache — hand back a fresh copy instead.
				out = cachedResponse(req, c.entries[key])
			}
		}
		c.mu.Unlock()
		if out != nil {
			return out, nil
		}
		return resp, err
	}
}
