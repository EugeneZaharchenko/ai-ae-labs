// E2 · Tool Confirmation Gate — the agent plans and reads freely; only one
// irreversible action needs a human's yes. The gate sits on the tool call,
// not on the whole plan (that is E1).
//
//	agent → list_buckets (free) → delete_bucket ─prod-*─→ confirm? (human) → delete / blocked
//	                                            └other──→ delete, no question asked
//
//	go run .                                  # human confirms the prod delete
//	go run . -input "Delete bucket tmp-cache"  # non-prod: no confirmation
//	go run . console
package main

import (
	"fmt"
	"strings"

	"google.golang.org/adk/v2/agent"
	"google.golang.org/adk/v2/agent/llmagent"
	"google.golang.org/adk/v2/tool"
	"google.golang.org/adk/v2/tool/functiontool"

	"github.com/dimetron/ai-eng-course/labs/demo/3_adk2_patterns/internal/kit"
)

var spec = kit.Spec{
	ID:      "E2",
	Title:   "Tool Confirmation Gate",
	Input:   "Delete bucket prod-logs",
	Answers: []any{true},
	Build:   build,
}

func main() { kit.Main(spec) }

type listArgs struct{}

type listResult struct {
	Buckets []string `json:"buckets"`
}

type deleteArgs struct {
	Bucket string `json:"bucket" jsonschema:"bucket name to delete"`
}

type deleteResult struct {
	Deleted string `json:"deleted"`
}

var buckets = []string{"prod-logs", "prod-invoices", "tmp-cache"}

func listBuckets(agent.Context, listArgs) (listResult, error) {
	return listResult{Buckets: buckets}, nil
}

func deleteBucket(_ agent.Context, in deleteArgs) (deleteResult, error) {
	for _, b := range buckets {
		if b == in.Bucket {
			// A real implementation calls the storage API here — after the
			// confirmation, never before it.
			return deleteResult{Deleted: in.Bucket}, nil
		}
	}
	return deleteResult{}, fmt.Errorf("bucket %q not found", in.Bucket)
}

// needsConfirmation is the policy: only production buckets are gated.
// Gate everything and people start rubber-stamping — worse than no gate.
func needsConfirmation(in deleteArgs) bool {
	return strings.HasPrefix(in.Bucket, "prod-")
}

func bucketIn(text string) string {
	for _, f := range strings.Fields(text) {
		f = strings.Trim(f, "?.,!'\"")
		if strings.Contains(f, "-") {
			return f
		}
	}
	return ""
}

func brain(p kit.Prompt) kit.Reply {
	b := bucketIn(p.Text)
	if b == "" {
		return kit.Say("Which bucket? I can list them for you.")
	}
	if _, ok := p.Result("list_buckets"); !ok {
		return kit.Call("list_buckets", map[string]any{})
	}
	res, ok := p.Result("delete_bucket")
	if !ok {
		return kit.Call("delete_bucket", map[string]any{"bucket": b})
	}
	if d, ok := res["deleted"]; ok {
		return kit.Say("Bucket %v deleted.", d)
	}
	return kit.Say("Bucket %s was NOT deleted: %v", b, res["error"])
}

func build(m kit.Models) (agent.Agent, error) {
	list, err := functiontool.New(functiontool.Config{
		Name:        "list_buckets",
		Description: "List storage buckets. Read-only.",
	}, listBuckets)
	if err != nil {
		return nil, err
	}
	del, err := functiontool.New(functiontool.Config{
		Name:                        "delete_bucket",
		Description:                 "Delete a storage bucket. Irreversible.",
		RequireConfirmationProvider: needsConfirmation,
	}, deleteBucket)
	if err != nil {
		return nil, err
	}
	return llmagent.New(llmagent.Config{
		Name:        "storage_agent",
		Description: "Manages storage buckets.",
		Model:       m.For("storage_agent", brain),
		Instruction: `You manage storage buckets. First call list_buckets to check the bucket exists,
then call delete_bucket when asked to delete one. Report exactly what the tool returned;
if the deletion was not confirmed, say it was not deleted.`,
		Tools: []tool.Tool{list, del},
	})
}
