# Transflect [![CI/CD](https://github.com/cashapp/transflect/workflows/ci/cd/badge.svg?branch=master)](https://github.com/cashapp/transflect/actions?query=workflow%3ACI%2FCD+branch%3Amaster) [![Slack chat](https://img.shields.io/badge/slack-gophers-795679?logo=slack)](https://gophers.slack.com/messages/cashapp)

Transflect is a **Kubernetes operator** that uses **Istio** to set up
Envoy's [gRPC-JSON transcoding](https://www.envoyproxy.io/docs/envoy/latest/configuration/http/http_filters/grpc_json_transcoder_filter),
mapping HTTP/JSON APIs to gRPC services. The transcoding is applied to
Kubernetes deployments with transflect-specific annotations. [gRPC server
reflection](https://github.com/grpc/grpc/blob/master/doc/server-reflection.md) is required.

Transflect creates a **HTTP/JSON** or **REST API** that is fully defined
in the `.proto` files of the gRPC service, similar to how
[Google Cloud Endpoints](https://cloud.google.com/endpoints/docs/grpc/transcoding) define their
HTTP/JSON APIs.

## What problem does transflect solve?

Transflect moves HTTP/JSON to gRPC mapping into the service mesh, just
as other common (micro-)service requirements like mTLS or observability
have been moved out of the application logic into the cluster
infrastructure. Benefits include lower developer effort, better
decoupling, consistent results across application frameworks and
languages.

## Transcoding in Action

You can try out transflect locally without Kubernetes or Istio.
Transflect can generate a gRPC-JSON transcoding Envoy configuration for
an Envoy proxy run in front of your local gRPC service.

_Pre-requisites:_ git, curl, docker (tested with version 20.10). Other
 tools, including Envoy, are bootstrapped with [hermit](https://cashapp.github.io/hermit/).

Clone this repo and build transflect

	. ./bin/activate-hermit
	make install

Start a gRPC server with [gRPC server reflection](https://github.com/grpc/grpc/blob/master/doc/server-reflection.md)

	make run-testserver # starts a gRPC server on :9090, use your command instead

In a second terminal generate the Envoy config and start the Envoy proxy

	transflect --plaintext --format envoy --http-port 9999 localhost:9090 envoy.yaml
	envoy -c envoy.yaml

In a third terminal test the HTTP/JSON API

	curl localhost:9999/api/echo/hello -d '{"message": "👋"}'

with expected output

	{ "response": "And to you: 👋" }

## Usage

### Annotate your `.proto` files

Use the `google.api.http` option to annotate gRPC service methods with
their HTTP/JSON API mappings

```proto
rpc GetShelf(GetShelfRequest) returns (Shelf) {
  option (google.api.http) = { get: "/v1/shelves/{shelf}" };
}

message GetShelfRequest {
  int64 shelf = 1;
}
```

If there is no annotation specified transflect auto-generates
`{post: "/api/<pkg>.<service>/<method>"}`.
See [google/api/http.proto](https://github.com/googleapis/googleapis/blob/master/google/api/http.proto) for more details on http annotations.

### Annotate your deployment

Annotate your deployment with `transflect.cash.squareup.com/port: "<gRPC-port>"`
to tell transflect to set up transcoding

```yaml
apiVersion: apps/v1
kind: Deployment
metadata:
  name: your-deployment
  annotations:
    transflect.cash.squareup.com/port: "9090"
# ...
```

You will need transflect running in your cluster for the annotation to
have an effect. See [deployment/transflect.yaml](deployment/transflect.yaml)
for a sample transflect Deployment used in integration tests.

## Local development

_Pre-requisites:_ git, curl, docker (tested with version 20.10). Other
 tools are bootstrapped with [hermit](https://cashapp.github.io/hermit/).

Activate your hermit environment with

	. ./bin/activate-hermit

For a first time setup of the local `k3d` cluster, your root password is
required. The setup updates `/etc/hosts` with an entry for the local
Docker registry used by the cluster. Run

	make setup

You can build and test locally, run CI integration tests and see make targets with

	make
	make ci
	make help

For a quick-feedback development cycle you can run the transflect
operator on your host machine (e.g. Mac laptop), outside the cluster

	make cluster-create                        # Create lightweight local cluster
	kubectl apply -f deployment/guppyecho.yaml # Add sample gRPC deployment
	make run-local-operator                    # Start transflect on host machine

Use <kbd>Ctrl</kbd> + <kbd>\\</kbd> to instantly shutdown the local
operator with `SIGQUIT`. `SIGINT` will first clean up resources.

Modify the operator sources and re-run `make run-local-operator`.

To clean-up, you can remove the entire local cluster with `make
cluster-delete` or reduce it to the initial bare-bones installation
with `make cluster-clean`.

## Under the hood

Follow the [local cluster guide](docs/local-cluster.md) to set up a
lightweight test cluster with `k3d` and see transflect in action
on Kubernetes. The same setup is used in CI and integration tests.

Read up on the [inner workings of transflect](docs/inner-workings.md) to
better understand which events transflect observes in the Kubernetes
cluster and what resources it creates.

---

Copyright 2021 Square, Inc.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
