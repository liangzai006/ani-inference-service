# Inference tenant runtime prerequisites

The platform provisions this chart into an existing, platform-owned tenant
namespace. Use the exact namespace returned by the platform; do not derive a
name in Inference. Namespace ownership and Kubernetes UID are verified again
at service admission and during runtime operations.

The RoleBinding grants the Inference control-plane ServiceAccount access only
within the target tenant namespace. Its tenant-local Secret permissions are
needed for model download URLs and reading registry pull credentials. The runtime
ServiceAccount has no API token or workload RoleBinding.

Set `inference.serviceAccount`, `inference.namespace`, and
`runtimeServiceAccount.name` to the values used by the Inference release. If
model downloads use a private CA, provide the same ConfigMap name, key and CA
bundle configured in the control plane, or provision that ConfigMap separately.
Provision any image pull secrets listed by the runtime ServiceAccount in the
same namespace. The namespace lifecycle owner must run this provisioning for
each new tenant; the chart does not create or delete tenant namespaces.

The control-plane chart also installs read-only cluster watches for runtime
events. Those watches do not include Secrets; Secret reads are made directly
under the tenant-scoped RoleBinding.

The Resource service must authorize the Inference caller for standard tenant
namespaces and manage their registry pull Secrets. KServe must also use the
domain template in `charts/platform-values/kserve.yaml`: it separates the
service name and the 43-character tenant namespace into DNS labels so long
service names remain valid. Apply these prerequisites before enabling tenant
placement in the Inference control plane.
