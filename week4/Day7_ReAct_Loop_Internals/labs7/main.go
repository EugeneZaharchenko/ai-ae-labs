// Стартовий шаблон ДЗ 7 — станом на ADK Go v2.6.0 (жовтень 2026).
//
// Основний шлях — штатний ADK: llmagent із колбеками (agent.go, guards.go).
// Ваша частина — той самий цикл, написаний руками (react.go) і поставлений у
// граф (graph.go): так видно, що рушій робить за вас.
//
//	go run . console        # llmagent із колбеками в інтерактивній консолі ADK (також: web)
//	go run . agent          # той самий агент, один ран із трасою Thought → Action → Observation
//	go run . team           # граф із трьох llmagent: ReAct усередині кожного вузла (team.go)
//	go run . graph console  # ВАША графова версія циклу (react.go + graph.go)
//	go test ./...           # тести; контрактні тести вашої частини стоять на t.Skip, доки ви не реалізуєте Run
//
// main.go лише збирає залежності.
package main

import (
	"context"
	"log"
	"os"
	"strings"

	"google.golang.org/adk/v2/agent"
	"google.golang.org/adk/v2/cmd/launcher"
	"google.golang.org/adk/v2/cmd/launcher/full"

	"github.com/dimetron/ai-eng-course/labs/week4/Day7_ReAct_Loop_Internals/labs7/internal/ledger"
	"github.com/dimetron/ai-eng-course/labs/week4/Day7_ReAct_Loop_Internals/labs7/internal/react"
)

// maxIterations — жорсткий ліміт циклу (≥ 5). Після вичерпання — явна помилка.
const maxIterations = 5

func main() {
	if err := run(context.Background(), os.Args[1:]); err != nil {
		log.Fatal(err)
	}
}

func run(ctx context.Context, args []string) error {
	if len(args) > 0 {
		switch args[0] {
		case "agent":
			return runAgent(ctx, os.Stdout, strings.Join(args[1:], " "))
		case "team":
			// `team web webui api` / `team console` — той самий граф у лаунчері
			// ADK: підкоманди `web` і `console` тут означають те саме, що й на
			// основному шляху. Усе інше — текст завдання для одного рану.
			if launcherArgs(args[1:]) {
				return runTeamLauncher(ctx, args[1:])
			}
			return runTeam(ctx, os.Stdout, strings.Join(args[1:], " "))
		case "graph":
			return runGraph(ctx, args[1:])
		}
	}

	// Основний шлях: штатний llmagent. Цикл крутить рушій; ліміт кроків і охорона
	// інструмента — колбеки. Справжня модель, якщо провайдер налаштований;
	// без нього — скрипт, і перший рядок виводу це каже.
	m, judge := agentModel(ctx, os.Stdout)
	a, err := NewGuardedAgent(m, ledger.New(), maxIterations, judge)
	if err != nil {
		return err
	}
	return runLauncher(ctx, a, args)
}

// runGraph — `go run . graph console`: ваша графова версія циклу. Мозок —
// справжня модель через нативний function calling (react.LLMPlanner), якщо
// провайдер налаштований; без нього — правила, і перший рядок виводу це каже.
func runGraph(ctx context.Context, args []string) error {
	reg := ledger.New().Tools()
	var p react.Planner = &react.RulePlanner{}
	if m, ok := react.ResolveModel(ctx, os.Stdout); ok {
		p = react.LLMPlanner{Model: m, Tools: reg}
	}
	a, err := NewGraphAgent(p, reg, react.Budget{MaxIterations: maxIterations})
	if err != nil {
		return err
	}
	return runLauncher(ctx, a, args)
}

// launcherArgs повідомляє, чи аргументи після `team` — це підкоманди лаунчера ADK
// (`web`, `console`, …), а не текст завдання. Без цієї перевірки `team web webui
// api` мовчки пішло б одним раном із завданням «web webui api», і жодного
// сервера ніхто б не побачив.
func launcherArgs(args []string) bool {
	for _, a := range args {
		switch a {
		case "web", "console":
			return true
		}
	}
	return false
}

// runTeamLauncher піднімає граф із трьох llmagent у лаунчері ADK — консоль або
// Web UI. Мозок вибирається так само, як у решті входів: справжня модель, якщо
// провайдер налаштований, інакше скрипт на кожен вузол.
func runTeamLauncher(ctx context.Context, args []string) error {
	ms, judge := teamModels(ctx, os.Stdout)
	a, err := NewTeamAgent(ms, ledger.New(), maxIterations, judge)
	if err != nil {
		return err
	}
	return runLauncher(ctx, a, args)
}

// runLauncher — точка підміни для тестів: справжній лаунчер блокує назавжди
// (сервер або цикл читання stdin), тож перевірити, ЩО саме йому передали, можна
// лише підмінивши його. Змінна, а не інтерфейс у сигнатурі, бо це тестовий шов, а
// не частина API лаби.
var runLauncher = launch

// launch — справжній лаунчер ADK: console читає stdin, web слухає :8080.
func launch(ctx context.Context, a agent.Agent, args []string) error {
	l := full.NewLauncher()
	return l.Execute(ctx, &launcher.Config{AgentLoader: agent.NewSingleLoader(a)}, args)
}
