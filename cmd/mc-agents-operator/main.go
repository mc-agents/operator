package main

import (
	"context"
	"flag"
	"os"
	"os/signal"
	"syscall"

	"k8s.io/klog/v2"

	"github.com/mc-agents/operator/pkg/operator"
)

func main() {
	klog.InitFlags(nil)

	opts := operator.DefaultOptions()
	opts.Bind(flag.CommandLine)
	flag.Parse()

	logger := klog.Background()
	ctx := klog.NewContext(signalContext(), logger)

	runner, err := operator.New(opts)
	if err != nil {
		logger.Error(err, "startup failed")
		os.Exit(1)
	}
	if err := runner.Run(ctx); err != nil {
		logger.Error(err, "operator exited")
		os.Exit(1)
	}
}

func signalContext() context.Context {
	ctx, cancel := context.WithCancel(context.Background())
	signals := make(chan os.Signal, 2)
	signal.Notify(signals, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		<-signals
		cancel()
		<-signals
		os.Exit(1)
	}()
	return ctx
}
