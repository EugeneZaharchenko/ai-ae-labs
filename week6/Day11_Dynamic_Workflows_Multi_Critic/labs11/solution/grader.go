// Об'єктивний грейдер лаби: справжні go build / go vet / go test.
//
// Сама робота — в internal/gate (ADK-вільний пакет з власними тестами); тут
// лише адаптер до інтерфейсу Grader з ядра циклу. Саме цей грейдер підставляє
// main.go. RuleGrader із criticloop.go лишається для тестів політики циклу.
package main

import (
	"context"

	"github.com/dimetron/ai-eng-course/labs/week6/Day11_Dynamic_Workflows_Multi_Critic/labs11/internal/gate"
)

// ExecGrader проганяє кандидата крізь gate проти заданого файлу тестів.
type ExecGrader struct {
	Runner gate.Runner
	// Test — вміст *_test.go, що є специфікацією для цього завдання.
	Test string
}

// Grade реалізує Grader.
//
// Сирий вивід інструмента (обрізаний у gate) іде у Failures як є: maker
// виправляє те, що сказав компілятор, а не те, що про це подумав пересказ.
func (g ExecGrader) Grade(ctx context.Context, code string) GradeReport {
	rep := g.Runner.Grade(ctx, gate.Spec{Code: code, Test: g.Test})
	if rep.OK {
		return GradeReport{OK: true}
	}
	return GradeReport{
		Stage:    rep.Stage,
		Failures: []string{rep.Stage + ": " + rep.Output},
	}
}
