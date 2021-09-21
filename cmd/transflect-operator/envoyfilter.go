package main

import (
	"context"
	"fmt"

	"github.com/cashapp/transflect/pkg/transflect"
	"github.com/gogo/protobuf/types"
	"github.com/pkg/errors"
	"github.com/rs/zerolog/log"
	"google.golang.org/grpc"
	"google.golang.org/protobuf/types/descriptorpb"
	ist "istio.io/api/networking/v1alpha3"
	istionet "istio.io/client-go/pkg/apis/networking/v1alpha3"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	netv1 "k8s.io/api/networking/v1"
	k8errors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func (o *operator) deleteFilter(ctx context.Context, rs *appsv1.ReplicaSet) error {
	name := envoyFilterName(rs)
	opts := metav1.DeleteOptions{}
	if err := o.istio.EnvoyFilters(rs.Namespace).Delete(ctx, name, opts); err != nil {
		if !k8errors.IsNotFound(err) {
			return errors.Wrapf(err, "cannot delete EnvoyFilter %s", name)
		}
		log.Warn().Err(err).Str("replica", rs.Name).Msg("Cannot delete EnvoyFilter because it cannot be found")
	} else {
		log.Debug().Str("replica", rs.Name).Msg("EnvoyFilter deleted")
	}
	filtersGauge.Dec()
	return nil
}

func (o *operator) upsertFilter(ctx context.Context, rs *appsv1.ReplicaSet) error {
	service, err := o.createService(ctx, rs)
	if err != nil {
		return errors.Wrap(err, "cannot create Service")
	}
	defer o.deleteService(ctx, service)
	log.Debug().Str("replica", rs.Name).Msg("Service created")
	if o.useIngress {
		ingress, err := o.createIngress(ctx, rs)
		if err != nil {
			return errors.Wrap(err, "cannot create Ingress")
		}
		defer o.deleteIngress(ctx, ingress)
		log.Debug().Str("replica", rs.Name).Msg("Ingress created")
	}
	addr, opts := o.getAddrOpts(rs)
	ctx = context.WithValue(ctx, ctxReplicaKey, rs.Name)
	fds, services, err := transflect.GetFileDescriptorSet(ctx, addr, opts...)
	if err != nil {
		return errors.Wrapf(err, "cannot query Reflection API or create FileDescriptor for address %s", o.address)
	}
	log.Debug().Str("replica", rs.Name).Strs("grpcServices", services).Msg("Reflection API queried")
	if err := o.upsertEnvoyFilter(ctx, rs, fds, services); err != nil {
		return errors.Wrap(err, "cannot upsert EnvoyFilter resource")
	}
	log.Debug().Str("replica", rs.Name).Msg("EnvoyFilter upserted")
	return nil
}

func (o *operator) createService(ctx context.Context, rs *appsv1.ReplicaSet) (*corev1.Service, error) {
	service := newService(rs)
	name := service.Name
	createOpts := metav1.CreateOptions{}
	result, err := o.k8s.CoreV1().Services(rs.Namespace).Create(ctx, service, createOpts)
	if err == nil {
		return result, nil
	} else if !k8errors.IsAlreadyExists(err) {
		return nil, errors.Wrap(err, "cannot create service")
	}
	log.Debug().Str("replica", rs.Name).Str("name", name).Msg("Service already exists, updating")
	getOpts := metav1.GetOptions{}
	s, err := o.k8s.CoreV1().Services(rs.Namespace).Get(ctx, name, getOpts)
	if err != nil {
		return nil, errors.Wrap(err, "cannot get service to update")
	}
	v := s.ObjectMeta.GetResourceVersion()
	service.ObjectMeta.SetResourceVersion(v)
	service.Spec.ClusterIP = s.Spec.ClusterIP

	updateOpts := metav1.UpdateOptions{}
	result, err = o.k8s.CoreV1().Services(rs.Namespace).Update(ctx, service, updateOpts)
	if err != nil {
		return nil, errors.Wrap(err, "cannot update service")
	}
	return result, nil
}

func newService(rs *appsv1.ReplicaSet) *corev1.Service {
	return &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Namespace: rs.Namespace,
			Name:      rs.Name,
		},
		Spec: corev1.ServiceSpec{
			Selector: rs.Spec.Template.Labels,
			Ports: []corev1.ServicePort{
				{
					Name: "grpc",
					Port: int32(grpcPort(rs)),
				},
			},
		},
	}
}

func (o *operator) deleteService(ctx context.Context, service *corev1.Service) {
	opts := metav1.DeleteOptions{}
	err := o.k8s.CoreV1().Services(service.Namespace).Delete(ctx, service.Name, opts)
	if err != nil {
		log.Error().Err(err).Str("replica", service.Name).Str("name", service.Name).Msg("Cannot delete Service")
	}
}

func (o *operator) createIngress(ctx context.Context, rs *appsv1.ReplicaSet) (*netv1.Ingress, error) {
	ingress := newIngress(rs)
	name := ingress.Name
	opts := metav1.CreateOptions{}

	result, err := o.k8s.NetworkingV1().Ingresses(rs.Namespace).Create(ctx, ingress, opts)
	if err == nil {
		return result, nil
	} else if !k8errors.IsAlreadyExists(err) {
		return nil, errors.Wrap(err, "cannot create ingress")
	}

	log.Debug().Str("replica", rs.Name).Str("name", name).Msg("Ingress already exists, updating")
	getOpts := metav1.GetOptions{}
	i, err := o.k8s.NetworkingV1().Ingresses(rs.Namespace).Get(ctx, name, getOpts)
	if err != nil {
		return nil, errors.Wrap(err, "Cannot get ingress to update")
	}
	v := i.ObjectMeta.GetResourceVersion()
	ingress.ObjectMeta.SetResourceVersion(v)

	updateOpts := metav1.UpdateOptions{}
	result, err = o.k8s.NetworkingV1().Ingresses(rs.Namespace).Update(ctx, ingress, updateOpts)
	if err != nil {
		return nil, errors.Wrap(err, "Cannot update ingress to update")
	}
	return result, nil
}

func newIngress(rs *appsv1.ReplicaSet) *netv1.Ingress {
	pathType := netv1.PathTypePrefix
	return &netv1.Ingress{
		ObjectMeta: metav1.ObjectMeta{
			Namespace:   rs.Namespace,
			Name:        rs.Name,
			Annotations: map[string]string{"kubernetes.io/ingress.class": "istio"},
		},
		Spec: netv1.IngressSpec{
			Rules: []netv1.IngressRule{
				{
					Host: rs.Name + ".local",
					IngressRuleValue: netv1.IngressRuleValue{
						HTTP: &netv1.HTTPIngressRuleValue{
							Paths: []netv1.HTTPIngressPath{
								{
									Path:     "/",
									PathType: &pathType,
									Backend: netv1.IngressBackend{
										Service: &netv1.IngressServiceBackend{
											Name: rs.Name,
											Port: netv1.ServiceBackendPort{
												Number: int32(grpcPort(rs)),
											},
										},
									},
								},
							},
						},
					},
				},
			},
		},
	}
}

func (o *operator) deleteIngress(ctx context.Context, ingress *netv1.Ingress) {
	opts := metav1.DeleteOptions{}
	err := o.k8s.NetworkingV1().Ingresses(ingress.Namespace).Delete(ctx, ingress.Name, opts)
	if err != nil {
		log.Error().Err(err).Str("replica", ingress.Name).Str("name", ingress.Name).Msg("Cannot delete Ingress")
	}
}

func (o *operator) upsertEnvoyFilter(ctx context.Context, rs *appsv1.ReplicaSet, fds *descriptorpb.FileDescriptorSet, services []string) error {
	protosetBase64, err := transflect.Base64Proto(fds)
	if err != nil {
		return errors.Wrap(err, "cannot encode proto FileDescriptorSet")
	}
	envoyFilter := newEnvoyFilter(rs, protosetBase64, services, o.version)
	name := envoyFilter.Name

	opts := metav1.CreateOptions{}
	_, err = o.istio.EnvoyFilters(rs.Namespace).Create(ctx, envoyFilter, opts)
	if err == nil {
		filtersGauge.Inc()
		return nil
	} else if !k8errors.IsAlreadyExists(err) {
		return errors.Wrap(err, "cannot upsert EnvoyFilter")
	}

	log.Debug().Str("replica", rs.Name).Str("name", name).Msg("EnvoyFilter already exists, updating")
	getOpts := metav1.GetOptions{}
	e, err := o.istio.EnvoyFilters(rs.Namespace).Get(ctx, name, getOpts)
	if err != nil {
		return errors.Wrap(err, "cannot get EnvoyFilter to update")
	}
	v := e.ObjectMeta.GetResourceVersion()
	envoyFilter.ObjectMeta.SetResourceVersion(v)

	updateOpts := metav1.UpdateOptions{}
	if _, err := o.istio.EnvoyFilters(rs.Namespace).Update(ctx, envoyFilter, updateOpts); err != nil {
		return errors.Wrap(err, "cannot Update envoyFilter")
	}
	return nil
}

func newEnvoyFilter(rs *appsv1.ReplicaSet, protosetBase64 string, services []string, version string) *istionet.EnvoyFilter {
	transcoderV3 := "type.googleapis.com/envoy.extensions.filters.http.grpc_json_transcoder.v3.GrpcJsonTranscoder"
	match := &ist.EnvoyFilter_EnvoyConfigObjectMatch{
		Context: ist.EnvoyFilter_SIDECAR_INBOUND,
		ObjectTypes: &ist.EnvoyFilter_EnvoyConfigObjectMatch_Listener{
			Listener: &ist.EnvoyFilter_ListenerMatch{
				PortNumber: grpcPort(rs),
				FilterChain: &ist.EnvoyFilter_ListenerMatch_FilterChainMatch{
					Filter: &ist.EnvoyFilter_ListenerMatch_FilterMatch{
						Name: "envoy.filters.network.http_connection_manager",
						SubFilter: &ist.EnvoyFilter_ListenerMatch_SubFilterMatch{
							Name: "envoy.filters.http.router",
						},
					},
				},
			},
		},
	}
	patch := &ist.EnvoyFilter_Patch{
		Operation: ist.EnvoyFilter_Patch_INSERT_BEFORE,
		Value: &types.Struct{Fields: map[string]*types.Value{
			"name": stringVal("envoy.filters.http.grpc_json_transcoder"),
			"typed_config": structVal(map[string]*types.Value{
				"@type":                stringVal(transcoderV3),
				"proto_descriptor_bin": stringVal(protosetBase64),
				"services":             listVal(services),
				"print_options": structVal(map[string]*types.Value{
					"add_whitespace":                boolVal(false),
					"always_print_primitive_fields": boolVal(true),
					"always_print_enums_as_ints":    boolVal(false),
					"preserve_proto_field_names":    boolVal(true),
				}),
			}),
		}},
	}
	deployKey, _ := getDeploymentKey(rs)
	return &istionet.EnvoyFilter{
		ObjectMeta: metav1.ObjectMeta{
			Namespace: rs.Namespace,
			Name:      envoyFilterName(rs),
			Labels:    map[string]string{"app": "transflect"},
			Annotations: map[string]string{
				"transflect.cash.squareup.com/version":    version,
				"transflect.cash.squareup.com/replicaset": rs.Name,
				"transflect.cash.squareup.com/deployment": deployKey,
				"transflect.cash.squareup.com/port":       rs.Annotations["transflect.cash.squareup.com/port"],
				"deployment.kubernetes.io/revision":       rs.Annotations["deployment.kubernetes.io/revision"],
			},
			OwnerReferences: envoyFilterOwner(rs), // default: Deployment. On Deployment deletion EnvoyFilter gets deleted automatically.
		},
		Spec: ist.EnvoyFilter{
			WorkloadSelector: &ist.WorkloadSelector{
				Labels: envoyFilterSelector(rs),
			},
			ConfigPatches: []*ist.EnvoyFilter_EnvoyConfigObjectPatch{
				{
					ApplyTo: ist.EnvoyFilter_HTTP_FILTER,
					Match:   match,
					Patch:   patch,
				},
			},
		},
	}
}

func envoyFilterName(rs *appsv1.ReplicaSet) string {
	name := rs.Name
	if deployment, ok := getDeployment(rs); ok {
		name = deployment.Name
	}
	return name + "-transflect"
}

func envoyFilterOwner(rs *appsv1.ReplicaSet) []metav1.OwnerReference {
	if deployment, ok := getDeployment(rs); ok {
		return []metav1.OwnerReference{deployment}
	}
	owner := metav1.OwnerReference{
		APIVersion: rs.APIVersion,
		Name:       rs.Name,
		Kind:       "Replicaset",
		UID:        rs.UID,
	}
	return []metav1.OwnerReference{owner}
}

func envoyFilterSelector(rs *appsv1.ReplicaSet) map[string]string {
	labels := map[string]string{}
	for k, v := range rs.Spec.Template.Labels {
		if k != "pod-template-hash" {
			labels[k] = v
		}
	}
	return labels
}

func stringVal(s string) *types.Value {
	return &types.Value{Kind: &types.Value_StringValue{StringValue: s}}
}

func boolVal(b bool) *types.Value {
	return &types.Value{Kind: &types.Value_BoolValue{BoolValue: b}}
}

func listVal(ss []string) *types.Value {
	v := make([]*types.Value, len(ss))
	for i, s := range ss {
		v[i] = stringVal(s)
	}
	return &types.Value{
		Kind: &types.Value_ListValue{ListValue: &types.ListValue{Values: v}},
	}
}

func structVal(fields map[string]*types.Value) *types.Value {
	return &types.Value{
		Kind: &types.Value_StructValue{StructValue: &types.Struct{Fields: fields}},
	}
}

func (o *operator) getAddrOpts(rs *appsv1.ReplicaSet) (string, []grpc.DialOption) {
	port := grpcPort(rs)
	addr := fmt.Sprintf("%s.%s.svc.cluster.local:%d", rs.Name, rs.Namespace, port)
	var opts []grpc.DialOption
	if o.useIngress {
		addr = o.address
		authority := rs.Namespace + ".local"
		opts = append(opts, grpc.WithAuthority(authority))
	}
	if o.plaintext {
		opts = append(opts, grpc.WithInsecure())
	}
	return addr, opts
}
