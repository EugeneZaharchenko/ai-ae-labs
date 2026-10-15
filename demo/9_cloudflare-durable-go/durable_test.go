package cloudflaredurable

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"slices"
	"testing"

	"github.com/cloudflare/cloudflare-go/v7"
	"github.com/cloudflare/cloudflare-go/v7/option"
)

const (
	account   = "acc-1"
	namespace = "ns-agents"
)

// fakeAPI serves two pages of the Durable Objects list endpoint in the real
// Cloudflare v4 envelope, so the test runs offline with no account.
func fakeAPI(t *testing.T) *httptest.Server {
	t.Helper()
	want := fmt.Sprintf("/accounts/%s/workers/durable_objects/namespaces/%s/objects", account, namespace)
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != want {
			t.Errorf("path = %s, want %s", r.URL.Path, want)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer test-token" {
			t.Errorf("Authorization = %q", got)
		}
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Query().Get("cursor") {
		case "":
			fmt.Fprint(w, `{"success":true,"errors":[],"messages":[],
				"result":[{"id":"session-a","hasStoredData":true},{"id":"session-b","hasStoredData":false}],
				"result_info":{"count":2,"cursors":{"after":"page-2"}}}`)
		case "page-2":
			fmt.Fprint(w, `{"success":true,"errors":[],"messages":[],
				"result":[{"id":"session-c","hasStoredData":true}],
				"result_info":{"count":1,"cursors":{}}}`)
		default:
			t.Errorf("unexpected cursor %q", r.URL.Query().Get("cursor"))
		}
	}))
}

func TestObjectsWithStateFollowsCursorAndFilters(t *testing.T) {
	srv := fakeAPI(t)
	defer srv.Close()
	c := cloudflare.NewClient(option.WithBaseURL(srv.URL), option.WithAPIToken("test-token"), option.WithMaxRetries(0))

	got, err := ObjectsWithState(context.Background(), c, account, namespace)
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"session-a", "session-c"}; !slices.Equal(got, want) {
		t.Fatalf("got %v, want %v", got, want)
	}
}

func TestObjectsWithStateReportsAPIError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusForbidden)
		fmt.Fprint(w, `{"success":false,"errors":[{"code":10000,"message":"Authentication error"}],"messages":[],"result":null}`)
	}))
	defer srv.Close()
	c := cloudflare.NewClient(option.WithBaseURL(srv.URL), option.WithAPIToken("bad"), option.WithMaxRetries(0))

	if _, err := ObjectsWithState(context.Background(), c, account, namespace); err == nil {
		t.Fatal("want an error for HTTP 403, got nil")
	}
}

// TestLive runs against the real API only when credentials are set:
//
//	CLOUDFLARE_API_TOKEN=… CLOUDFLARE_ACCOUNT_ID=… CF_DO_NAMESPACE_ID=… go test -run TestLive -v
func TestLive(t *testing.T) {
	token, acc, ns := os.Getenv("CLOUDFLARE_API_TOKEN"), os.Getenv("CLOUDFLARE_ACCOUNT_ID"), os.Getenv("CF_DO_NAMESPACE_ID")
	if token == "" || acc == "" || ns == "" {
		t.Skip("set CLOUDFLARE_API_TOKEN, CLOUDFLARE_ACCOUNT_ID and CF_DO_NAMESPACE_ID to run against Cloudflare")
	}
	ids, err := ObjectsWithState(context.Background(), cloudflare.NewClient(option.WithAPIToken(token)), acc, ns)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("%d durable objects with stored data: %v", len(ids), ids)
}
