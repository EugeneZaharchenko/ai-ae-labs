// HW8: Fault-Tolerant Tool Harness. The agent of HW7 runs as a Temporal
// Workflow; every LLM call and every tool call is an Activity.
//
//	temporal server start-dev --db-filename .data/temporal.db
//	docker compose up -d dgraph
//	LABS8_MODEL=demo go run . worker
//	go run . start -session s1 "Open a refund for txn-2026-07-118845 at merchant A-114"
//	go run . counter
//
// main only passes os.Args to run(); everything else is in app.go.
package main

import (
	"context"
	"log"
	"os"
)

func main() {
	if err := run(context.Background(), os.Args[1:], os.Stdout); err != nil {
		log.Fatal(err)
	}
}
