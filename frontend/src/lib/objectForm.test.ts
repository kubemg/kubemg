import { describe, expect, it } from 'vitest'

import {
  entryId,
  initialObjectForm,
  objectFormKind,
  renderObjectManifest,
  type ConfigMapFormValues,
  type IngressFormValues,
  type HTTPRouteFormValues,
  type ServiceFormValues,
  type WorkloadFormValues,
} from './objectForm'
import { resourceItem, type ResourceItem } from './resources'

/*
 * The form's whole output is a string, which is why these assertions are whole
 * manifests rather than a scattering of `toContain`: what matters about a
 * generated manifest is as much what it does *not* say (no `metadata.namespace`,
 * no `securityContext` when the box is off, no zero-valued field somebody has to
 * notice and delete) as what it does, and only comparing the document catches
 * that. They were run by hand once when the module was written; this is that
 * pass, kept.
 */

function workload(kind: 'pods' | 'deployments' | 'cronjobs'): WorkloadFormValues {
  return initialObjectForm(kind) as WorkloadFormValues
}

function item(key: string): ResourceItem {
  const found = resourceItem(key as Parameters<typeof resourceItem>[0])
  if (!found) throw new Error(`no fixed inventory entry for ${key}`)
  return found
}

describe('objectFormKind', () => {
  it('offers a form for each of the seven kinds, and for nothing else', () => {
    const offered = [
      'pods',
      'deployments',
      'cronjobs',
      'services',
      'ingresses',
      'httproutes',
      'configmaps',
    ]
    for (const key of offered) expect(objectFormKind(item(key))).toBe(key)
    for (const key of ['statefulsets', 'daemonsets', 'jobs', 'secrets', 'namespaces']) {
      expect(objectFormKind(item(key))).toBeNull()
    }
  })

  it('asks the deny list rather than mirroring it', () => {
    // A kind KubeMG will not create at all never gets a form, so adding one to
    // the deny list removes its form in the same edit.
    for (const key of ['roles', 'clusterrolebindings', 'nodes']) {
      expect(objectFormKind(item(key))).toBeNull()
    }
  })

  it('never offers a form for a discovered CRD', () => {
    const custom: ResourceItem = {
      key: 'crd:kafka.strimzi.io/v1beta2/kafkatopics',
      label: 'KafkaTopics',
      scope: 'namespaced',
      custom: {
        group: 'kafka.strimzi.io',
        version: 'v1beta2',
        plural: 'kafkatopics',
        scope: 'namespaced',
      },
    }
    expect(objectFormKind(custom)).toBeNull()
  })
})

describe('renderObjectManifest: a Deployment', () => {
  it('renders the full field set', () => {
    const values: WorkloadFormValues = {
      ...workload('deployments'),
      name: 'web',
      image: 'nginx',
      tag: '1.27-alpine',
      replicas: '3',
      serviceAccountName: 'web',
      ports: [{ id: entryId(), name: 'http', port: '8080', protocol: 'TCP' }],
      env: [
        { id: entryId(), name: 'LOG_LEVEL', from: 'value', value: 'debug', refName: '', refKey: '' },
        { id: entryId(), name: 'TOKEN', from: 'secret', value: '', refName: 'web', refKey: 'token' },
        {
          id: entryId(),
          name: 'REGION',
          from: 'configmap',
          value: '',
          refName: 'site',
          refKey: 'region',
        },
      ],
      liveness: {
        mode: 'http',
        path: '/healthz',
        port: '',
        initialDelaySeconds: '10',
        periodSeconds: '',
      },
      readiness: { mode: 'tcp', path: '', port: '9090', initialDelaySeconds: '', periodSeconds: '5' },
    }

    expect(renderObjectManifest(values)).toBe(`apiVersion: apps/v1
kind: Deployment
metadata:
  name: web
  labels:
    app: web
spec:
  replicas: 3
  selector:
    matchLabels:
      app: web
  template:
    metadata:
      labels:
        app: web
    spec:
      serviceAccountName: web
      securityContext:
        runAsNonRoot: true
        seccompProfile:
          type: RuntimeDefault
      containers:
        - name: app
          image: nginx:1.27-alpine
          ports:
            - name: http
              containerPort: 8080
          env:
            - name: LOG_LEVEL
              value: debug
            - name: TOKEN
              valueFrom:
                secretKeyRef:
                  name: web
                  key: token
            - name: REGION
              valueFrom:
                configMapKeyRef:
                  name: site
                  key: region
          resources:
            requests:
              cpu: 50m
              memory: 64Mi
            limits:
              cpu: 250m
              memory: 128Mi
          livenessProbe:
            httpGet:
              path: /healthz
              port: 8080
            initialDelaySeconds: 10
          readinessProbe:
            tcpSocket:
              port: 9090
            periodSeconds: 5
          securityContext:
            allowPrivilegeEscalation: false
`)
  })

  it('writes no securityContext at all when non-root is off', () => {
    // The posture rule reads an absence, and so does this: off must not render
    // a context declaring root, it must render none.
    const rendered = renderObjectManifest({
      ...workload('deployments'),
      name: 'web',
      image: 'nginx',
      nonRoot: false,
    })
    expect(rendered).not.toContain('securityContext')
    expect(rendered).not.toContain('runAsNonRoot')
    // And the rest of the manifest is unaffected.
    expect(rendered).toContain('image: nginx\n')
  })

  it('never writes metadata.namespace — the namespace is the address', () => {
    for (const kind of ['pods', 'deployments', 'cronjobs'] as const) {
      expect(renderObjectManifest({ ...workload(kind), name: 'x', image: 'nginx' })).not.toContain(
        'namespace:',
      )
    }
  })

  it('leaves a half-typed number out rather than rendering NaN', () => {
    const rendered = renderObjectManifest({
      ...workload('deployments'),
      name: 'web',
      image: 'nginx',
      replicas: '',
      cpuRequest: '',
      memoryRequest: '',
      cpuLimit: '',
      memoryLimit: '',
      ports: [{ id: entryId(), name: 'http', port: 'eighty', protocol: 'TCP' }],
    })
    expect(rendered).not.toContain('NaN')
    expect(rendered).not.toContain('replicas:')
    expect(rendered).not.toContain('resources:')
    expect(rendered).not.toContain('ports:')
  })
})

describe('renderObjectManifest: a Pod and a CronJob', () => {
  it('renders a Pod with no controller around it', () => {
    expect(
      renderObjectManifest({
        ...workload('pods'),
        name: 'debug',
        image: 'busybox',
        tag: '1.36',
        cpuRequest: '',
        memoryRequest: '',
        cpuLimit: '',
        memoryLimit: '',
      }),
    ).toBe(`apiVersion: v1
kind: Pod
metadata:
  name: debug
  labels:
    app: debug
spec:
  securityContext:
    runAsNonRoot: true
    seccompProfile:
      type: RuntimeDefault
  containers:
    - name: app
      image: busybox:1.36
      securityContext:
        allowPrivilegeEscalation: false
`)
  })

  it('quotes the schedule and restarts a run only on failure', () => {
    const rendered = renderObjectManifest({
      ...workload('cronjobs'),
      name: 'nightly',
      image: 'ghcr.io/acme/report',
      tag: 'v2',
      timeZone: 'Europe/Istanbul',
      cpuRequest: '',
      memoryRequest: '',
      cpuLimit: '',
      memoryLimit: '',
    })

    expect(rendered).toBe(`apiVersion: batch/v1
kind: CronJob
metadata:
  name: nightly
  labels:
    app: nightly
spec:
  schedule: "0 3 * * *"
  timeZone: Europe/Istanbul
  concurrencyPolicy: Forbid
  successfulJobsHistoryLimit: 3
  failedJobsHistoryLimit: 1
  jobTemplate:
    spec:
      template:
        metadata:
          labels:
            app: nightly
        spec:
          restartPolicy: OnFailure
          securityContext:
            runAsNonRoot: true
            seccompProfile:
              type: RuntimeDefault
          containers:
            - name: app
              image: ghcr.io/acme/report:v2
              securityContext:
                allowPrivilegeEscalation: false
`)
  })
})

describe('renderObjectManifest: a Service', () => {
  it('leaves the default type out and keeps a named targetPort a name', () => {
    const values: ServiceFormValues = {
      ...(initialObjectForm('services') as ServiceFormValues),
      name: 'web',
      selector: [{ id: entryId(), key: 'app', value: 'web' }],
      ports: [
        { id: entryId(), name: 'http', port: '80', targetPort: 'http', protocol: 'TCP' },
        { id: entryId(), name: 'dns', port: '53', targetPort: '5353', protocol: 'UDP' },
      ],
    }

    expect(renderObjectManifest(values)).toBe(`apiVersion: v1
kind: Service
metadata:
  name: web
spec:
  selector:
    app: web
  ports:
    - name: http
      port: 80
      targetPort: http
    - name: dns
      port: 53
      targetPort: 5353
      protocol: UDP
`)
  })

  it('writes the type when it is not the default', () => {
    const values: ServiceFormValues = {
      ...(initialObjectForm('services') as ServiceFormValues),
      name: 'web',
      type: 'LoadBalancer',
      selector: [{ id: entryId(), key: 'app', value: 'web' }],
    }
    expect(renderObjectManifest(values)).toContain('  type: LoadBalancer\n')
  })
})

describe('renderObjectManifest: an Ingress', () => {
  it('groups one-URL-per-row back into host rules, in first-seen order', () => {
    const values: IngressFormValues = {
      kind: 'ingresses',
      name: 'site',
      className: 'nginx',
      tlsSecretName: 'site-tls',
      rules: [
        {
          id: entryId(),
          host: 'acme.example',
          path: '/',
          pathType: 'Prefix',
          serviceName: 'web',
          servicePort: '80',
        },
        {
          id: entryId(),
          host: 'api.example',
          path: '/v1',
          pathType: 'Prefix',
          serviceName: 'api',
          servicePort: '8080',
        },
        {
          id: entryId(),
          host: 'acme.example',
          path: '/static',
          pathType: 'Exact',
          serviceName: 'cdn',
          servicePort: '80',
        },
        // Incomplete: no service, so the row is dropped rather than rendered
        // half-written.
        {
          id: entryId(),
          host: 'ghost.example',
          path: '/',
          pathType: 'Prefix',
          serviceName: '',
          servicePort: '80',
        },
      ],
    }

    expect(renderObjectManifest(values)).toBe(`apiVersion: networking.k8s.io/v1
kind: Ingress
metadata:
  name: site
spec:
  ingressClassName: nginx
  tls:
    - hosts:
        - acme.example
        - api.example
      secretName: site-tls
  rules:
    - host: acme.example
      http:
        paths:
          - path: /
            pathType: Prefix
            backend:
              service:
                name: web
                port:
                  number: 80
          - path: /static
            pathType: Exact
            backend:
              service:
                name: cdn
                port:
                  number: 80
    - host: api.example
      http:
        paths:
          - path: /v1
            pathType: Prefix
            backend:
              service:
                name: api
                port:
                  number: 8080
`)
  })

  it('renders a hostless row as the rule that matches anything', () => {
    const values: IngressFormValues = {
      kind: 'ingresses',
      name: 'site',
      className: '',
      tlsSecretName: '',
      rules: [
        {
          id: entryId(),
          host: '',
          path: '/',
          pathType: 'Prefix',
          serviceName: 'web',
          servicePort: '80',
        },
      ],
    }
    const rendered = renderObjectManifest(values)
    expect(rendered).not.toContain('host:')
    expect(rendered).not.toContain('tls:')
    expect(rendered).toContain('  rules:\n    - http:\n')
  })
})

describe('renderObjectManifest: an HTTPRoute', () => {
  it('names the Gateway as a parent and leaves its namespace out when unset', () => {
    const values: HTTPRouteFormValues = {
      ...(initialObjectForm('httproutes') as HTTPRouteFormValues),
      name: 'web',
      gatewayName: 'public',
      hostnames: [{ id: entryId(), value: 'acme.example' }],
      rules: [
        {
          id: entryId(),
          path: '/',
          pathType: 'PathPrefix',
          backendName: 'web',
          backendPort: '80',
        },
      ],
    }

    expect(renderObjectManifest(values)).toBe(`apiVersion: gateway.networking.k8s.io/v1
kind: HTTPRoute
metadata:
  name: web
spec:
  parentRefs:
    - name: public
  hostnames:
    - acme.example
  rules:
    - matches:
        - path:
            type: PathPrefix
            value: /
      backendRefs:
        - name: web
          port: 80
`)
  })
})

describe('renderObjectManifest: a ConfigMap', () => {
  it('writes a multi-line value as a block scalar, keeping the trailing newline', () => {
    const values: ConfigMapFormValues = {
      kind: 'configmaps',
      name: 'site',
      entries: [
        { id: entryId(), key: 'REGION', value: 'eu-central' },
        // Ends in a newline: `|` rather than `|-`, because a config parser can
        // care about the difference.
        { id: entryId(), key: 'nginx.conf', value: 'server {\n  listen 80;\n}\n' },
        // Does not: `|-`.
        { id: entryId(), key: 'motd', value: 'one\ntwo' },
        // No key, so nothing to address it by.
        { id: entryId(), key: '', value: 'orphan' },
      ],
    }

    expect(renderObjectManifest(values)).toBe(`apiVersion: v1
kind: ConfigMap
metadata:
  name: site
data:
  REGION: eu-central
  nginx.conf: |
    server {
      listen 80;
    }
  motd: |-
    one
    two
`)
  })

  it('renders an empty map rather than a dangling key', () => {
    expect(
      renderObjectManifest({ kind: 'configmaps', name: 'empty', entries: [] }),
    ).toBe(`apiVersion: v1
kind: ConfigMap
metadata:
  name: empty
data: {}
`)
  })
})

describe('the yaml writer', () => {
  it('quotes a scalar that would otherwise read as something else', () => {
    const rendered = renderObjectManifest({
      kind: 'configmaps',
      name: 'quoting',
      entries: [
        { id: entryId(), key: 'a', value: 'true' },
        { id: entryId(), key: 'b', value: '8080' },
        { id: entryId(), key: 'c', value: '' },
        { id: entryId(), key: 'd', value: '*' },
        { id: entryId(), key: 'e', value: 'key: value' },
        { id: entryId(), key: 'f', value: ' padded ' },
        // An image reference is the case that must *not* be quoted: a colon
        // only opens a mapping when a space follows it.
        { id: entryId(), key: 'g', value: 'nginx:1.27' },
      ],
    })

    expect(rendered).toContain('  a: "true"\n')
    expect(rendered).toContain('  b: "8080"\n')
    expect(rendered).toContain('  c: ""\n')
    expect(rendered).toContain('  d: "*"\n')
    expect(rendered).toContain('  e: "key: value"\n')
    expect(rendered).toContain('  f: " padded "\n')
    expect(rendered).toContain('  g: nginx:1.27\n')
  })

  it('indents a sequence dash under its key', () => {
    // Both forms parse; only one of them matches every other manifest this
    // console writes or shows.
    const rendered = renderObjectManifest({
      ...workload('pods'),
      name: 'web',
      image: 'nginx',
    })
    expect(rendered).toContain('  containers:\n    - name: app\n')
  })

  it('ends the document in a newline', () => {
    expect(renderObjectManifest(workload('pods')).endsWith('\n')).toBe(true)
  })
})

describe('initialObjectForm', () => {
  it('opens close to empty, with the two conventions that beat a blank', () => {
    const deployment = workload('deployments')
    expect(deployment.name).toBe('')
    expect(deployment.image).toBe('')
    expect(deployment.containerName).toBe('app')
    expect(deployment.nonRoot).toBe(true)
    expect(deployment.replicas).toBe('2')
    expect(workload('pods').replicas).toBe('')
    expect(workload('cronjobs').schedule).toBe('0 3 * * *')
    expect(workload('pods').schedule).toBe('')
  })

  it('gives every repeating row a key of its own', () => {
    const service = initialObjectForm('services') as ServiceFormValues
    const again = initialObjectForm('services') as ServiceFormValues
    expect(service.ports[0].id).not.toBe(again.ports[0].id)
  })
})
