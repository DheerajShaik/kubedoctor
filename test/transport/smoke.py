import http.server
import json
import os
import pathlib
import subprocess
import sys
import tempfile
import threading
import urllib.parse

binary_directory, kubectl_directory = sys.argv[1:3]
warning_sentinel = "KUBEDOCTOR_FIXTURE_WARNING_SENTINEL"
private_sentinel = "synthetic-private-literal"
version = {
    "apps": "apps/v1", "autoscaling": "autoscaling/v2", "batch": "batch/v1",
    "discovery.k8s.io": "discovery.k8s.io/v1",
    "networking.k8s.io": "networking.k8s.io/v1",
    "policy": "policy/v1",
}
kinds = {
    "pods": "Pod", "configmaps": "ConfigMap", "events": "Event",
    "persistentvolumeclaims": "PersistentVolumeClaim", "services": "Service",
    "replicasets": "ReplicaSet", "deployments": "Deployment", "daemonsets": "DaemonSet",
    "endpointslices": "EndpointSlice", "ingresses": "Ingress", "networkpolicies": "NetworkPolicy",
    "poddisruptionbudgets": "PodDisruptionBudget", "statefulsets": "StatefulSet",
    "jobs": "Job", "cronjobs": "CronJob", "horizontalpodautoscalers": "HorizontalPodAutoscaler",
    "limitranges": "LimitRange", "resourcequotas": "ResourceQuota",
}
wire = []

class API(http.server.BaseHTTPRequestHandler):
    def do_GET(self):
        path = urllib.parse.urlsplit(self.path).path
        wire.append(path)
        if path == "/apis/discovery.k8s.io/v1":
            self.respond(200, "v1", "APIResourceList", groupVersion="discovery.k8s.io/v1",
                         resources=[{"name": "endpointslices", "namespaced": True, "kind": "EndpointSlice"}])
            return
        tokens = path.strip("/").split("/")
        if "namespaces" in tokens:
            ns_index = tokens.index("namespaces") + 1
            ns = tokens[ns_index]
            if ns in ("payments", "payments-broken"):
                if ns_index == len(tokens) - 1:
                    self.respond(200, "v1", "Namespace", metadata={"name": ns, "uid": "fixture-" + ns})
                    return
                target = tokens[-1]
                if target in kinds and len(tokens) == ns_index + 2:
                    items = []
                    if target == "pods" and ns == "payments-broken":
                        items = [{"metadata": {"name": "checkout", "namespace": ns, "uid": "checkout-uid"},
                                  "spec": {"containers": [{"name": "app", "image": "example.invalid/app",
                                                                "env": [{"name": "FIXTURE", "value": private_sentinel}]}]},
                                  "status": {"phase": "Pending", "containerStatuses": [{
                                      "name": "app", "state": {"waiting": {"reason": "ImagePullBackOff"}},
                                  }]}}]
                    self.respond(200, version.get(tokens[1], "v1"), kinds[target]+"List",
                                 metadata={"resourceVersion": "1"}, items=items)
                    return
            if ns == "kube-system" and tokens[ns_index+1:] == ["pods"]:
                self.respond(200, "v1", "PodList", metadata={"resourceVersion": "1"}, items=[])
                return
        self.respond(404, "v1", "Status", status="Failure", reason="NotFound", code=404)

    def respond(self, http_code, api_version, kind, **fields):
        body = json.dumps(dict(apiVersion=api_version, kind=kind, **fields)).encode()
        self.send_response(http_code)
        self.send_header("Content-Type", "application/json")
        self.send_header("Content-Length", str(len(body)))
        self.send_header("Warning", f'299 smoke "{warning_sentinel}"')
        self.end_headers()
        self.wfile.write(body)

    def log_message(self, *args):
        return

with http.server.ThreadingHTTPServer(("127.0.0.1", 0), API) as server:
    thread = threading.Thread(target=server.serve_forever, daemon=True)
    thread.start()
    try:
        with tempfile.TemporaryDirectory() as temp:
            config = pathlib.Path(temp) / "kubeconfig"
            config.write_text(json.dumps({
                "apiVersion": "v1", "kind": "Config", "current-context": "smoke",
                "contexts": [{"name": "smoke", "context": {"cluster": "mock", "namespace": "payments"}}],
                "clusters": [{"name": "mock", "cluster": {"server": f"http://127.0.0.1:{server.server_port}"}}],
            }))
            env = os.environ | {"PATH": os.pathsep.join([binary_directory, kubectl_directory, os.environ["PATH"]])}
            for namespace, expected in [("payments", 0), ("payments-broken", 1)]:
                scan = subprocess.run(["kubectl", "doctor", "namespace", namespace, "--kubeconfig",
                                       str(config), "--output", "json"], capture_output=True, text=True,
                                      env=env, timeout=40)
                report = json.loads(scan.stdout)
                assert scan.returncode == expected, (scan.returncode, scan.stderr, report)
                assert warning_sentinel not in scan.stdout + scan.stderr
                assert report["complete"] and report["scope"] == {
                    "kind": "namespace", "namespace": namespace, "context": "smoke",
                }, report
                if expected == 1:
                    assert any(f["rule_id"] == "pod-containers" and f["resource"]["name"] == "checkout"
                               for f in report["findings"]), report
                print(f"kubectl doctor namespace {namespace} → exit {scan.returncode}, {len(report['findings'])} findings")
                bundle = pathlib.Path(temp) / f"{namespace}.bundle.json"
                def invoke(*args, kubeconfig=config):
                    return subprocess.run(["kubectl", "doctor", *args, "--kubeconfig", str(kubeconfig)],
                                          capture_output=True, text=True, env=env, timeout=45)

                live = invoke("capture", "namespace", namespace, "--bundle", str(bundle), "-o", "json")
                assert live.returncode == 0, live.stderr
                investigation = json.loads(live.stdout)
                assert investigation["report"]["findings"] == report["findings"]
                assert investigation["cluster_id"], "namespace identity was not captured"
                off = pathlib.Path(temp) / "unusable"
                wire_before = list(wire)
                replay = invoke("replay", str(bundle), "-o", "json", kubeconfig=off)
                assert replay.returncode == 0 and replay.stdout == live.stdout, replay.stderr
                stored = invoke("replay", str(bundle), "--mode", "stored", "-o", "json", kubeconfig=off)
                assert stored.returncode == 0 and json.loads(stored.stdout) == investigation["report"]
                html = invoke("investigate", str(bundle), "-o", "html", kubeconfig=off)
                assert html.returncode == 0 and "Collection coverage" in html.stdout
                assert wire == wire_before, "offline operation contacted Kubernetes"
                for public in [bundle.read_text(), live.stdout, replay.stdout, stored.stdout, html.stdout,
                               live.stderr, replay.stderr, stored.stderr, html.stderr]:
                    assert private_sentinel not in public and warning_sentinel not in public
                print(f"capture/replay/stored/HTML agree for {namespace}; the offline commands made zero API reads")
        assert not any("/secrets" in path or "/exec" in path for path in wire), wire
        print(f"{len(wire)} read-only requests reached the synthetic Kubernetes endpoint")
    finally:
        server.shutdown()
