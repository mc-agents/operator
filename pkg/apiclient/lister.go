package apiclient

import (
	"fmt"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/tools/cache"
)

type Lister[T Object] interface {
	Get(namespace, name string) (T, error)
	List(namespace string, selector labels.Selector) ([]T, error)
}

type indexerLister[T Object] struct {
	indexer  cache.Indexer
	resource schema.GroupResource
}

func NewLister[T Object](indexer cache.Indexer, resource schema.GroupResource) Lister[T] {
	return &indexerLister[T]{indexer: indexer, resource: resource}
}

func (l *indexerLister[T]) Get(namespace, name string) (T, error) {
	var zero T
	key := name
	if namespace != "" {
		key = namespace + "/" + name
	}
	obj, exists, err := l.indexer.GetByKey(key)
	if err != nil {
		return zero, err
	}
	if !exists {
		return zero, apierrors.NewNotFound(l.resource, name)
	}
	typed, ok := obj.(T)
	if !ok {
		return zero, fmt.Errorf("cache holds %T for %s/%s", obj, l.resource, key)
	}
	return typed, nil
}

func (l *indexerLister[T]) List(namespace string, selector labels.Selector) ([]T, error) {
	if selector == nil {
		selector = labels.Everything()
	}
	var items []T
	appendMatching := func(obj any) {
		typed, ok := obj.(T)
		if !ok {
			return
		}
		if selector.Matches(labels.Set(typed.GetLabels())) {
			items = append(items, typed)
		}
	}
	if namespace == "" {
		for _, obj := range l.indexer.List() {
			appendMatching(obj)
		}
		return items, nil
	}
	objs, err := l.indexer.ByIndex(cache.NamespaceIndex, namespace)
	if err != nil {
		return nil, err
	}
	for _, obj := range objs {
		appendMatching(obj)
	}
	return items, nil
}
