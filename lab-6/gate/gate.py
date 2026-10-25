import datetime
import json
import math
import os
import ssl
import sys
import time
import urllib.error
import urllib.parse
import urllib.request


PROMETHEUS = os.getenv("PROMETHEUS_URL", "http://monitoring-prometheus.monitoring.svc:9090")
NAMESPACE = os.getenv("TARGET_NAMESPACE", "shop")
ROUTE = os.getenv("ROUTE_NAME", "shop-api")
TOKEN_PATH = "/var/run/secrets/kubernetes.io/serviceaccount/token"
CA_PATH = "/var/run/secrets/kubernetes.io/serviceaccount/ca.crt"
API_SERVER = "https://kubernetes.default.svc"
V1 = os.getenv("V1_BACKEND", "shop-api-v1")
V2 = os.getenv("V2_BACKEND", "shop-api-v2")
INTERVAL = 15
WARMUP = 75
STEPS = (40, 70, 100)


def log(weight, error, p95, requests, action):
    print(json.dumps({
        "timestamp": datetime.datetime.now(datetime.timezone.utc).isoformat(),
        "weight": weight,
        "candidate_error": error,
        "candidate_p95_seconds": p95,
        "candidate_requests": requests,
        "action": action,
    }), flush=True)


def prom_query(expression):
    url = PROMETHEUS + "/api/v1/query?" + urllib.parse.urlencode({"query": expression})
    with urllib.request.urlopen(url, timeout=10) as response:
        payload = json.load(response)
    if payload.get("status") != "success":
        raise RuntimeError("Prometheus query failed")
    result = payload.get("data", {}).get("result", [])
    if not result:
        return None
    return float(result[0]["value"][1])


def evaluate():
    base = 'namespace="{}",version="v2",route="/order",method="POST"'.format(NAMESPACE)
    requests = prom_query("sum(increase(http_requests_total{" + base + "}[1m]))")
    errors = prom_query("sum(increase(http_errors_total{" + base + "}[1m]))")
    p95 = prom_query("histogram_quantile(0.95,sum by(le)(increase(http_request_duration_seconds_bucket{" + base + "}[1m])))")
    if requests is None or errors is None or p95 is None:
        return None
    ratio = errors / max(requests, 1)
    if not all(math.isfinite(value) for value in (requests, ratio, p95)):
        return None
    return requests, ratio, p95


def kubernetes_request(path, method, body=None):
    with open(TOKEN_PATH, encoding="utf-8") as token_file:
        token = token_file.read().strip()
    context = ssl.create_default_context(cafile=CA_PATH)
    data = None if body is None else json.dumps(body).encode()
    request = urllib.request.Request(
        API_SERVER + path,
        data=data,
        method=method,
        headers={
            "Authorization": "Bearer " + token,
            "Content-Type": "application/merge-patch+json",
        },
    )
    with urllib.request.urlopen(request, context=context, timeout=10) as response:
        return json.load(response)


def set_weight(v2_weight):
    path = "/apis/gateway.networking.k8s.io/v1/namespaces/{}/httproutes/{}".format(
        urllib.parse.quote(NAMESPACE, safe=""), urllib.parse.quote(ROUTE, safe="")
    )
    current = kubernetes_request(path, "GET")
    refs = current.get("spec", {}).get("rules", [{}])[0].get("backendRefs", [])
    by_name = {ref.get("name"): ref for ref in refs}
    if V1 not in by_name or V2 not in by_name:
        raise RuntimeError("HTTPRoute is missing expected API backends")
    updated = []
    for ref in refs:
        item = dict(ref)
        if item.get("name") == V2:
            item["weight"] = v2_weight
        elif item.get("name") == V1:
            item["weight"] = 100 - v2_weight
        updated.append(item)
    kubernetes_request(path, "PATCH", {"spec": {"rules": [{"backendRefs": updated}]}})


def rollback(action):
    set_weight(0)
    log(0, None, None, None, action)


def main():
    weight = 10
    try:
        set_weight(weight)
        log(weight, None, None, None, "start")
        time.sleep(WARMUP)
        holds = 0
        for step in STEPS:
            while True:
                time.sleep(INTERVAL)
                try:
                    result = evaluate()
                except (urllib.error.URLError, TimeoutError, ValueError, KeyError, RuntimeError) as exc:
                    rollback("rollback_runtime_error")
                    print("gate runtime error: " + type(exc).__name__, file=sys.stderr, flush=True)
                    return 1
                if result is None or result[0] < 30:
                    holds += 1
                    log(weight, None if result is None else result[1], None if result is None else result[2], None if result is None else result[0], "hold")
                    if holds >= 3:
                        rollback("rollback_insufficient_data")
                        return 0
                    continue
                holds = 0
                requests, error, p95 = result
                if error > 0.05 or p95 > 0.2:
                    set_weight(0)
                    log(0, error, p95, requests, "rollback")
                    return 0
                set_weight(step)
                weight = step
                log(weight, error, p95, requests, "advance")
                break
        time.sleep(60)
        result = evaluate()
        if result is None or result[0] < 30:
            rollback("rollback_insufficient_final_data")
            return 0
        requests, error, p95 = result
        if error > 0.05 or p95 > 0.2:
            set_weight(0)
            log(0, error, p95, requests, "rollback")
            return 0
        log(weight, error, p95, requests, "success")
        return 0
    except Exception as exc:
        try:
            set_weight(0)
            log(0, None, None, None, "rollback_runtime_error")
        except Exception as rollback_error:
            print("gate rollback failed: " + type(rollback_error).__name__, file=sys.stderr, flush=True)
        print("gate runtime error: " + type(exc).__name__, file=sys.stderr, flush=True)
        return 1


if __name__ == "__main__":
    sys.exit(main())
