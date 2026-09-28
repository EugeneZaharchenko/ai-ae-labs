package ratecache

import (
	"io"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// countingTransport counts real upstream hits and always answers 200/body.
type countingTransport struct {
	hits atomic.Int64
	body string
}

func (t *countingTransport) RoundTrip(_ *http.Request) (*http.Response, error) {
	t.hits.Add(1)
	return &http.Response{
		StatusCode:    http.StatusOK,
		Status:        "200 OK",
		Header:        http.Header{"Content-Type": []string{"application/json"}},
		Body:          io.NopCloser(strings.NewReader(t.body)),
		ContentLength: int64(len(t.body)),
	}, nil
}

func TestCachedTransportServesSameURLFromCache(t *testing.T) {
	upstream := &countingTransport{body: `{"ok":true}`}
	ttl := time.Minute
	base := time.Unix(1_800_000_000, 0)
	now := base
	tr := &CachedTransport{
		Inner:       upstream,
		TTL:         ttl,
		MinInterval: ttl,
		Now:         func() time.Time { return now },
	}
	client := &http.Client{Transport: tr}
	url := "http://example.test/rates"

	for i := 0; i < 5; i++ {
		resp, err := client.Get(url)
		if err != nil {
			t.Fatalf("GET %d: %v", i, err)
		}
		b, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		if string(b) != `{"ok":true}` {
			t.Fatalf("GET %d: body = %q", i, b)
		}
	}
	if got := upstream.hits.Load(); got != 1 {
		t.Fatalf("upstream hits = %d, want 1 (4 repeat GETs must be cache hits)", got)
	}

	// Advance past the TTL: the next GET may hit upstream again.
	now = base.Add(2 * ttl)
	if _, err := client.Get(url); err != nil {
		t.Fatal(err)
	}
	if got := upstream.hits.Load(); got != 2 {
		t.Fatalf("after TTL: hits = %d, want 2", got)
	}
}

func TestCachedTransportSeparatesURLs(t *testing.T) {
	upstream := &countingTransport{body: `x`}
	base := time.Unix(1_800_000_000, 0)
	tr := &CachedTransport{Inner: upstream, TTL: time.Minute, MinInterval: time.Minute,
		Now: func() time.Time { return base }}
	client := &http.Client{Transport: tr}

	for _, u := range []string{"http://a.test/x", "http://b.test/x", "http://a.test/y"} {
		if _, err := client.Get(u); err != nil {
			t.Fatal(err)
		}
	}
	if got := upstream.hits.Load(); got != 3 {
		t.Fatalf("hits = %d, want 3 (different URLs are not shared cache keys)", got)
	}
}

func TestCachedTransportRateLimitsEvenWhenStale(t *testing.T) {
	// The limiter case: cache expired but MinInterval has not. With a stale
	// copy present, we serve stale instead of hammering the upstream.
	upstream := &countingTransport{body: `v1`}
	ttl := time.Minute
	base := time.Unix(1_800_000_000, 0)
	now := base
	tr := &CachedTransport{Inner: upstream, TTL: ttl, MinInterval: ttl,
		Now: func() time.Time { return now }}
	client := &http.Client{Transport: tr}
	url := "http://example.test/rates"

	if _, err := client.Get(url); err != nil {
		t.Fatal(err)
	}
	// 2×TTL later: cache stale, but only 2×TTL since lastHit; MinInterval is
	// 1×TTL, so... 2×TTL > 1×MinInterval — the limiter allows the fetch.
	// To exercise the limiter we need now < lastHit + MinInterval while the
	// entry is already stale. With MinInterval == TTL that window is empty,
	// so this case uses MinInterval = 3×TTL, like a 1-min cache against a
	// 3-min upstream contract.
	upstream2 := &countingTransport{body: `v1`}
	tr2 := &CachedTransport{Inner: upstream2, TTL: ttl, MinInterval: 3 * ttl,
		Now: func() time.Time { return now }}
	client2 := &http.Client{Transport: tr2}

	if _, err := client2.Get(url); err != nil {
		t.Fatal(err)
	}
	// TTL expired (we move 2×TTL) but MinInterval (3×TTL) not yet: the stale
	// copy is served, no upstream hit.
	now = base.Add(2 * ttl)
	resp, err := client2.Get(url)
	if err != nil {
		t.Fatal(err)
	}
	b, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if string(b) != `v1` {
		t.Fatalf("stale body = %q", b)
	}
	if got := upstream2.hits.Load(); got != 1 {
		t.Fatalf("hits = %d, want 1 (limiter must suppress the refetch)", got)
	}
	// Past MinInterval: refetch allowed.
	now = base.Add(4 * ttl)
	if _, err := client2.Get(url); err != nil {
		t.Fatal(err)
	}
	if got := upstream2.hits.Load(); got != 2 {
		t.Fatalf("hits = %d, want 2 after MinInterval passed", got)
	}
}

func TestCachedTransportSingleflight(t *testing.T) {
	// 8 concurrent GETs to the same URL -> exactly 1 upstream hit, all
	// callers get the same body.
	upstream := &countingTransport{body: `{"rates":1}`}
	tr := &CachedTransport{Inner: upstream, TTL: time.Minute, MinInterval: time.Minute,
		Now: time.Now}
	client := &http.Client{Transport: tr}
	url := "http://example.test/rates"

	var wg sync.WaitGroup
	errs := make([]error, 8)
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			resp, err := client.Get(url)
			if err != nil {
				errs[i] = err
				return
			}
			resp.Body.Close()
		}(i)
	}
	wg.Wait()
	for _, err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	if got := upstream.hits.Load(); got != 1 {
		t.Fatalf("hits = %d, want 1 (concurrent callers must singleflight)", got)
	}
}

func TestCachedTransportDoesNotCacheNonGET(t *testing.T) {
	upstream := &countingTransport{body: `post`}
	tr := &CachedTransport{Inner: upstream, TTL: time.Minute, MinInterval: time.Minute,
		Now: time.Now}
	client := &http.Client{Transport: tr}

	for i := 0; i < 3; i++ {
		if _, err := client.Post("http://example.test/x", "text/plain", strings.NewReader("hi")); err != nil {
			t.Fatal(err)
		}
	}
	if got := upstream.hits.Load(); got != 3 {
		t.Fatalf("hits = %d, want 3 (POST must never be cached)", got)
	}
}

func TestCachedTransportDoesNotCacheErrors(t *testing.T) {
	var fail atomic.Bool
	fail.Store(true) // start failing: the first GET must get a 500
	inner := roundTripFunc(func(_ *http.Request) (*http.Response, error) {
		if fail.Load() {
			return &http.Response{
				StatusCode: http.StatusInternalServerError,
				Status:     "500 Internal Server Error",
				Header:     http.Header{},
				Body:       io.NopCloser(strings.NewReader("boom")),
			}, nil
		}
		return &http.Response{
			StatusCode: http.StatusOK,
			Status:     "200 OK",
			Header:     http.Header{},
			Body:       io.NopCloser(strings.NewReader("ok")),
		}, nil
	})
	tr := &CachedTransport{Inner: inner, TTL: time.Minute, MinInterval: time.Minute,
		Now: time.Now}
	client := &http.Client{Transport: tr}
	url := "http://example.test/rates"

	// 500 must not be cached: the next call tries upstream again.
	resp, err := client.Get(url)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", resp.StatusCode)
	}
	fail.Store(false)
	resp, err = client.Get(url)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("second GET after a 500: status = %d, want 200 (errors must not poison the cache)", resp.StatusCode)
	}
}

// roundTripFunc adapts a function to http.RoundTripper.
type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }
