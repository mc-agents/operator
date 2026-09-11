package apiclient

import (
	"context"
	"fmt"
	"time"

	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/runtime/serializer"
	"k8s.io/apimachinery/pkg/watch"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/cache"

	"github.com/mc-agents/operator/api/v1alpha1"
)

type Object interface {
	runtime.Object
	metav1.Object
}

type Resource[T Object] interface {
	Get(ctx context.Context, namespace, name string) (T, error)
	List(ctx context.Context, namespace string, opts metav1.ListOptions) ([]T, error)
	Create(ctx context.Context, obj T) (T, error)
	Update(ctx context.Context, obj T) (T, error)
	UpdateStatus(ctx context.Context, obj T) (T, error)
	Delete(ctx context.Context, namespace, name string, opts metav1.DeleteOptions) error
	NewInformer(namespace string, resync time.Duration) cache.SharedIndexInformer
}

type Interface interface {
	Bots() Resource[*v1alpha1.MinecraftBot]
	Pools() Resource[*v1alpha1.MinecraftBotPool]
}

type clientSet struct {
	bots  Resource[*v1alpha1.MinecraftBot]
	pools Resource[*v1alpha1.MinecraftBotPool]
}

func New(cfg *rest.Config) (Interface, error) {
	scheme := runtime.NewScheme()
	if err := v1alpha1.AddToScheme(scheme); err != nil {
		return nil, fmt.Errorf("register scheme: %w", err)
	}

	restClient, err := newRESTClient(cfg, v1alpha1.SchemeGroupVersion, scheme)
	if err != nil {
		return nil, err
	}
	codec := runtime.NewParameterCodec(scheme)

	return &clientSet{
		bots: &restResource[*v1alpha1.MinecraftBot]{
			client: restClient, codec: codec, resource: "minecraftbots",
			newObj:  func() *v1alpha1.MinecraftBot { return &v1alpha1.MinecraftBot{} },
			newList: func() runtime.Object { return &v1alpha1.MinecraftBotList{} },
		},
		pools: &restResource[*v1alpha1.MinecraftBotPool]{
			client: restClient, codec: codec, resource: "minecraftbotpools",
			newObj:  func() *v1alpha1.MinecraftBotPool { return &v1alpha1.MinecraftBotPool{} },
			newList: func() runtime.Object { return &v1alpha1.MinecraftBotPoolList{} },
		},
	}, nil
}

func (c *clientSet) Bots() Resource[*v1alpha1.MinecraftBot] { return c.bots }

func (c *clientSet) Pools() Resource[*v1alpha1.MinecraftBotPool] { return c.pools }

func newRESTClient(cfg *rest.Config, gv schema.GroupVersion, scheme *runtime.Scheme) (rest.Interface, error) {
	c := rest.CopyConfig(cfg)
	c.GroupVersion = &gv
	c.APIPath = "/apis"
	c.NegotiatedSerializer = serializer.NewCodecFactory(scheme).WithoutConversion()
	if c.UserAgent == "" {
		c.UserAgent = rest.DefaultKubernetesUserAgent()
	}
	client, err := rest.RESTClientFor(c)
	if err != nil {
		return nil, fmt.Errorf("build rest client for %s: %w", gv, err)
	}
	return client, nil
}

type restResource[T Object] struct {
	client   rest.Interface
	codec    runtime.ParameterCodec
	resource string
	newObj   func() T
	newList  func() runtime.Object
}

func (r *restResource[T]) Get(ctx context.Context, namespace, name string) (T, error) {
	out := r.newObj()
	err := r.client.Get().
		NamespaceIfScoped(namespace, namespace != "").
		Resource(r.resource).
		Name(name).
		Do(ctx).
		Into(out)
	if err != nil {
		var zero T
		return zero, err
	}
	return out, nil
}

func (r *restResource[T]) List(ctx context.Context, namespace string, opts metav1.ListOptions) ([]T, error) {
	list := r.newList()
	err := r.client.Get().
		NamespaceIfScoped(namespace, namespace != "").
		Resource(r.resource).
		VersionedParams(&opts, r.codec).
		Do(ctx).
		Into(list)
	if err != nil {
		return nil, err
	}
	raw, err := meta.ExtractList(list)
	if err != nil {
		return nil, fmt.Errorf("extract %s list: %w", r.resource, err)
	}
	items := make([]T, 0, len(raw))
	for _, obj := range raw {
		typed, ok := obj.(T)
		if !ok {
			return nil, fmt.Errorf("unexpected item type %T in %s list", obj, r.resource)
		}
		items = append(items, typed)
	}
	return items, nil
}

func (r *restResource[T]) Create(ctx context.Context, obj T) (T, error) {
	out := r.newObj()
	err := r.client.Post().
		NamespaceIfScoped(obj.GetNamespace(), obj.GetNamespace() != "").
		Resource(r.resource).
		Body(obj).
		Do(ctx).
		Into(out)
	if err != nil {
		var zero T
		return zero, err
	}
	return out, nil
}

func (r *restResource[T]) Update(ctx context.Context, obj T) (T, error) {
	out := r.newObj()
	err := r.client.Put().
		NamespaceIfScoped(obj.GetNamespace(), obj.GetNamespace() != "").
		Resource(r.resource).
		Name(obj.GetName()).
		Body(obj).
		Do(ctx).
		Into(out)
	if err != nil {
		var zero T
		return zero, err
	}
	return out, nil
}

func (r *restResource[T]) UpdateStatus(ctx context.Context, obj T) (T, error) {
	out := r.newObj()
	err := r.client.Put().
		NamespaceIfScoped(obj.GetNamespace(), obj.GetNamespace() != "").
		Resource(r.resource).
		Name(obj.GetName()).
		SubResource("status").
		Body(obj).
		Do(ctx).
		Into(out)
	if err != nil {
		var zero T
		return zero, err
	}
	return out, nil
}

func (r *restResource[T]) Delete(ctx context.Context, namespace, name string, opts metav1.DeleteOptions) error {
	return r.client.Delete().
		NamespaceIfScoped(namespace, namespace != "").
		Resource(r.resource).
		Name(name).
		Body(&opts).
		Do(ctx).
		Error()
}

func (r *restResource[T]) NewInformer(namespace string, resync time.Duration) cache.SharedIndexInformer {
	lw := &cache.ListWatch{
		ListFunc: func(opts metav1.ListOptions) (runtime.Object, error) {
			list := r.newList()
			err := r.client.Get().
				NamespaceIfScoped(namespace, namespace != "").
				Resource(r.resource).
				VersionedParams(&opts, r.codec).
				Do(context.Background()).
				Into(list)
			return list, err
		},
		WatchFunc: func(opts metav1.ListOptions) (watch.Interface, error) {
			opts.Watch = true
			return r.client.Get().
				NamespaceIfScoped(namespace, namespace != "").
				Resource(r.resource).
				VersionedParams(&opts, r.codec).
				Watch(context.Background())
		},
	}
	return cache.NewSharedIndexInformer(lw, r.newObj(), resync,
		cache.Indexers{cache.NamespaceIndex: cache.MetaNamespaceIndexFunc})
}
