// ДЗ 4: ADK REST навколо того самого графа ДЗ 3, без LLM і без ключів.
// ADK Go v2.4.0, станом на 09/2026.
package main

import (
	"context"
	"flag"
	"log"
	"net"
	"os"
	"os/signal"
	"syscall"
	"time"
)

func main() {
	defaultAddr, err := listenAddress(os.Getenv("PORT"))
	if err != nil {
		log.Fatal(err)
	}
	addr := flag.String("addr", defaultAddr, "HTTP listen address (PORT binds all interfaces; otherwise loopback)")
	drainTimeout := flag.Duration("drain-timeout", 30*time.Second, "maximum graceful shutdown duration")
	flag.Parse()
	if *drainTimeout <= 0 {
		log.Fatal("drain-timeout must be positive")
	}
	svc, err := newService()
	if err != nil {
		log.Fatal(err)
	}
	listener, err := net.Listen("tcp", *addr)
	if err != nil {
		log.Fatal(err)
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	log.Printf("ADK service listening at %s (no model credentials required)", listener.Addr())
	if err := svc.serve(ctx, listener, *drainTimeout); err != nil {
		log.Fatal(err)
	}
	log.Print("ADK service drained and stopped")
}
