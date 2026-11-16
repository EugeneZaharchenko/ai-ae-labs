// ДЗ 12 — Personal Research Agent з безпечним самополіпшенням (autoresearch).
//
// Конвеєр (ДЗ 5–11): план → паралельний збір (kb_search + graph_lookup,
// fan-out + JoinNode) → чернетка → цикл критиків → HITL → фінальний звіт.
// Ядро самополіпшення: агент редагує лише strategy.md у межах бюджету, за
// однією чистою метрикою, і кожен експеримент іде в experiments.log.
//
//	go run . --ask "Хто має затвердити повернення 3000 EUR?"     # конвеєр із паузою для людини
//	go run . --self-improve --eval=eval/ --strategy=./strategy.md --experiments=6
//	go run . --self-improve --parallel=3 --experiments=6            # ++ Advanced: паралельні воркери
//	go run . --evaluate --eval=eval/ --set=generalization           # таблиця по питаннях і типах фактів
//	go run . --replay                                              # перевірити experiments.log
//
// Без провайдера (або з --offline) працюють правила замість моделей, і вивід
// першим рядком каже про це: такий прогін доводить гальма й проводку, а не те,
// що LLM покращує власну стратегію.
package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	if err := run(ctx, os.Args[1:], os.Stdin, os.Stdout); err != nil {
		fmt.Fprintln(os.Stderr, "помилка:", err)
		os.Exit(1)
	}
}
