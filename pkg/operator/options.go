package operator

import (
	"flag"
	"fmt"
	"time"

	"github.com/mc-agents/operator/api/v1alpha1"
	"github.com/mc-agents/operator/pkg/botimage"
)

type Options struct {
	Kubeconfig     string
	Namespace      string
	Workers        int
	ResyncPeriod   time.Duration
	MetricsAddress string
	ProbeAddress   string

	LeaderElect              bool
	LeaderElectID            string
	LeaderElectNamespace     string
	LeaderElectLeaseDuration time.Duration

	BotRegistry       string
	MineflayerTag     string
	FabricTag         string
	AssetFetcherImage string
	SpawnInterval     time.Duration
}

func DefaultOptions() Options {
	return Options{
		Workers:                  2,
		ResyncPeriod:             10 * time.Minute,
		MetricsAddress:           ":8080",
		ProbeAddress:             ":8081",
		LeaderElectID:            "mc-agents-operator." + v1alpha1.GroupName,
		LeaderElectLeaseDuration: 15 * time.Second,
		BotRegistry:              botimage.DefaultRegistry,
		MineflayerTag:            "latest",
		FabricTag:                "latest",
		SpawnInterval:            4 * time.Second,
	}
}

func (o *Options) Bind(fs *flag.FlagSet) {
	fs.StringVar(&o.Kubeconfig, "kubeconfig", o.Kubeconfig,
		"path to a kubeconfig; empty uses the in-cluster config")
	fs.StringVar(&o.Namespace, "namespace", o.Namespace,
		"namespace to watch; empty watches every namespace")
	fs.IntVar(&o.Workers, "workers", o.Workers,
		"concurrent reconciles per controller")
	fs.DurationVar(&o.ResyncPeriod, "resync-period", o.ResyncPeriod,
		"informer resync period")
	fs.StringVar(&o.MetricsAddress, "metrics-bind-address", o.MetricsAddress,
		"address the Prometheus endpoint binds to")
	fs.StringVar(&o.ProbeAddress, "health-probe-bind-address", o.ProbeAddress,
		"address the health probe endpoint binds to")

	fs.BoolVar(&o.LeaderElect, "leader-elect", o.LeaderElect,
		"enable leader election; required when replicas > 1")
	fs.StringVar(&o.LeaderElectID, "leader-elect-id", o.LeaderElectID,
		"name of the Lease used for leader election")
	fs.StringVar(&o.LeaderElectNamespace, "leader-elect-namespace", o.LeaderElectNamespace,
		"namespace holding the leader election Lease; empty uses the pod namespace")
	fs.DurationVar(&o.LeaderElectLeaseDuration, "leader-elect-lease-duration", o.LeaderElectLeaseDuration,
		"how long a lost leader stays fenced before another replica takes over")

	fs.StringVar(&o.BotRegistry, "bot-registry", o.BotRegistry,
		"registry and namespace holding the bot images")
	fs.StringVar(&o.MineflayerTag, "mineflayer-tag", o.MineflayerTag,
		"default tag for bot-mineflayer images")
	fs.StringVar(&o.FabricTag, "fabric-tag", o.FabricTag,
		"default tag for bot-fabric images; the Minecraft version is appended as -mc<version>")
	fs.StringVar(&o.AssetFetcherImage, "asset-fetcher-image", o.AssetFetcherImage,
		"image of the initContainer that pulls the client jar and assets from the Mojang manifest")
	fs.DurationVar(&o.SpawnInterval, "spawn-interval", o.SpawnInterval,
		"minimum gap between two bot pod creations; Paper refuses a reconnect within 4s of the last one")
}

func (o *Options) Validate() error {
	if o.Workers < 1 {
		return fmt.Errorf("--workers must be at least 1")
	}
	if o.SpawnInterval < 0 {
		return fmt.Errorf("--spawn-interval must not be negative")
	}
	if o.LeaderElect && o.LeaderElectID == "" {
		return fmt.Errorf("--leader-elect-id must not be empty when leader election is on")
	}
	return nil
}
