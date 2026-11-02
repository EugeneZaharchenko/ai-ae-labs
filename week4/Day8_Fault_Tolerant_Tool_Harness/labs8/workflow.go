package main

import (
	"fmt"
	"strings"
	"time"

	"go.temporal.io/sdk/contrib/googleadk"
	"go.temporal.io/sdk/temporal"
	"go.temporal.io/sdk/workflow"
	"google.golang.org/adk/v2/agent"
	"google.golang.org/adk/v2/agent/llmagent"
	"google.golang.org/adk/v2/runner"
	"google.golang.org/adk/v2/session"
	"google.golang.org/genai"
)

const (
	appName   = "ledgerworks"
	taskQueue = "labs8-refunds"
	// modelName is the logical name the workflow uses. The worker maps it to
	// the real model (demo rules or the provider from apps/.env).
	modelName = "ledgerworks-model"
)

const instruction = `You are the LEDGERWORKS refund desk agent. You open refund cases and look up merchant payouts.
Copy ids exactly as the user wrote them: merchants look like A-114, transactions like txn-2026-07-118845. Never send placeholders such as "undefined".
If a tool returns status "invalid_argument", read the observation, fix the named field from the user's message, and call the tool again.
If a tool returns status "unavailable", do not call it again; tell the user what happened.
When the user asks what you know or remember about a merchant or a transaction, use open_nodes for known ids and search_nodes for words.`

// Request is one turn of a conversation.
type Request struct {
	UserID    string
	SessionID string
	Message   string
}

// Result is what start prints.
type Result struct {
	Answer          string
	Events          []string
	SelfCorrections int
	LoadedEvents    int // events of the earlier turns, imported from SQLite
}

// storeOptions is for session and memory Activities.
var storeOptions = workflow.ActivityOptions{
	StartToCloseTimeout: 20 * time.Second,
	RetryPolicy: &temporal.RetryPolicy{
		InitialInterval: time.Second, MaximumAttempts: 5,
		NonRetryableErrorTypes: []string{errTypeSemantic, errTypeFatal},
	},
}

// AgentWorkflow is the agent loop of HW7 as a Temporal Workflow. Every LLM
// call is an Activity (googleadk.NewModel), every tool call is an Activity
// (tools.go), so after a crash the history is replayed and finished calls
// are not made again.
func AgentWorkflow(ctx workflow.Context, req Request) (Result, error) {
	var res Result
	key := SessionKey{AppName: appName, UserID: req.UserID, SessionID: req.SessionID}
	sctx := workflow.WithActivityOptions(ctx, storeOptions)
	var a *Activities

	// L3: the conversation lives in SQLite, the runner only sees an in-memory copy.
	var snap *googleadk.SessionSnapshot
	if err := workflow.ExecuteActivity(sctx, a.LoadSession, key).Get(ctx, &snap); err != nil {
		return res, err
	}
	adkCtx := googleadk.NewContext(ctx)
	svc := session.InMemoryService()
	if snap != nil {
		if _, err := googleadk.ImportSession(adkCtx, svc, snap); err != nil {
			return res, err
		}
		res.LoadedEvents = len(snap.Events)
	} else if _, err := svc.Create(adkCtx, &session.CreateRequest{AppName: appName, UserID: req.UserID, SessionID: req.SessionID}); err != nil {
		return res, err
	}

	b := &budget{}
	tools, err := newTools(b)
	if err != nil {
		return res, err
	}
	root, err := llmagent.New(llmagent.Config{
		Name: "refund_agent", Description: "Opens refund cases for LEDGERWORKS.",
		Model: googleadk.NewModel(modelName), Instruction: instruction, Tools: tools,
	})
	if err != nil {
		return res, err
	}
	r, err := runner.New(runner.Config{AppName: appName, Agent: root, SessionService: svc})
	if err != nil {
		return res, err
	}

	msg := genai.NewContentFromText(req.Message, genai.RoleUser)
	for ev, err := range r.Run(adkCtx, req.UserID, req.SessionID, msg, agent.RunConfig{}) {
		if err != nil {
			return res, err
		}
		for _, line := range eventLines(ev) {
			res.Events = append(res.Events, line)
			if strings.HasPrefix(line, "[ev:answer]") {
				res.Answer = strings.TrimPrefix(line, "[ev:answer] ")
			}
		}
		if b.stop != nil {
			res.SelfCorrections = b.used
			errType := errTypeFatal
			if b.used > maxSelfCorrections {
				errType = errTypeExhausted
			}
			// The Go type does not cross into the client either, so the type string goes with it.
			return res, temporal.NewNonRetryableApplicationError(b.stop.Error(), errType, b.stop)
		}
	}
	res.SelfCorrections = b.used

	out, err := googleadk.ExportSession(adkCtx, svc, appName, req.UserID, req.SessionID)
	if err != nil {
		return res, err
	}
	if err := workflow.ExecuteActivity(sctx, a.PersistSession, out).Get(ctx, nil); err != nil {
		return res, err
	}
	if err := workflow.ExecuteActivity(sctx, a.RememberSession, out).Get(ctx, nil); err != nil {
		return res, err
	}
	return res, nil
}

// eventLines renders an event the way Taras reads the log. fmt prints maps
// with sorted keys, so the lines are the same on replay.
func eventLines(ev *session.Event) []string {
	if ev == nil || ev.Content == nil {
		return nil
	}
	var out []string
	for _, p := range ev.Content.Parts {
		switch {
		case p == nil:
		case p.FunctionCall != nil:
			out = append(out, fmt.Sprintf("[ev:tool_call] %s %v", p.FunctionCall.Name, p.FunctionCall.Args))
		case p.FunctionResponse != nil:
			tag := "[ev:tool_observation]"
			if s, _ := p.FunctionResponse.Response["status"].(string); s == "invalid_argument" || s == "unavailable" {
				tag = "[ev:tool_observation ERROR]"
			} else if _, failed := p.FunctionResponse.Response["error"]; failed {
				tag = "[ev:tool_observation ERROR]"
			}
			out = append(out, fmt.Sprintf("%s %s %v", tag, p.FunctionResponse.Name, p.FunctionResponse.Response))
		case strings.TrimSpace(p.Text) != "":
			out = append(out, "[ev:answer] "+strings.TrimSpace(p.Text))
		}
	}
	return out
}
