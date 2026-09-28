// C3 · Swarm — several role agents argue on a shared blackboard, with no
// coordinator deciding for them, until they all agree or a hard round cap
// stops them.
//
// ADK Go v2.4.0 has NO swarm primitive. This is the hand-built version the
// catalog describes: one dynamic node (plain Go loop) + a blackboard + RunNode
// for each role's turn.
//
//	debate (DynamicNode) ⟲ round ≤ maxRounds:
//	    engineer → cost → designer   (each reads and appends to the board)
//	    all AGREE? → consensus
//
//	go run .              # scripted demo, offline: consensus in round 2
//	go run . console
package main

import (
	"fmt"
	"strings"

	"google.golang.org/adk/v2/agent"
	"google.golang.org/adk/v2/agent/llmagent"
	"google.golang.org/adk/v2/agent/workflowagent"
	"google.golang.org/adk/v2/session"
	"google.golang.org/adk/v2/workflow"

	"github.com/dimetron/ai-eng-course/labs/demo/3_adk2_patterns/internal/kit"
)

var spec = kit.Spec{
	ID:    "C3",
	Title: "Swarm",
	Input: "Design the charging cable for our home EV charger",
	Build: build,
}

func main() { kit.Main(spec) }

// maxRounds is the hard cap. Convergence is NOT guaranteed; without a cap a
// swarm is an unbounded bill.
const maxRounds = 4

// agreeWord is the token a role writes when it accepts the current proposal.
const agreeWord = "AGREE"

// role is one voice of the swarm with its offline position per round.
type role struct {
	name, goal string
	// first is the round-1 proposal; later rounds agree once the board holds
	// a compromise from every role.
	first string
}

var roles = []role{
	{"engineer", "safety and durability", "5 m cable, 32 A, liquid-free copper"},
	{"cost", "unit cost under 80 EUR", "7 m is too expensive; cap at 5 m and 16 A"},
	{"designer", "ease of use", "needs a holster and a coiled 5 m cable"},
}

func roleBrain(r role) kit.Brain {
	return func(p kit.Prompt) kit.Reply {
		if strings.Contains(p.Text, "Round 1") {
			return kit.Say("%s: PROPOSE %s", r.name, r.first)
		}
		return kit.Say("%s: %s 5 m coiled cable, 32 A, holster included", r.name, agreeWord)
	}
}

// turnInput is what a role sees: the round, the goal and the whole board.
// The board is the only shared context — roles never read each other's
// sessions.
func turnInput(round int, topic string, board []string) string {
	return fmt.Sprintf("Round %d. Topic: %s\nBlackboard:\n%s\nReply '<role>: AGREE <final design>' if you accept, or '<role>: PROPOSE <change>'.",
		round, topic, strings.Join(board, "\n"))
}

func debate(nodes []workflow.Node) workflow.DynamicFn[string, string] {
	return func(ctx agent.Context, topic string, _ func(*session.Event) error) (string, error) {
		var board []string
		for round := 1; round <= maxRounds; round++ {
			agreed := 0
			for _, n := range nodes {
				out, err := workflow.RunNode[any](ctx, n, turnInput(round, topic, board))
				if err != nil {
					return "", fmt.Errorf("round %d, %s: %w", round, n.Name(), err)
				}
				msg := strings.TrimSpace(fmt.Sprint(out))
				board = append(board, msg)
				if strings.Contains(msg, agreeWord) {
					agreed++
				}
			}
			if agreed == len(nodes) {
				return fmt.Sprintf("consensus in round %d: %s", round, afterAgree(board[len(board)-1])), nil
			}
		}
		return fmt.Sprintf("no consensus after %d rounds; last board entry: %s", maxRounds, board[len(board)-1]), nil
	}
}

func afterAgree(msg string) string {
	if i := strings.Index(msg, agreeWord); i >= 0 {
		return strings.TrimSpace(msg[i+len(agreeWord):])
	}
	return msg
}

func build(m kit.Models) (agent.Agent, error) { return buildWith(m, roleBrain) }

// buildWith takes the offline brain per role, so a test can seat a role that
// never agrees and watch the round cap end the debate.
func buildWith(m kit.Models, brainFor func(role) kit.Brain) (agent.Agent, error) {
	nodes := make([]workflow.Node, 0, len(roles))
	for _, r := range roles {
		a, err := llmagent.New(llmagent.Config{
			Name:        r.name,
			Description: "swarm role: " + r.goal,
			Model:       m.For(r.name, brainFor(r)),
			Instruction: fmt.Sprintf(`You are the %s in a design debate. You care about %s.
Read the blackboard. Start your reply with "%s:". Write AGREE and the final design
only if the latest proposals satisfy your goal; otherwise write PROPOSE and one change.`,
				r.name, r.goal, r.name),
		})
		if err != nil {
			return nil, err
		}
		n, err := workflow.NewAgentNode(a, workflow.NodeConfig{})
		if err != nil {
			return nil, err
		}
		nodes = append(nodes, n)
	}
	return workflowagent.New(workflowagent.Config{
		Name:        "swarm",
		Description: "Role agents debate on a shared blackboard.",
		Edges:       workflow.Chain(workflow.Start, workflow.NewDynamicNode("debate", debate(nodes), workflow.NodeConfig{})),
	})
}
