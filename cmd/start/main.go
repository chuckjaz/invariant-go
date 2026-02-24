package main

import (
	"context"
	"flag"
	"log"
	"os"
	"os/signal"
	"syscall"
	"time"

	"invariant/internal/start"
)

func main() {
	var configPath string
	flag.StringVar(&configPath, "config", "services.yaml", "Path to the YAML configuration file")
	var maxBackoff time.Duration
	flag.DurationVar(&maxBackoff, "max-backoff", 5*time.Minute, "Configurable amount of time to try exponential back-off before waiting the retry-interval")
	var retryInterval time.Duration
	flag.DurationVar(&retryInterval, "retry-interval", 10*time.Minute, "Time to wait before retrying to start a process that has failed beyond the max backoff")
	flag.Parse()

	cfg, err := start.LoadConfig(configPath)
	if err != nil {
		log.Fatalf("Failed to load configuration: %v", err)
	}

	rc := start.RunnerConfig{
		MaxBackoffDuration: maxBackoff,
		RetryInterval:      retryInterval,
		Config:             cfg,
	}

	runner, err := start.NewRunner(rc)
	if err != nil {
		log.Fatalf("Failed to initialize runner: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Handle graceful shutdown on interrupt
	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, syscall.SIGINT, syscall.SIGTERM)

	go func() {
		<-sigChan
		log.Println("Received termination signal, stopping all services...")
		cancel()
	}()

	log.Printf("Starting services from %s...", configPath)
	runner.Start(ctx)
	log.Println("All services stopped. Exiting.")
}
