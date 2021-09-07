# Set up `transflect` in local cluster

Pre-requisites: git, curl, docker (tested with version 20.10) Other tools are
self-bootstrapped with [hermit](https://cashapp.github.io/hermit/). Activate
hermit with

    . ./bin/activate-hermit

## First time setup

Clone this repo, activate hermit and run first time setup

    . ./bin/activate-hermit
    make setup

`make setup` requires your root password to update `/etc/hosts` with an entry
for the local Docker registry used by the k3d cluster.

## Create a local cluster

Setup lightweight, local [`k3d`](https://k3d.io/) Kubernetes cluster with

    make cluster-create

you can inspect your bare-bones local transflect cluster with

    $ k3d node list
    NAME                            ROLE           CLUSTER      STATUS
    k3d-registry.transflect.local   registry                    running
    k3d-transflect-server-0         server         transflect   running
    k3d-transflect-serverlb         loadbalancer   transflect   running

    $ kubectl get deployments --all-namespaces
    NAMESPACE      NAME                     READY   UP-TO-DATE   AVAILABLE   AGE
    kube-system    local-path-provisioner   1/1     1            1           24m
    kube-system    metrics-server           1/1     1            1           24m
    kube-system    coredns                  1/1     1            1           24m
    istio-system   istiod                   1/1     1            1           23m
    istio-system   istio-ingressgateway     1/1     1            1           23m

## Add demo Kubernetes deployment `guppyecho`

Add the prepared gRPC echo service deployment with

    kubectl apply -f deployment/guppyecho.yaml

you can inspect pods with

    $ kubectl get pods -n guppyecho
    NAME                         READY   STATUS    RESTARTS   AGE
    guppyecho-54db9d96c8-89qr9   2/2     Running   0          91s
    guppyecho-54db9d96c8-cw9cl   2/2     Running   0          90s
    guppyecho-54db9d96c8-2r5vg   2/2     Running   0          90s

and gRPC service with

    $ grpcurl --authority "guppyecho.local" -plaintext 127.0.0.1:80 list
    echo.Echo
    grpc.reflection.v1alpha.ServerReflection

    $ grpcurl --authority "guppyecho.local" -plaintext 127.0.0.1:80 describe
    ...

Notice that at this stage no HTTP/JSON endpoints or `EnvoyFilter` resources are
_not yet_ available

    $ curl http://127.0.0.1/api/echo/hello -H "Host: guppyecho.local" -d '{"message": "👋"}'
    /api/echo/hello: not Implemented

    $ kubectl get envoyfilter -n guppyecho
    No resources found in guppyecho namespace.

Aside: the HTTP/JSON path `/api/echo/hello` is defined in
[echo.proto](https://github.com/juliaogris/guppy/blob/v0.0.6/protos/echo/echo.proto#L11).

## Add Kubernetes deployment for `transflect`

Apply transflect deployment

    kubectl apply -f deployment/transflect.yaml

and watch the logs with

    kubectl logs -l app=transflect -n transflect

## Test HTTP/JSON API

You can now successfully call the above `curl` command and find the
transflect-created `EnvoyFilter` resource

$ curl http://127.0.0.1/api/echo/hello -H "Host: guppyecho.local" -d
'{"message": "👋"}' {"response":"And to you: 👋"}

    $ kubectl get envoyfilter -n guppyecho
    NAME                   AGE
    guppyecho-transflect   17m

## Cleanup

Remove the entire local cluster with `make cluster-delete` or reduce it to the
initial bare-bones installation with `make cluster-clean`.

## Further reading

- `Local cluster` section in the [Makefile](Makefile)
- Kubernetes deployments in [deployment/](deployment/) directory (apps and
  transflect)
- Integration tests in [cluster-test.sh](cluster-test.sh)
- Sample Istio EnvoyFilter resource
  [istio/envoy-filter.yaml](istio/envoy-filter.yaml)
