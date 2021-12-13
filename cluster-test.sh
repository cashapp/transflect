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

rguide_v1_url=http://127.0.0.1/api/rguide.RouteGuide/GetFeature
rguide_v2_url=http://127.0.0.1/api/rguide.RouteGuide/GetDefaultFeature
rguide_v2=v0.0.4
echo_url=http://127.0.0.1/api/echo/hello
reflection_url=http://127.0.0.1/api/grpc.reflection.v1alpha.ServerReflection/ServerReflectionInfo
attempts=25

main() {
    # Set up initial deployments and transflect
    kubectl apply -f deployment/transflect.yaml
    kubectl apply -f deployment/routeguide.yaml
    kubectl apply -f deployment/guppyecho.yaml

    # Ensure EnvoyFilters are created as expected and expose HTTP/JSON endpoints
    ./out/test-http-transcoding --attempts "${attempts}" --url "${rguide_v1_url}" --host "routeguide.local"
    ./out/test-http-transcoding --attempts "${attempts}" --url "${echo_url}" --host "guppyecho.local"
    ./out/test-http-transcoding --attempts "${attempts}" --url "${reflection_url}" --host "guppyecho.local" --body '[{"list_services": ""}]'
    ./out/test-http-transcoding --attempts "${attempts}" --url "${reflection_url}" --host "routeguide.local" --body '[{"list_services": ""}]'

    # Ensure EnvoyFilter gets updated on Replicaset change
    # test routeguide v2 endpoint isn't present
    ./out/test-http-transcoding --attempts 1 --url "${rguide_v2_url}" --host "routeguide.local" && exit 1
    kubectl set image deployment/routeguide -n routeguide routeguide=julia/routeguide:"${rguide_v2}"
    ./out/test-http-transcoding --attempts 40 --url "${rguide_v1_url}" --host "routeguide.local" --dur 250ms --poll-until-error --error-threshold 3
    ./out/test-http-transcoding --attempts "${attempts}" --url "${rguide_v2_url}" --host "routeguide.local"

    # Ensure services get excluded and included per annotation
    # in both tests we disable the grpc server reflection service
    kubectl annotate deployment guppyecho transflect.cash.squareup.com/exclude-grpc-services="grpc.reflection.v1alpha.ServerReflection" --overwrite -n guppyecho
    kubectl annotate deployment routeguide transflect.cash.squareup.com/include-grpc-services="rguide.RouteGuide" --overwrite -n routeguide
    ./out/test-http-transcoding --attempts "${attempts}" --url "${reflection_url}" --host "guppyecho.local" --body '[{"list_services": ""}]' --dur 1s --poll-until-error --error-threshold 3 && exit 1
    ./out/test-http-transcoding --attempts "${attempts}" --url "${reflection_url}" --host "routeguide.local" --body '[{"list_services": ""}]' --dur 1s --poll-until-error --error-threshold 3 && exit 1

    # Ensure EnvoyFilter gets deleted when transflect/port annotation is invalidated on deployment
    kubectl get envoyfilter guppyecho-transflect -n guppyecho
    kubectl annotate deployment guppyecho transflect.cash.squareup.com/port=-1 --overwrite -n guppyecho
    echo 'Expecting: "routeguide-transflect" not found'
    ./out/retry-until-fail 5 5s kubectl get envoyfilter guppyecho-transflect -n guppyecho

    check_metrics

    # Ensure EnvoyFilter gets deleted when transflect/port annotation is invalidated while transflect is offline
    kubectl delete -f deployment/transflect.yaml
    kubectl get envoyfilter routeguide-transflect -n routeguide
    kubectl annotate deployment routeguide transflect.cash.squareup.com/port=-1 --overwrite -n routeguide
    kubectl apply -f deployment/transflect.yaml
    echo 'expect "Error from server (NotFound)"'
    ./out/retry-until-fail 5 5s kubectl get envoyfilter routeguide-transflect -n routeguide
    echo "Integration tests finished successfully."
}

check_metrics() {
    pod=$(kubectl get lease -n transflect transflect-leader -o jsonpath='{.spec.holderIdentity}')
    kubectl port-forward "pod/${pod}" 9090 -n transflect &
    #shellcheck disable=SC2064
    trap "kill $!" EXIT
    sleep 1
    metrics_got=$(curl -s localhost:9090/metrics | grep "^transflect_" | grep --invert-match "^transflect_ignored_total" | sort)

    metrics_want='transflect_envoyfilters 1
transflect_leader 1
transflect_operations_total{status="success",type="delete"} 1
transflect_operations_total{status="success",type="upsert"} 5
transflect_preprocess_error_total 0'
    if [ "${metrics_want}" != "${metrics_got}" ]; then
        printf "unexpected metrics value\n want:\n%s\n\n got:\n%s\n" "${metrics_want}" "${metrics_got}"
        exit 1
    fi

    metrics_got=$(curl -s localhost:9090/metrics | awk -F'{' '/^workqueue_/ {print $1}' | sort -u)
    metrics_want='workqueue_adds_total
workqueue_depth
workqueue_latency_seconds_bucket
workqueue_latency_seconds_count
workqueue_latency_seconds_sum
workqueue_longest_running_processor_seconds
workqueue_retries_total
workqueue_unfinished_work_seconds
workqueue_work_duration_seconds_bucket
workqueue_work_duration_seconds_count
workqueue_work_duration_seconds_sum'

    if [ "${metrics_want}" != "${metrics_got}" ]; then
        printf "unexpected metrics value\n want:\n%s\n\n got:\n%s\n" "${metrics_want}" "${metrics_got}"
        exit 1
    fi
    echo "metrics test SUCCESS"
}

# Only run main if executed as a script and not "sourced".
if [[ "${BASH_SOURCE[0]}" == "$0" ]]; then main "$@"; fi
