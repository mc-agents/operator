package main

import (
	"flag"
	"fmt"
	"time"

	"github.com/mc-agents/operator/api/v1alpha1"
	"github.com/mc-agents/operator/internal/botimage"
)

type options struct {
	namespace      string
	workers        int
	resyncPeriod   time.Duration
	metricsAddress string
	probeAddress   string

	leaderElect              bool
	leaderElectID            string
	leaderElectNamespace     string
	leaderElectLeaseDuration time.Duration

	botRegistry       string
	fabricTag         string
	assetFetcherImage string
	spawnInterval     time.Duration
}

func defaultOptions() options {
	return options{
		workers:                  2,
		resyncPeriod:             10 * time.Minute,
		metricsAddress:           ":8080",
		probeAddress:             ":8081",
		leaderElectID:            componentName + "." + v1alpha1.GroupName,
		leaderElectLeaseDuration: 15 * time.Second,
		botRegistry:              botimage.DefaultRegistry,
		fabricTag:                "latest",
		spawnInterval:            4 * time.Second,
	}
}

func (o *options) bind(fs *flag.FlagSet) {
	fs.StringVar(&o.namespace, "namespace", o.namespace,
		"namespace to watch; empty watches every namespace")
	fs.IntVar(&o.workers, "workers", o.workers,
		"concurrent reconciles per controller")
	fs.DurationVar(&o.resyncPeriod, "resync-period", o.resyncPeriod,
		"informer resync period")
	fs.StringVar(&o.metricsAddress, "metrics-bind-address", o.metricsAddress,
		"address the Prometheus endpoint binds to; 0 disables it")
	fs.StringVar(&o.probeAddress, "health-probe-bind-address", o.probeAddress,
		"address the health probe endpoint binds to")

	fs.BoolVar(&o.leaderElect, "leader-elect", o.leaderElect,
		"enable leader election; required when replicas > 1")
	fs.StringVar(&o.leaderElectID, "leader-elect-id", o.leaderElectID,
		"name of the Lease used for leader election")
	fs.StringVar(&o.leaderElectNamespace, "leader-elect-namespace", o.leaderElectNamespace,
		"namespace holding the leader election Lease; empty uses the pod namespace")
	fs.DurationVar(&o.leaderElectLeaseDuration, "leader-elect-lease-duration", o.leaderElectLeaseDuration,
		"how long a lost leader stays fenced before another replica takes over")

	fs.StringVar(&o.botRegistry, "bot-registry", o.botRegistry,
		"registry and namespace holding the bot images")
	fs.StringVar(&o.fabricTag, "fabric-tag", o.fabricTag,
		"default tag for bot-fabric images; the Minecraft version is appended as -mc<version>")
	fs.StringVar(&o.assetFetcherImage, "asset-fetcher-image", o.assetFetcherImage,
		"image of the initContainer that pulls the client jar and assets from the Mojang manifest")
	fs.DurationVar(&o.spawnInterval, "spawn-interval", o.spawnInterval,
		"minimum gap between two bot pod creations; Paper refuses a reconnect within 4s of the last one")
}

func (o *options) validate() error {
	if o.workers < 1 {
		return fmt.Errorf("--workers must be at least 1")
	}
	if o.spawnInterval < 0 {
		return fmt.Errorf("--spawn-interval must not be negative")
	}
	if o.leaderElect && o.leaderElectID == "" {
		return fmt.Errorf("--leader-elect-id must not be empty when leader election is on")
	}
	if o.leaderElect && o.leaderElectLeaseDuration < time.Second {
		return fmt.Errorf("--leader-elect-lease-duration must be at least 1s")
	}
	return nil
}
