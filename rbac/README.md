# Example access

KubeDoctor uses the active kubeconfig credentials or the context selected explicitly. These are example permissions for cluster administrators to review and bind. KubeDoctor never applies them.

- For `namespace payments`, bind the namespace Role with a RoleBinding in `payments` and the dependency ClusterRole with a ClusterRoleBinding to the same identity. Change the Role namespace and the namespace resource name when adapting the example. Where hosting nodes and storage classes are fixed, the administrator can restrict their GET permissions with `resourceNames`.
- For `cluster`, bind the ClusterRole in `cluster.yaml` to an identity authorized to see all namespaces. The lists include ConfigMaps and Events. Kubernetes RBAC cannot grant key-only ConfigMap reads. KubeDoctor extracts key names and discards ConfigMap values.
- For both scopes, a separate RoleBinding in `kube-system` can grant conventional DNS observations from `dns.yaml`. Kubernetes RBAC cannot enforce the label selector on the Pod and EndpointSlice list grants. The client always uses its bounded selectors in a namespace scan.
- The named `node-local-dns` DaemonSet GET in `dns.yaml` supports the conventional NodeLocal DNSCache arrangement. Namespace diagnostics retain only its owned Pods on observed application hosting nodes.
- StatefulSet, Job, CronJob, HPA, ResourceQuota, and LimitRange list rules are optional for legacy scan completeness. Removing them prevents full diagnosis in their respective checks and the report records the lost access.
- The named `kube-system` Namespace GET in the cluster example is used only by cluster capture for comparison provenance. Namespace capture reuses the requested Namespace GET. Without provenance the tool can still capture evidence, but comparison may be unavailable.
- For optional current usage, grant `list` on `pods` in the `metrics.k8s.io` group for the desired scope and GET on `/apis/metrics.k8s.io/v1beta1` if general discovery is restricted. The report exposes missing metrics visibility.
