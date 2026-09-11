package queue

import (
	"context"
	"fmt"
	"sync"
	"time"

	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/cache"
	"k8s.io/client-go/util/workqueue"
	"k8s.io/klog/v2"
)

type Result struct {
	RequeueAfter time.Duration
}

type Reconciler interface {
	Reconcile(ctx context.Context, key types.NamespacedName) (Result, error)
}

type Enqueuer interface {
	Enqueue(key types.NamespacedName)
	EnqueueAfter(key types.NamespacedName, delay time.Duration)
}

type Controller struct {
	name       string
	reconciler Reconciler
	queue      workqueue.TypedRateLimitingInterface[types.NamespacedName]

	mu     sync.Mutex
	synced []cache.InformerSynced
}

func NewController(name string, reconciler Reconciler) *Controller {
	return &Controller{
		name:       name,
		reconciler: reconciler,
		queue: workqueue.NewTypedRateLimitingQueueWithConfig(
			workqueue.DefaultTypedControllerRateLimiter[types.NamespacedName](),
			workqueue.TypedRateLimitingQueueConfig[types.NamespacedName]{Name: name},
		),
	}
}

func (c *Controller) Name() string { return c.name }

func (c *Controller) WaitFor(synced ...cache.InformerSynced) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.synced = append(c.synced, synced...)
}

func (c *Controller) Enqueue(key types.NamespacedName) {
	c.queue.Add(key)
}

func (c *Controller) EnqueueAfter(key types.NamespacedName, delay time.Duration) {
	c.queue.AddAfter(key, delay)
}

func (c *Controller) Run(ctx context.Context, workers int) error {
	logger := klog.FromContext(ctx).WithName(c.name)
	ctx = klog.NewContext(ctx, logger)

	defer c.queue.ShutDown()

	c.mu.Lock()
	synced := append([]cache.InformerSynced(nil), c.synced...)
	c.mu.Unlock()

	if !cache.WaitForCacheSync(ctx.Done(), synced...) {
		return fmt.Errorf("%s: cache sync failed", c.name)
	}
	logger.Info("caches synced, starting workers", "workers", workers)

	var wg sync.WaitGroup
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for c.processNext(ctx) {
			}
		}()
	}

	<-ctx.Done()
	c.queue.ShutDown()
	wg.Wait()
	logger.Info("workers stopped")
	return nil
}

func (c *Controller) processNext(ctx context.Context) bool {
	key, shutdown := c.queue.Get()
	if shutdown {
		return false
	}
	defer c.queue.Done(key)

	logger := klog.FromContext(ctx).WithValues("key", key.String())
	result, err := c.reconciler.Reconcile(klog.NewContext(ctx, logger), key)
	switch {
	case err != nil:
		logger.Error(err, "reconcile failed, requeuing")
		c.queue.AddRateLimited(key)
	case result.RequeueAfter > 0:
		c.queue.Forget(key)
		c.queue.AddAfter(key, result.RequeueAfter)
	default:
		c.queue.Forget(key)
	}
	return true
}
