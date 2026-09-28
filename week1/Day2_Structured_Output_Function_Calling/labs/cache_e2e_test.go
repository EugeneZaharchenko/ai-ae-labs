package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/dimetron/ai-eng-course/labs/week1/internal/ratecache"
)

// TestE2ECacheAcrossProvidersAndCalls proves the production shape end-to-end:
// one process, one shared cached client, NBU + Mono both pointed at
// httptest servers. Three sequential Mono calls must cost ONE upstream hit;
// interleaved NBU fetches must be cached independently per URL.
func TestE2ECacheAcrossProvidersAndCalls(t *testing.T) {
	var monoHits, nbuHits atomic.Int64
	monoSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		monoHits.Add(1)
		w.Write([]byte(`[{"currencyCodeA":840,"currencyCodeB":980,"date":1789455006,"rateBuy":44.43,"rateSell":44.831}]`))
	}))
	defer monoSrv.Close()
	nbuSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		nbuHits.Add(1)
		w.Write([]byte(`[{"txt":"Долар США","rate":44.639,"cc":"USD","exchangedate":"15.09.2026"}]`))
	}))
	defer nbuSrv.Close()

	// The production shape: providers default to defaultRateHTTPClient(),
	// which embeds its own CachedTransport. The cache is per transport, so a
	// process-wide shared cache is the shared-client arrangement a
	// long-lived process wires up itself (compare.go builds both providers
	// in one process; each defaults to its own transport, so cross-provider
	// sharing needs one explicit client — shown here).
	tr := ratecache.NewCachedTransport()
	shared := &http.Client{Timeout: 10 * time.Second, Transport: tr}
	mono := &MonoProvider{BaseURL: monoSrv.URL, HTTPClient: shared}
	nbu := &NBUProvider{BaseURL: nbuSrv.URL, HTTPClient: shared}

	ctx := context.Background()
	for i := 0; i < 3; i++ {
		if _, _, err := mono.RatesToUAH(ctx); err != nil {
			t.Fatal(err)
		}
	}
	if got := monoHits.Load(); got != 1 {
		t.Fatalf("monobank hits = %d, want 1 (3 calls within the 1-min TTL)", got)
	}

	for i := 0; i < 2; i++ {
		if _, _, err := nbu.RatesToUAH(ctx); err != nil {
			t.Fatal(err)
		}
		if _, err := mono.RatesToUAHDetailed(ctx); err != nil {
			t.Fatal(err)
		}
	}
	if got := monoHits.Load(); got != 1 {
		t.Fatalf("monobank hits = %d, want 1", got)
	}
	if got := nbuHits.Load(); got != 1 {
		t.Fatalf("nbu hits = %d, want 1 (second round is a cache hit)", got)
	}
}

// TestE2EConcurrentFetchSingleflight: 6 goroutines, one provider, one URL —
// exactly 1 upstream hit while every caller gets valid typed data.
func TestE2EConcurrentFetchSingleflight(t *testing.T) {
	var hits atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hits.Add(1)
		time.Sleep(80 * time.Millisecond) // widen the race window on purpose
		w.Write([]byte(`[{"currencyCodeA":840,"currencyCodeB":980,"date":1789455006,"rateBuy":44.43,"rateSell":44.831}]`))
	}))
	defer srv.Close()

	tr := ratecache.NewCachedTransport()
	shared := &http.Client{Timeout: 10 * time.Second, Transport: tr}
	mono := &MonoProvider{BaseURL: srv.URL, HTTPClient: shared}

	var wg sync.WaitGroup
	errs := make([]error, 6)
	for i := range errs {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			_, _, err := mono.RatesToUAH(context.Background())
			errs[i] = err
		}(i)
	}
	wg.Wait()
	for _, err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	if got := hits.Load(); got != 1 {
		t.Fatalf("hits = %d, want 1 under concurrency (singleflight)", got)
	}
}
