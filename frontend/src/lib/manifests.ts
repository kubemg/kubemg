import type { ResourceItem, ResourceKey } from './resources'

/**
 * Starter manifests, and which kinds may be created at all.
 *
 * The console could change an object and delete it, but the object had to
 * already exist for either to have an address — so `kubectl create -f` was the
 * one thing the tunnel carried that the console could not do. What closes that
 * is the manifest editor opened on a document nothing has read: the same write
 * path, addressed at a collection instead of an object.
 *
 * A template is a **starting point, not a form**. KubeMG has no chart to render
 * and no opinion about what belongs in somebody's workload, so what these hold
 * is the shape of the kind — the fields the API server refuses the object
 * without, plus the few an operator would immediately add anyway — and nothing
 * else. They live here rather than on the server for the reason the resource
 * inventory lives here: this is presentation of a kind the sidebar already
 * knows about, and what actually reaches the cluster is the text the operator
 * typed over it, byte for byte.
 *
 * Note what is deliberately absent from every one of them: `metadata.namespace`.
 * The namespace is part of the *address*, chosen above the editor and checked
 * against the caller's grant, exactly as it is for a read; a manifest carrying
 * one as well is a second place for it to disagree with the first.
 */

/**
 * The kinds KubeMG will not create, mirroring the backend's own deny list — this
 * is only what keeps the button from being offered, the refusal itself lives on
 * the server. The RBAC four are the rule the manifest editor already states:
 * KubeMG reads a cluster's RBAC and does not author it. A Node is not created,
 * it joins. A Helm release has no manifest at all — it is a labelled Secret Helm
 * writes, and creating one from here would produce a release nothing installed.
 */
const NOT_CREATABLE = new Set<ResourceKey>([
  'roles',
  'rolebindings',
  'clusterroles',
  'clusterrolebindings',
  'nodes',
  'helmreleases',
])

/** Whether a Create action is offered for this resource. */
export function canCreateResource(item: ResourceItem): boolean {
  if (NOT_CREATABLE.has(item.key)) return false
  // A discovered CRD is creatable: its kind name is the singular the sidebar
  // already carries, which is all a skeleton needs.
  return item.custom !== undefined || item.key in MANIFEST_TEMPLATES
}

const MANIFEST_TEMPLATES: Partial<Record<ResourceKey, string>> = {
  pods: `apiVersion: v1
kind: Pod
metadata:
  name: example
  labels:
    app: example
spec:
  containers:
    - name: app
      image: nginx:1.27-alpine
      ports:
        - containerPort: 80
      resources:
        requests:
          cpu: 50m
          memory: 64Mi
        limits:
          cpu: 250m
          memory: 128Mi
`,
  deployments: `apiVersion: apps/v1
kind: Deployment
metadata:
  name: example
  labels:
    app: example
spec:
  replicas: 2
  selector:
    matchLabels:
      app: example
  template:
    metadata:
      labels:
        app: example
    spec:
      containers:
        - name: app
          image: nginx:1.27-alpine
          ports:
            - containerPort: 80
          resources:
            requests:
              cpu: 50m
              memory: 64Mi
            limits:
              cpu: 250m
              memory: 128Mi
`,
  statefulsets: `apiVersion: apps/v1
kind: StatefulSet
metadata:
  name: example
spec:
  # A StatefulSet needs a headless Service to give its pods stable names.
  serviceName: example
  replicas: 1
  selector:
    matchLabels:
      app: example
  template:
    metadata:
      labels:
        app: example
    spec:
      containers:
        - name: app
          image: nginx:1.27-alpine
          volumeMounts:
            - name: data
              mountPath: /data
  volumeClaimTemplates:
    - metadata:
        name: data
      spec:
        accessModes: ["ReadWriteOnce"]
        resources:
          requests:
            storage: 1Gi
`,
  daemonsets: `apiVersion: apps/v1
kind: DaemonSet
metadata:
  name: example
spec:
  selector:
    matchLabels:
      app: example
  template:
    metadata:
      labels:
        app: example
    spec:
      containers:
        - name: agent
          image: busybox:1.36
          command: ["sh", "-c", "sleep infinity"]
          resources:
            requests:
              cpu: 20m
              memory: 32Mi
`,
  jobs: `apiVersion: batch/v1
kind: Job
metadata:
  # generateName lets the cluster name each run, which is what you want for a
  # job you will submit more than once. Swap it for a plain name: for a one-off.
  generateName: example-
spec:
  backoffLimit: 3
  template:
    spec:
      restartPolicy: Never
      containers:
        - name: task
          image: busybox:1.36
          command: ["sh", "-c", "echo hello"]
`,
  cronjobs: `apiVersion: batch/v1
kind: CronJob
metadata:
  name: example
spec:
  schedule: "0 3 * * *"
  # Set this where the schedule means a wall clock somewhere in particular;
  # without it the cluster's own zone decides.
  # timeZone: Europe/Istanbul
  concurrencyPolicy: Forbid
  successfulJobsHistoryLimit: 3
  failedJobsHistoryLimit: 1
  jobTemplate:
    spec:
      template:
        spec:
          restartPolicy: OnFailure
          containers:
            - name: task
              image: busybox:1.36
              command: ["sh", "-c", "echo hello"]
`,
  services: `apiVersion: v1
kind: Service
metadata:
  name: example
spec:
  type: ClusterIP
  selector:
    app: example
  ports:
    - name: http
      port: 80
      targetPort: 80
      protocol: TCP
`,
  ingresses: `apiVersion: networking.k8s.io/v1
kind: Ingress
metadata:
  name: example
spec:
  # The class has to name an installed controller; kubectl get ingressclass
  # says which ones this cluster has.
  ingressClassName: nginx
  rules:
    - host: example.internal
      http:
        paths:
          - path: /
            pathType: Prefix
            backend:
              service:
                name: example
                port:
                  number: 80
`,
  networkpolicies: `apiVersion: networking.k8s.io/v1
kind: NetworkPolicy
metadata:
  name: example
spec:
  # An empty podSelector selects every pod in the namespace.
  podSelector:
    matchLabels:
      app: example
  policyTypes:
    - Ingress
  ingress:
    - from:
        - podSelector:
            matchLabels:
              app: caller
      ports:
        - protocol: TCP
          port: 80
`,
  persistentvolumeclaims: `apiVersion: v1
kind: PersistentVolumeClaim
metadata:
  name: example
spec:
  accessModes:
    - ReadWriteOnce
  resources:
    requests:
      storage: 1Gi
  # Left out, the cluster's default StorageClass is used.
  # storageClassName: standard
`,
  persistentvolumes: `apiVersion: v1
kind: PersistentVolume
metadata:
  name: example
spec:
  capacity:
    storage: 1Gi
  accessModes:
    - ReadWriteOnce
  persistentVolumeReclaimPolicy: Retain
  # Replace this with the volume this actually is — a PV that outlives its
  # node's local path is a claim the cluster cannot keep.
  hostPath:
    path: /mnt/data/example
`,
  storageclasses: `apiVersion: storage.k8s.io/v1
kind: StorageClass
metadata:
  name: example
# The provisioner has to be one this cluster runs.
provisioner: kubernetes.io/no-provisioner
volumeBindingMode: WaitForFirstConsumer
reclaimPolicy: Delete
allowVolumeExpansion: true
`,
  configmaps: `apiVersion: v1
kind: ConfigMap
metadata:
  name: example
data:
  KEY: value
  # A whole file goes under a block scalar:
  # app.conf: |
  #   listen = 8080
`,
  secrets: `apiVersion: v1
kind: Secret
metadata:
  name: example
type: Opaque
# stringData takes plain text and the cluster encodes it; use data: for values
# you already hold as base64. Once created, kubemg redacts these values on the
# way back out, so this manifest is the only place you will see them here.
stringData:
  KEY: value
`,
  serviceaccounts: `apiVersion: v1
kind: ServiceAccount
metadata:
  name: example
`,
  namespaces: `apiVersion: v1
kind: Namespace
metadata:
  name: example
  labels:
    kubernetes.io/metadata.name: example
`,
  crds: `apiVersion: apiextensions.k8s.io/v1
kind: CustomResourceDefinition
metadata:
  # The name has to be plural.group.
  name: examples.example.com
spec:
  group: example.com
  scope: Namespaced
  names:
    kind: Example
    plural: examples
    singular: example
  versions:
    - name: v1
      served: true
      storage: true
      schema:
        openAPIV3Schema:
          type: object
          properties:
            spec:
              type: object
              properties:
                size:
                  type: integer
`,
  httproutes: `apiVersion: gateway.networking.k8s.io/v1
kind: HTTPRoute
metadata:
  name: example
spec:
  parentRefs:
    - name: gateway
  hostnames:
    - example.internal
  rules:
    - matches:
        - path:
            type: PathPrefix
            value: /
      backendRefs:
        - name: example
          port: 80
`,
  virtualservices: `apiVersion: networking.istio.io/v1
kind: VirtualService
metadata:
  name: example
spec:
  hosts:
    - example.internal
  gateways:
    - gateway
  http:
    - route:
        - destination:
            host: example
            port:
              number: 80
`,
}

/**
 * The manifest the editor opens on. For a CRD-served kind there is no template
 * to hold — its spec is whatever its author decided — so what is offered is the
 * four lines every Kubernetes object has, with the API and the Kind filled in
 * from the sidebar entry the list was read under. That is more than an empty
 * editor and less than a guess about somebody's schema.
 */
export function manifestTemplate(item: ResourceItem): string {
  const fixed = MANIFEST_TEMPLATES[item.key]
  if (fixed) return fixed

  const ref = item.custom
  if (!ref) return 'apiVersion: v1\nkind: \nmetadata:\n  name: example\n'
  return `apiVersion: ${ref.group}/${ref.version}
kind: ${item.singular ?? item.label}
metadata:
  name: example
spec: {}
`
}
