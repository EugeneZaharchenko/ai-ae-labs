package refund

import (
	"context"
	"iter"
	"strings"
	"sync"
	"testing"

	"google.golang.org/adk/v2/agent"
	"google.golang.org/adk/v2/model"
	"google.golang.org/adk/v2/session"
	"google.golang.org/adk/v2/tool"
)

// The interface assertion: if a future ADK release changes model.LLM, this line
// stops compiling, which is cheaper than a runtime surprise in a lab.
var _ Classifier = (*stubClassifier)(nil)

func TestPrepare(t *testing.T) {
	for _, tc := range []struct {
		input string
		want  Input
		bad   bool
	}{
		{"Мерчант A-114: txn-2026-07-118845", Input{"txn-2026-07-118845", "A-114"}, false},
		{"TXN-123 для b-207.", Input{"txn-123", "B-207"}, false},
		{"", Input{}, true},
		{"A-114", Input{}, true},
		{"txn-123", Input{}, true},
	} {
		t.Run(tc.input, func(t *testing.T) {
			ctx := agent.NewStrictContextMock(t.Context())
			got, err := Prepare(&ctx, tc.input)
			if (err != nil) != tc.bad || got != tc.want {
				t.Fatalf("Prepare = %+v, %v; want %+v, bad=%v", got, err, tc.want, tc.bad)
			}
		})
	}
}

func TestFormat(t *testing.T) {
	valid := Output{"rc-txn-123-A-114", "txn-123", "A-114", "pending"}
	for _, tc := range []struct {
		name string
		in   Output
		bad  bool
	}{
		{"valid", valid, false},
		{"duplicate", Output{valid.CaseID, valid.TransactionID, valid.MerchantID, "already_open"}, false},
		{"empty", Output{}, true},
		{"missing transaction", Output{valid.CaseID, "", valid.MerchantID, valid.Status}, true},
		{"missing merchant", Output{valid.CaseID, valid.TransactionID, "", valid.Status}, true},
		{"invented status", Output{valid.CaseID, valid.TransactionID, valid.MerchantID, "paid"}, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := agent.NewStrictContextMock(t.Context())
			got, err := Format(&ctx, tc.in)
			if (err != nil) != tc.bad {
				t.Fatalf("Format = %q, %v", got, err)
			}
			if !tc.bad && (!strings.Contains(got, tc.in.CaseID) || !strings.Contains(got, tc.in.Status)) {
				t.Fatalf("lost result fields: %s", got)
			}
		})
	}
}

type toolContext struct {
	agent.StrictContextMock
	actions session.EventActions
}

func (c *toolContext) Actions() *session.EventActions { return &c.actions }

func TestToolStateAndIdempotency(t *testing.T) {
	reg := &Registry{}
	for _, tc := range []struct {
		in     Input
		status string
	}{
		{Input{"TXN-123", "a-114"}, "pending"},
		{Input{"txn-123", "A-114"}, "already_open"},
		{Input{"invalid", "A-114"}, ""},
		{Input{"txn-123", "Z-999"}, ""},
	} {
		ctx := &toolContext{StrictContextMock: agent.NewStrictContextMock(context.Background())}
		out, err := reg.OpenCase(ctx, tc.in)
		if tc.status == "" {
			if err == nil || len(ctx.actions.StateDelta) != 0 {
				t.Fatalf("rejected input mutated state: %+v, %v", ctx.actions, err)
			}
			continue
		}
		if err != nil || out.Status != tc.status || out.CaseID != "rc-txn-123-A-114" {
			t.Fatalf("OpenCase = %+v, %v", out, err)
		}
		if ctx.actions.StateDelta[StateKeyStatus] != tc.status || ctx.actions.StateDelta[StateKeyCaseID] != out.CaseID {
			t.Fatalf("missing audit: %v", ctx.actions.StateDelta)
		}
	}
	if len(reg.cases) != 1 {
		t.Fatalf("registry entries = %d", len(reg.cases))
	}
}

func TestConcurrentDuplicate(t *testing.T) {
	reg := &Registry{}
	var wg sync.WaitGroup
	for range 20 {
		wg.Go(func() {
			ctx := &toolContext{StrictContextMock: agent.NewStrictContextMock(context.Background())}
			if _, err := reg.OpenCase(ctx, Input{"txn-123", "A-114"}); err != nil {
				t.Error(err)
			}
		})
	}
	wg.Wait()
	if len(reg.cases) != 1 {
		t.Fatalf("registry entries = %d", len(reg.cases))
	}
}

func TestPrepareStatus(t *testing.T) {
	for _, tc := range []struct {
		input string
		want  StatusInput
		bad   bool
	}{
		{"check status rc-txn-2026-07-118845-A-114", StatusInput{"rc-txn-2026-07-118845-a-114"}, false},
		{"RC-TXN-123-A-114?", StatusInput{"rc-txn-123-a-114"}, false},
		{"перевір статус кейса rc-txn-123-B-207, будь ласка", StatusInput{"rc-txn-123-b-207"}, false},
		{"", StatusInput{}, true},
		{"status?", StatusInput{}, true},
		// A transaction ID is not a case ID: reading a case needs the ID the
		// register issued, and guessing one from a transaction is how a status
		// answer turns into a fiction.
		{"status txn-2026-07-118845 A-114", StatusInput{}, true},
		{"rc-", StatusInput{}, true},
	} {
		t.Run(tc.input, func(t *testing.T) {
			ctx := agent.NewStrictContextMock(t.Context())
			got, err := PrepareStatus(&ctx, tc.input)
			if (err != nil) != tc.bad || got != tc.want {
				t.Fatalf("PrepareStatus = %+v, %v; want %+v, bad=%v", got, err, tc.want, tc.bad)
			}
		})
	}
}

func TestLookup(t *testing.T) {
	reg := &Registry{}
	open := &toolContext{StrictContextMock: agent.NewStrictContextMock(context.Background())}
	if _, err := reg.OpenCase(open, Input{"txn-2026-07-118845", "A-114"}); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		in     StatusInput
		status string
	}{
		{StatusInput{"rc-txn-2026-07-118845-A-114"}, "pending"},
		{StatusInput{"RC-TXN-2026-07-118845-a-114"}, "pending"},
		// Never opened in this process: an error, not an invented status.
		{StatusInput{"rc-txn-999-A-114"}, ""},
		{StatusInput{"not-a-case"}, ""},
		{StatusInput{""}, ""},
		// A transaction ID must not be accepted as a case ID.
		{StatusInput{"txn-2026-07-118845"}, ""},
	} {
		ctx := &toolContext{StrictContextMock: agent.NewStrictContextMock(context.Background())}
		out, err := reg.Lookup(ctx, tc.in)
		if tc.status == "" {
			if err == nil {
				t.Fatalf("Lookup(%+v) = %+v, want error", tc.in, out)
			}
			continue
		}
		if err != nil || out.Status != tc.status || out.CaseID != "rc-txn-2026-07-118845-A-114" || out.MerchantID != "A-114" {
			t.Fatalf("Lookup(%+v) = %+v, %v", tc.in, out, err)
		}
		if ctx.actions.StateDelta[StateKeyStatus] != tc.status || ctx.actions.StateDelta[StateKeyCaseID] != out.CaseID {
			t.Fatalf("missing audit: %v", ctx.actions.StateDelta)
		}
	}
	// Reading twice must not turn a pending case into already_open: Lookup is
	// a read, and the register is the only thing allowed to change status.
	if len(reg.cases) != 1 {
		t.Fatalf("registry entries = %d", len(reg.cases))
	}
	if got := reg.cases[canonicalCaseID("rc-txn-2026-07-118845-A-114")].Status; got != "pending" {
		t.Fatalf("Lookup mutated the register: %s", got)
	}
}

func TestClassify(t *testing.T) {
	for _, tc := range []struct {
		input string
		want  string
	}{
		{"Мерчант A-114 просить повернення по транзакції txn-2026-07-118845", RouteRefund},
		{"open a refund case for txn-2026-07-118845", RouteRefund},
		{"check status rc-txn-2026-07-118845-A-114", RouteStatus},
		{"перевір статус rc-txn-2026-07-118845-A-114", RouteStatus},
		{"rc-txn-2026-07-118845-A-114", RouteStatus},
		{"status?", RouteStatus},
		{"статус", RouteStatus},
		// Both vocabularies present: the status branch wins, because asking
		// about an existing case is a read regardless of the word "refund".
		{"check the status of the refund for rc-txn-123-A-114", RouteStatus},
		// IDs outrank vocabulary. An unopenable refund request still belongs to
		// the refund branch, where it fails with a reason instead of a shrug.
		{"Мерчант Z-999 просить повернення по транзакції txn-123", RouteRefund},
		{"refund txn-123 A-114", RouteRefund},
		{"привіт, як справи?", RouteOutOfDomain},
		{"", RouteOutOfDomain},
		// One ID is not enough to open a case and not a status request either.
		{"txn-123", RouteOutOfDomain},
	} {
		t.Run(tc.input, func(t *testing.T) {
			ctx := agent.NewStrictContextMock(t.Context())
			got, err := Classify(&ctx, tc.input)
			if err != nil || got != tc.want {
				t.Fatalf("Classify(%q) = %q, %v; want %q", tc.input, got, err, tc.want)
			}
		})
	}
}

func TestFormatStatusAndOutOfDomain(t *testing.T) {
	valid := Output{"rc-txn-123-A-114", "txn-123", "A-114", "pending"}
	ctx := agent.NewStrictContextMock(t.Context())
	got, err := FormatStatus(&ctx, valid)
	if err != nil || !strings.Contains(got, valid.CaseID) || !strings.Contains(got, "уже в реєстрі") {
		t.Fatalf("FormatStatus = %q, %v", got, err)
	}
	if _, err := FormatStatus(&ctx, Output{valid.CaseID, valid.TransactionID, valid.MerchantID, "paid"}); err == nil {
		t.Fatal("FormatStatus accepted an invented status")
	}
	if _, err := OutOfDomain(&ctx, "   "); err == nil {
		t.Fatal("OutOfDomain accepted an empty request")
	}
	if got, err := OutOfDomain(&ctx, "привіт"); err != nil || !strings.Contains(got, "привіт") {
		t.Fatalf("OutOfDomain = %q, %v", got, err)
	}
}

func TestToolsExposeBothSides(t *testing.T) {
	reg := &Registry{}
	for _, tc := range []struct {
		name string
		make func(*Registry) (tool.Tool, error)
	}{
		{"open_refund_case", NewTool},
		{"check_refund_status", NewStatusTool},
	} {
		got, err := tc.make(reg)
		if err != nil {
			t.Fatalf("%s: %v", tc.name, err)
		}
		if got.Name() != tc.name {
			t.Fatalf("tool name = %q, want %q", got.Name(), tc.name)
		}
	}
}

// stubClassifier is the smallest model.LLM: it proves the interface is
// satisfied and lets the accessor tests run without a provider.
type stubClassifier struct {
	name  string
	calls int
}

func (s *stubClassifier) Name() string { return s.name }

func (s *stubClassifier) GenerateContent(_ context.Context, _ *model.LLMRequest, _ bool) iter.Seq2[*model.LLMResponse, error] {
	s.calls++
	return func(yield func(*model.LLMResponse, error) bool) {}
}

// The model classifier is optional, and its off state is the one every other
// test depends on: a fresh Registry must route without a provider.
func TestRegistryClassifierIsOptIn(t *testing.T) {
	reg := &Registry{}
	if got := reg.ClassifierModel(); got != nil {
		t.Fatalf("a fresh registry already has a classifier: %v", got)
	}
	// A zero-value Registry must be usable before and after the setter, so the
	// switch cannot be the thing that lazily initialises it.
	newCtx := func() *toolContext {
		return &toolContext{StrictContextMock: agent.NewStrictContextMock(context.Background())}
	}
	if _, err := reg.OpenCase(newCtx(), Input{"txn-123", "A-114"}); err != nil {
		t.Fatal(err)
	}
	m := &stubClassifier{name: "stub"}
	reg.SetClassifier(m)
	if got := reg.ClassifierModel(); got != m {
		t.Fatalf("ClassifierModel() = %v, want the model that was set", got)
	}
	// Routing with a classifier installed still opens cases: the register does
	// not care who picked the branch.
	if _, err := reg.OpenCase(newCtx(), Input{"txn-456", "B-207"}); err != nil {
		t.Fatal(err)
	}
	reg.SetClassifier(nil)
	if got := reg.ClassifierModel(); got != nil {
		t.Fatalf("SetClassifier(nil) left a classifier: %v", got)
	}
}
