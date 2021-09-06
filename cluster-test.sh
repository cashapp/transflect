#!/bin/bash
#
# Run transflect integration against a local k3d cluster that must have
# been already created with:
#
#     make add-registry-host  # first time only; updates /etc/hosts, requires root.
#     make create-cluster
#
# After running this test script the cluster can be cleanup with
#
#     make cluster-clean
#
# and fully deleted with
#
#     make cluster-delete
#
# A full testrun with setup and tear-down can be run with
#
#     make cluster-test

set -euo pipefail

main() {
    rguide_v1_url=http://127.0.0.1/api/rguide.RouteGuide/GetFeature
    rguide_v2_url=http://127.0.0.1/api/rguide.RouteGuide/GetDefaultFeature
    rguide_v2=v0.0.4
    echo_url=http://127.0.0.1/api/echo/hello
    attempts=25

    # Set up initial deployments and transflect
    # kubectl create namespace transflect # make run-local-operator needs transflect namespace for lease lock
    kubectl apply -f deployment/transflect.yaml
    kubectl apply -f deployment/routeguide.yaml
    kubectl apply -f deployment/guppyecho.yaml

    # Ensure EnvoyFilters are created as expected and expose HTTP/JSON endpoints
    ./out/test-http-transcoding --attempts "${attempts}" --url "${rguide_v1_url}" --host "routeguide.local"
    ./out/test-http-transcoding --attempts "${attempts}" --url "${echo_url}" --host "guppyecho.local"

    # Ensure EnvoyFilter gets updated on Replicaset change
    # test routeguide v2 endpoint isn't present
    ./out/test-http-transcoding --attempts 1 --url "${rguide_v2_url}" --host "routeguide.local" && exit 1
    kubectl set image deployment/routeguide -n routeguide routeguide=julia/routeguide:"${rguide_v2}"
    ./out/test-http-transcoding --attempts 40 --url "${rguide_v1_url}" --host "routeguide.local" --dur 250ms --poll-until-error --error-threshold 3
    ./out/test-http-transcoding --attempts "${attempts}" --url "${rguide_v2_url}" --host "routeguide.local"

    # Ensure EnvoyFilter gets deleted when transflect/port annotation is invalidated on deployment
    kubectl get envoyfilter guppyecho-transflect -n guppyecho
    kubectl annotate deployment guppyecho transflect.cash.squareup.com/port=-1 --overwrite -n guppyecho
    echo 'Expecting: "routeguide-transflect" not found'
    ./out/retry-until-fail 5 5s kubectl get envoyfilter guppyecho-transflect -n guppyecho

    # Ensure EnvoyFilter gets deleted when transflect/port annotation is invalidated while transflect is offline
    kubectl delete -f deployment/transflect.yaml
    kubectl get envoyfilter routeguide-transflect -n routeguide
    kubectl annotate deployment routeguide transflect.cash.squareup.com/port=-1 --overwrite -n routeguide
    kubectl apply -f deployment/transflect.yaml
    ./out/retry-until-fail 5 5s kubectl get envoyfilter routeguide-transflect -n routeguide
}

# Only run main if executed as a script and not "sourced".
if [[ "${BASH_SOURCE[0]}" == "$0" ]]; then main "$@"; fi
