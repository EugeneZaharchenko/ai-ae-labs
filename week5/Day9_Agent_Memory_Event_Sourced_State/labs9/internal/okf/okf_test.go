package okf

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func newStore(t *testing.T) *Store {
	t.Helper()
	s, err := NewStore(filepath.Join(t.TempDir(), "bundle"))
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func TestPageRoundTrip(t *testing.T) {
	s := newStore(t)
	in := Page{Type: "integration", Title: "Мерчант: A-114", Description: "d", Resource: "raw/a.json",
		Tags: []string{"merchant", "memory"}, GeneratedAt: "2026-10-13T10:00:00Z", Body: "# A-114\n\n- legacy\n"}
	if err := s.WritePage("integrations/a-114.md", in); err != nil {
		t.Fatal(err)
	}
	got, err := s.ReadPage("integrations/a-114.md")
	if err != nil {
		t.Fatal(err)
	}
	if got.Type != in.Type || got.Title != in.Title || got.GeneratedAt != in.GeneratedAt ||
		strings.Join(got.Tags, ",") != "merchant,memory" || !strings.Contains(got.Body, "legacy") {
		t.Fatalf("round trip lost data: %+v", got)
	}
}

func TestTrustBoundary(t *testing.T) {
	s := newStore(t)
	if err := s.WritePage("x.md", Page{}); !errors.Is(err, ErrMissingType) {
		t.Errorf("no type: %v", err)
	}
	for _, name := range []string{"index.md", "sub/log.md"} {
		if err := s.WritePage(name, Page{Type: "t"}); !errors.Is(err, ErrReservedName) {
			t.Errorf("%s: %v", name, err)
		}
	}
	for _, name := range []string{"../raw/a.md", "../../etc/passwd", "/abs.md"} {
		if err := s.WritePage(name, Page{Type: "t"}); !errors.Is(err, ErrReadOnlySurface) {
			t.Errorf("WritePage(%q) = %v, want ErrReadOnlySurface", name, err)
		}
	}
	for _, name := range []string{"../wiki/a.md", "../../x"} {
		if err := s.WriteRaw(name, nil); !errors.Is(err, ErrReadOnlySurface) {
			t.Errorf("WriteRaw(%q) = %v, want ErrReadOnlySurface", name, err)
		}
	}
	if err := s.WriteRaw("sub/a.json", []byte("{}")); err != nil {
		t.Fatal(err)
	}
	if b, err := s.ReadRaw("sub/a.json"); err != nil || string(b) != "{}" {
		t.Fatalf("ReadRaw = %q, %v", b, err)
	}
	if _, err := s.ReadRaw("../wiki/index.md"); err == nil {
		t.Error("ReadRaw must not escape raw/")
	}
	if _, err := s.ReadPage("../raw/sub/a.json"); err == nil {
		t.Error("ReadPage must not escape wiki/")
	}
}

func TestIndexIsDeterministicAndLogAppendOnly(t *testing.T) {
	s := newStore(t)
	for _, n := range []string{"b.md", "a/z.md", "a/y.md"} {
		if err := s.WritePage(n, Page{Type: "concept", Title: strings.ToUpper(n)}); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.RegenIndex(); err != nil {
		t.Fatal(err)
	}
	first, _ := os.ReadFile(filepath.Join(s.wikiDir(), IndexFile))
	if err := s.RegenIndex(); err != nil {
		t.Fatal(err)
	}
	second, _ := os.ReadFile(filepath.Join(s.wikiDir(), IndexFile))
	if string(first) != string(second) || strings.Index(string(first), "A/Y.MD") > strings.Index(string(first), "A/Z.MD") {
		t.Fatalf("index is not deterministic/sorted:\n%s", first)
	}
	if names, _ := s.PageNames(); len(names) != 3 {
		t.Errorf("PageNames must exclude index.md and log.md: %v", names)
	}

	if log, err := s.ReadLog(); err != nil || log != "" {
		t.Fatalf("empty log = %q, %v", log, err)
	}
	at := time.Date(2026, 10, 13, 9, 0, 0, 0, time.UTC)
	_ = s.AppendLog(at, "ingest", "a.md", "one")
	_ = s.AppendLog(at, "lint", "b.md", "two")
	log, _ := s.ReadLog()
	if strings.Count(log, "\n") != 2 || !strings.HasPrefix(log, "- 2026-10-13T09:00:00Z `ingest`") {
		t.Fatalf("log = %q", log)
	}
}

func TestSearchAndParse(t *testing.T) {
	s := newStore(t)
	_ = s.WritePage("a.md", Page{Type: "integration", Title: "A-114", Description: "legacy integration", Tags: []string{"merchant"}})
	_ = s.WritePage("b.md", Page{Type: "concept", Title: "B", Body: "один два три чотири п'ять шість сім вісім дев'ять десять одинадцять дванадцять тринадцять"})
	hits, err := s.Search("legacy")
	if err != nil || len(hits) != 1 || hits[0].Source != "okf:wiki/a.md" {
		t.Fatalf("Search = %+v, %v", hits, err)
	}
	all, _ := s.Search("")
	if len(all) != 2 || !strings.HasSuffix(all[1].Text, "…") {
		t.Fatalf("empty query must list all pages, long bodies truncated: %+v", all)
	}

	// v0.1 pages used `timestamp`; they must still parse.
	p, err := ParsePage([]byte("---\ntype: concept\ntimestamp: \"2026-01-01\"\ntags: [a, b]\n---\nbody\n"))
	if err != nil || p.GeneratedAt != "2026-01-01" || len(p.Tags) != 2 {
		t.Fatalf("v0.1 page = %+v, %v", p, err)
	}
	for _, bad := range []string{"no frontmatter", "---\ntitle: x\n---\nbody", "---\ntype: x\nbody without close"} {
		if _, err := ParsePage([]byte(bad)); err == nil {
			t.Errorf("ParsePage(%q) accepted a malformed page", bad)
		}
	}
	if err := os.WriteFile(filepath.Join(s.wikiDir(), "broken.md"), []byte("nope"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := s.RegenIndex(); err == nil {
		t.Error("an unparsable page must fail the index build, not vanish from it")
	}
	if _, err := s.Search("x"); err == nil {
		t.Error("an unparsable page must fail the search")
	}
}
