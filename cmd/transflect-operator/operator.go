package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"sync"
	"time"

	"github.com/cashapp/transflect/pkg/transflect"
	"github.com/jpillora/backoff"
	"github.com/pkg/errors"
	"github.com/rs/zerolog/log"
	istionet "istio.io/client-go/pkg/apis/networking/v1alpha3"
	istio "istio.io/client-go/pkg/clientset/versioned/typed/networking/v1alpha3"
	appsv1 "k8s.io/api/apps/v1"
	k8errors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/informers"
	informerv1 "k8s.io/client-go/informers/apps/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/cache"
	"k8s.io/client-go/tools/clientcmd"
	"k8s.io/client-go/util/homedir"
	"k8s.io/client-go/util/workqueue"
)

type operator struct {
	k8s            *kubernetes.Clientset
	istio          *istio.NetworkingV1alpha3Client
	rsInformer     informerv1.ReplicaSetInformer
	deployInformer informerv1.DeploymentInformer
	queue          workqueue.RateLimitingInterface
	stopper        chan struct{}
	wg             sync.WaitGroup
	// activeState records the deployment revision and its grpc port
	// for which an EnvoyFilter has been created. The port is required
	// to update the EnvoyFilter on port annotation change.
	//
	// 	    activeState[deploymentKey] = { revision, grpcPort }
	activeState sync.Map

	// deploymentLocker synchronises operations on a deployment
	// so that no two updates for a single deployment can run concurrently.
	deploymentLocker transflect.MutexMap

	useIngress bool
	plaintext  bool
	address    string
	version    string
}

type activeEntry struct {
	revision int
	grpcPort uint32
}

func newOperator(cfg *config) (*operator, error) {
	k8s, istio, err := getClientSets()
	if err != nil {
		return nil, err
	}

	op := &operator{
		k8s:   k8s,
		istio: istio,

		useIngress: cfg.UseIngress,
		plaintext:  cfg.Plaintext,
		address:    cfg.Address,
		version:    "transflect-" + version,
	}
	return op, nil
}

func watchErrorHandler(_ *cache.Reflector, err error) {
	log.Debug().Err(err).Msg("ListAndWatch dropped the connection with an error, back-off and retry")
}

func (o *operator) start() error {
	// Initialise activeState for existing transflect EnvoyFilters
	// to determine if EnvoyFilter upsert should be processed.
	if err := o.syncActive(); err != nil {
		return fmt.Errorf("cannot start operator: %w", err)
	}

	// Initialise work-queue and stopper channel
	o.queue = workqueue.NewRateLimitingQueue(workqueue.DefaultControllerRateLimiter())
	o.stopper = make(chan struct{})

	// Initialise informers
	informerFactory := informers.NewSharedInformerFactory(o.k8s, time.Second*30)
	o.rsInformer = informerFactory.Apps().V1().ReplicaSets()
	if err := o.rsInformer.Informer().SetWatchErrorHandler(watchErrorHandler); err != nil {
		return errors.Wrap(err, "cannot add custom error handler to replicaset informer")
	}
	o.rsInformer.Informer().AddEventHandler(cache.ResourceEventHandlerFuncs{
		AddFunc:    o.enqueue,
		UpdateFunc: o.enqueueWithOld,
	})
	o.deployInformer = informerFactory.Apps().V1().Deployments()
	if err := o.deployInformer.Informer().SetWatchErrorHandler(watchErrorHandler); err != nil {
		return errors.Wrap(err, "cannot add custom error handler to deployment informer")
	}
	o.deployInformer.Informer().AddEventHandler(cache.ResourceEventHandlerFuncs{
		DeleteFunc: o.cleanupDeployment,
	})
	log.Debug().Msg("Start leading: informer event handlers registered")

	// Run workers and informers
	o.runWorkers(42)
	o.rsInformer.Informer().Run(o.stopper)
	o.deployInformer.Informer().Run(o.stopper)

	// When run finishes e.g. because of signal calling o.stop():
	log.Debug().Msg("Shutting down queue")
	o.queue.ShutDown()
	return nil
}

func (o *operator) syncActive() error {
	opts := metav1.ListOptions{
		LabelSelector: "app=transflect",
		Limit:         42,
	}
	b := backoff.Backoff{Min: 2 * time.Second}
	ctx := context.Background()
	for {
		list, err := o.istio.EnvoyFilters("").List(ctx, opts)
		if err != nil {
			if int(b.Attempt()) > 10 {
				return fmt.Errorf("cannot list existing EnvoyFilters to sync active state, ran out of attempts")
			}
			time.Sleep(b.Duration())
			continue
		}
		b.Reset()
		for _, filter := range list.Items {
			key, entry, err := getActiveEntry(filter)
			if err != nil {
				log.Error().Err(err).Str("envoyfilter", filter.Name).Msg("Cannot be synced")
				continue
			}
			o.activeState.Store(key, entry)
			log.Debug().Str("deploymentKey", key).Int("revision", entry.revision).Uint32("port", entry.grpcPort).Msg("synced active state")
		}
		if opts.Continue == "" {
			return nil
		}
	}
}

func getActiveEntry(filter istionet.EnvoyFilter) (string, activeEntry, error) {
	a := filter.Annotations
	key := a["transflect.cash.squareup.com/deployment"]
	if key == "" {
		return "", activeEntry{}, fmt.Errorf("cannot retrieve deployment key from existing EnvoyFilter")
	}
	port := grpcPortStr(filter.Annotations["transflect.cash.squareup.com/port"])
	if port == 0 {
		return "", activeEntry{}, fmt.Errorf("cannot retrieve grpc port from existing EnvoyFilter")
	}
	revision := deployRevisionStr(filter.Annotations["deployment.kubernetes.io/revision"])
	if revision == 0 {
		return "", activeEntry{}, fmt.Errorf("cannot retrieve deployment revision from existing EnvoyFilter")
	}
	entry := activeEntry{grpcPort: port, revision: revision}
	return key, entry, nil
}

func (o *operator) enqueue(v interface{}) {
	rs, ok := v.(*appsv1.ReplicaSet)
	if !ok {
		log.Error().Interface("object", v).Msg("Cannot convert object to Replicaset for enqueueing")
		return
	}
	if !shouldEnqueue(rs) {
		return
	}
	key, _ := cache.MetaNamespaceKeyFunc(rs)
	o.queue.Add(key)
}

func (o *operator) enqueueWithOld(_, v interface{}) {
	o.enqueue(v)
}

func shouldEnqueue(rs *appsv1.ReplicaSet) bool {
	if rs.Status.ReadyReplicas == 0 {
		return false
	}
	if _, ok := getDeploymentKey(rs); !ok {
		log.Debug().Str("replica", rs.Name).Msg("Replicaset does not have deployment owner")
		return false
	}
	return true
}

func (o *operator) cleanupDeployment(v interface{}) {
	d, ok := v.(*appsv1.Deployment)
	if !ok {
		log.Error().Interface("object", v).Msg("Cannot convert object to Deployment for cleanup")
		return
	}
	key, err := cache.MetaNamespaceKeyFunc(d)
	if err != nil {
		log.Error().Err(err).Str("deployment", d.Name).Msg("Cannot create deployment Key, skip EnvoyFilter cleanup")
		return
	}
	o.activeState.Delete(key)
	o.deploymentLocker.Remove(key)
}

func (o *operator) runWorkers(cnt int) {
	for i := 0; i < cnt; i++ {
		o.wg.Add(1)
		go o.runWorker()
	}
}

func (o *operator) runWorker() {
	for o.next() { // process next enqueued Replicaset
	}
	o.wg.Done()
}

func (o *operator) stop() {
	close(o.stopper)
	o.wg.Wait()
	log.Debug().Msg("All workers have finished")
}

func (o *operator) next() bool {
	key, shutdown := o.queue.Get()
	if shutdown {
		return false
	}
	defer o.queue.Done(key)
	rsName, ok := key.(string)
	if !ok {
		log.Error().Interface("key", key).Msg("Invalid key type, expected string")
		return true
	}
	rs, err := o.getReplicaset(rsName)
	if err != nil {
		log.Error().Err(err).Msg("Cannot get next queued Replicaset")
		return true
	}
	if err := o.processFilter(rs); err != nil {
		log.Error().Err(err).Str("replica", rs.Name).Msg("Cannot process EnvoyFilter for Replicaset")
	}
	return true
}

func (o *operator) processFilter(rs *appsv1.ReplicaSet) error {
	deployKey, _ := getDeploymentKey(rs)
	o.deploymentLocker.Lock(deployKey)
	defer o.deploymentLocker.Unlock(deployKey)
	if !o.shouldProcess(rs) {
		return nil
	}
	port := grpcPort(rs)
	if port == 0 {
		if err := o.deleteFilter(context.Background(), rs); err != nil {
			if !k8errors.IsNotFound(err) {
				return err
			}
			log.Warn().Err(err).Str("replica", rs.Name).Msg("Cannot delete EnvoyFilter because it cannot be found")
		}
		o.activeState.Delete(deployKey)
		o.deploymentLocker.Remove(deployKey)
		return nil
	}

	if err := o.upsertFilter(context.Background(), rs); err != nil {
		return err
	}
	revision := deployRevision(rs)
	active := activeEntry{grpcPort: port, revision: revision}
	o.activeState.Store(deployKey, active)
	return nil
}

func (o *operator) shouldProcess(rs *appsv1.ReplicaSet) bool {
	if rs.Status.ReadyReplicas == 0 {
		return false
	}
	deployKey, _ := getDeploymentKey(rs)
	port := grpcPort(rs)
	v, existing := o.activeState.Load(deployKey)
	if existing { // EnvoyFilter for given deployment exists
		active, _ := v.(activeEntry)
		// Ignore candidate if we have an existing filter but the candidate is
		// old or has not changed the port.
		revision := deployRevision(rs)
		if revision == 0 {
			log.Error().Str("replica", rs.Name).Msg("Cannot get revision annotation for candidate evaluation")
			return false
		}
		if revision < active.revision {
			return false
		}
		// Potential race condition if not performed under deployment
		// lock and port is changed several times in a short period of time.
		if revision == active.revision && port == active.grpcPort {
			return false
		}
	}
	if !existing && port == 0 {
		// No existing record, so nothing to do if not annotated
		return false
	}
	// Upsert or delete EnvoyFilter for Replicaset
	return true
}

func (o *operator) getReplicaset(key string) (*appsv1.ReplicaSet, error) {
	namespace, name, err := cache.SplitMetaNamespaceKey(key)
	if err != nil {
		return nil, errors.Wrapf(err, "invalid key format")
	}
	rs, err := o.rsInformer.Lister().ReplicaSets(namespace).Get(name)
	if err != nil {
		return nil, errors.Wrapf(err, "cannot GET replicaset %s", name)
	}
	return rs, nil
}

func getClientSets() (*kubernetes.Clientset, *istio.NetworkingV1alpha3Client, error) {
	cfg, err := getConfig()
	if err != nil {
		return nil, nil, errors.Wrap(err, "cannot get Kubernetes config")
	}
	k8s, err := kubernetes.NewForConfig(cfg)
	if err != nil {
		return nil, nil, errors.Wrap(err, "cannot get Kubernetes ClientSet")
	}

	istioClient, err := istio.NewForConfig(cfg)
	if err != nil {
		return nil, nil, errors.Wrap(err, "cannot get Istio ClientSet")
	}
	return k8s, istioClient, nil
}

func grpcPort(rs *appsv1.ReplicaSet) uint32 {
	return grpcPortStr(rs.Annotations["transflect.cash.squareup.com/port"])
}

func grpcPortStr(s string) uint32 {
	i, err := strconv.Atoi(s)
	if err != nil || i < 0 || i > 65535 {
		return 0
	}
	return uint32(i)
}

func deployRevision(rs *appsv1.ReplicaSet) int {
	return deployRevisionStr(rs.Annotations["deployment.kubernetes.io/revision"])
}

func deployRevisionStr(s string) int {
	i, err := strconv.Atoi(s)
	if err != nil || i < 0 {
		return 0
	}
	return i
}

func getDeploymentKey(rs *appsv1.ReplicaSet) (string, bool) {
	d, ok := getDeployment(rs)
	if !ok {
		return "", false
	}
	if rs.Namespace == "" {
		return d.Name, true
	}
	return rs.Namespace + "/" + d.Name, true
}

func getDeployment(rs *appsv1.ReplicaSet) (metav1.OwnerReference, bool) {
	for _, owner := range rs.GetOwnerReferences() {
		if owner.Kind == "Deployment" {
			return owner, true
		}
	}
	return metav1.OwnerReference{}, false
}

func getConfig() (*rest.Config, error) {
	cfg, err := rest.InClusterConfig()
	if err == nil {
		return cfg, nil
	}
	if !errors.Is(err, rest.ErrNotInCluster) {
		return nil, errors.Wrap(err, "cannot get kubeconfig")
	}
	// Not running in the cluster. Find KUBECONFIG
	kc := os.Getenv("KUBECONFIG")
	if kc == "" {
		home := homedir.HomeDir()
		if home == "" {
			return nil, errors.New("cannot find kubeconfig. Set KUBECONFIG env var")
		}
		kc = filepath.Join(home, ".kube", "config")
	}
	kubeCfg, err := clientcmd.BuildConfigFromFlags("", kc)
	if err != nil {
		return nil, errors.Wrap(err, "cannot find kube config")
	}
	return kubeCfg, nil
}
