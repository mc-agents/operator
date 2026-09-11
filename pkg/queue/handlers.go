package queue

import (
	"fmt"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/cache"
	"k8s.io/klog/v2"
)

func EnqueueSelf(e Enqueuer) cache.ResourceEventHandler {
	return cache.ResourceEventHandlerFuncs{
		AddFunc:    func(obj any) { enqueueObject(e, obj) },
		UpdateFunc: func(_, obj any) { enqueueObject(e, obj) },
		DeleteFunc: func(obj any) { enqueueObject(e, obj) },
	}
}

func EnqueueController(e Enqueuer, ownerKind string) cache.ResourceEventHandler {
	return cache.ResourceEventHandlerFuncs{
		AddFunc:    func(obj any) { enqueueOwner(e, obj, ownerKind) },
		UpdateFunc: func(_, obj any) { enqueueOwner(e, obj, ownerKind) },
		DeleteFunc: func(obj any) { enqueueOwner(e, obj, ownerKind) },
	}
}

func enqueueObject(e Enqueuer, obj any) {
	meta, ok := accessor(obj)
	if !ok {
		return
	}
	e.Enqueue(types.NamespacedName{Namespace: meta.GetNamespace(), Name: meta.GetName()})
}

func enqueueOwner(e Enqueuer, obj any, ownerKind string) {
	meta, ok := accessor(obj)
	if !ok {
		return
	}
	owner := metav1.GetControllerOf(meta)
	if owner == nil || owner.Kind != ownerKind {
		return
	}
	e.Enqueue(types.NamespacedName{Namespace: meta.GetNamespace(), Name: owner.Name})
}

func accessor(obj any) (metav1.Object, bool) {
	if tombstone, ok := obj.(cache.DeletedFinalStateUnknown); ok {
		obj = tombstone.Obj
	}
	meta, ok := obj.(metav1.Object)
	if !ok {
		klog.Background().V(2).Info("dropping event for object without metadata", "type", fmt.Sprintf("%T", obj))
		return nil, false
	}
	return meta, true
}
