package fake

import (
	"context"
	"fmt"
	"sort"
	"sync"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/cache"

	"github.com/mc-agents/operator/pkg/apiclient"
)

type Resource[T apiclient.Object] struct {
	resource schema.GroupResource

	mu    sync.Mutex
	items map[string]T
	uid   int
}

func NewResource[T apiclient.Object](resource schema.GroupResource) *Resource[T] {
	return &Resource[T]{resource: resource, items: map[string]T{}}
}

func (r *Resource[T]) Seed(objs ...T) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, obj := range objs {
		r.items[key(obj.GetNamespace(), obj.GetName())] = obj
	}
}

func (r *Resource[T]) Names() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	names := make([]string, 0, len(r.items))
	for _, obj := range r.items {
		names = append(names, obj.GetName())
	}
	sort.Strings(names)
	return names
}

func (r *Resource[T]) Get(_ context.Context, namespace, name string) (T, error) {
	return r.lookup(namespace, name)
}

func (r *Resource[T]) List(_ context.Context, namespace string, _ metav1.ListOptions) ([]T, error) {
	return r.list(namespace, labels.Everything())
}

func (r *Resource[T]) Create(_ context.Context, obj T) (T, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	k := key(obj.GetNamespace(), obj.GetName())
	if _, exists := r.items[k]; exists {
		var zero T
		return zero, apierrors.NewAlreadyExists(r.resource, obj.GetName())
	}
	r.uid++
	stored, ok := obj.DeepCopyObject().(T)
	if !ok {
		var zero T
		return zero, fmt.Errorf("deep copy produced %T", obj)
	}
	stored.SetUID(types.UID(fmt.Sprintf("uid-%d", r.uid)))
	r.items[k] = stored
	return stored, nil
}

func (r *Resource[T]) Update(_ context.Context, obj T) (T, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	k := key(obj.GetNamespace(), obj.GetName())
	if _, exists := r.items[k]; !exists {
		var zero T
		return zero, apierrors.NewNotFound(r.resource, obj.GetName())
	}
	stored, ok := obj.DeepCopyObject().(T)
	if !ok {
		var zero T
		return zero, fmt.Errorf("deep copy produced %T", obj)
	}
	r.items[k] = stored
	return stored, nil
}

func (r *Resource[T]) UpdateStatus(ctx context.Context, obj T) (T, error) {
	return r.Update(ctx, obj)
}

func (r *Resource[T]) Delete(_ context.Context, namespace, name string, _ metav1.DeleteOptions) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	k := key(namespace, name)
	if _, exists := r.items[k]; !exists {
		return apierrors.NewNotFound(r.resource, name)
	}
	delete(r.items, k)
	return nil
}

func (r *Resource[T]) NewInformer(string, time.Duration) cache.SharedIndexInformer {
	panic("fake resource has no informer")
}

func (r *Resource[T]) Lister() apiclient.Lister[T] { return listerView[T]{r} }

func (r *Resource[T]) lookup(namespace, name string) (T, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	obj, exists := r.items[key(namespace, name)]
	if !exists {
		var zero T
		return zero, apierrors.NewNotFound(r.resource, name)
	}
	return obj, nil
}

func (r *Resource[T]) list(namespace string, selector labels.Selector) ([]T, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	var items []T
	for _, obj := range r.items {
		if namespace != "" && obj.GetNamespace() != namespace {
			continue
		}
		if !selector.Matches(labels.Set(obj.GetLabels())) {
			continue
		}
		items = append(items, obj)
	}
	sort.Slice(items, func(i, j int) bool { return items[i].GetName() < items[j].GetName() })
	return items, nil
}

type listerView[T apiclient.Object] struct {
	resource *Resource[T]
}

func (l listerView[T]) Get(namespace, name string) (T, error) {
	return l.resource.lookup(namespace, name)
}

func (l listerView[T]) List(namespace string, selector labels.Selector) ([]T, error) {
	if selector == nil {
		selector = labels.Everything()
	}
	return l.resource.list(namespace, selector)
}

func key(namespace, name string) string { return namespace + "/" + name }
