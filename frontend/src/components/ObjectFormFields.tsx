import { useId } from 'react'
import type { ReactNode } from 'react'
import { Plus, Trash2 } from 'lucide-react'
import type {
  ConfigMapFormValues,
  EnvEntry,
  HTTPRouteFormValues,
  IngressFormValues,
  LabelEntry,
  ObjectFormValues,
  PortEntry,
  ProbeValues,
  ServiceFormValues,
  ServicePortEntry,
  WorkloadFormValues,
} from '../lib/objectForm'
import { entryId } from '../lib/objectForm'
import { Button, Field, Select, TextArea, TextInput } from './primitives'

/**
 * The fields the form asks for, per kind.
 *
 * Nothing here decides anything: every control edits the value object and the
 * sheet above re-renders the manifest from it (`renderObjectManifest`), which
 * is what keeps this file a set of inputs and the generator the single place
 * that knows what a Deployment looks like. There is no parse in the other
 * direction, on purpose — see the header of `lib/objectForm.ts`.
 *
 * The shape is the same for every kind: the fields a person types, then the
 * repeating lists, then the defaults they should have and would not have typed.
 */

/* ------------------------------------------------------------- chrome ------ */

function Group({ title, hint, children }: { title: string; hint?: string; children: ReactNode }) {
  return (
    <section className="flex flex-col gap-3 rounded-card border border-line-soft p-3.5">
      <div className="flex flex-col gap-1">
        <h4 className="text-[13px] font-medium text-fg">{title}</h4>
        {hint ? <p className="text-[12px] leading-snug text-muted">{hint}</p> : null}
      </div>
      {children}
    </section>
  )
}

function Grid({ children }: { children: ReactNode }) {
  return <div className="grid grid-cols-1 gap-3 sm:grid-cols-2">{children}</div>
}

/** One repeating list: a header with its Add, the rows, and what it says empty. */
function RowList<T extends { id: string }>({
  label,
  hint,
  addLabel,
  empty,
  entries,
  onChange,
  create,
  children,
}: {
  label: string
  hint?: string
  addLabel: string
  empty: string
  entries: T[]
  onChange: (next: T[]) => void
  create: () => T
  children: (entry: T, update: (patch: Partial<T>) => void) => ReactNode
}) {
  return (
    <div className="flex flex-col gap-2">
      <div className="flex items-center justify-between gap-3">
        <div className="flex flex-col gap-0.5">
          <span className="label">{label}</span>
          {hint ? <span className="text-[12px] leading-snug text-muted">{hint}</span> : null}
        </div>
        <Button type="button" size="sm" onClick={() => onChange([...entries, create()])}>
          <Plus aria-hidden="true" className="size-3.5" />
          {addLabel}
        </Button>
      </div>
      {entries.length === 0 ? (
        <p className="text-[12.5px] text-muted">{empty}</p>
      ) : (
        <ul className="flex flex-col gap-2">
          {entries.map((entry, index) => (
            <li
              key={entry.id}
              className="flex flex-wrap items-center gap-2 rounded-control border border-line-soft px-3 py-2"
            >
              {children(entry, (patch) =>
                onChange(entries.map((item, i) => (i === index ? { ...item, ...patch } : item))),
              )}
              <Button
                type="button"
                variant="ghost"
                size="sm"
                className="ml-auto"
                aria-label={`Remove ${label.toLowerCase()} row`}
                onClick={() => onChange(entries.filter((_, i) => i !== index))}
              >
                <Trash2 aria-hidden="true" className="size-3.5" />
              </Button>
            </li>
          ))}
        </ul>
      )}
    </div>
  )
}

/* ---------------------------------------------------------------- probes --- */

/**
 * A probe is one control with three states rather than a checkbox and a
 * disabled block: "none" is a real answer, and the manifest simply does not
 * carry the probe. Left blank, the port falls back to the container's first
 * one — the number the operator already typed a few fields up.
 */
function ProbeFields({
  label,
  hint,
  values,
  onChange,
}: {
  label: string
  hint: string
  values: ProbeValues
  onChange: (next: ProbeValues) => void
}) {
  const id = useId()
  return (
    <div className="flex flex-col gap-3">
      <Field label={label} htmlFor={`${id}-mode`} hint={hint}>
        <Select
          id={`${id}-mode`}
          value={values.mode}
          onChange={(event) => onChange({ ...values, mode: event.target.value as ProbeValues['mode'] })}
        >
          <option value="none">None</option>
          <option value="http">HTTP GET</option>
          <option value="tcp">TCP socket</option>
        </Select>
      </Field>
      {values.mode === 'none' ? null : (
        <Grid>
          {values.mode === 'http' ? (
            <Field label="Path" htmlFor={`${id}-path`}>
              <TextInput
                id={`${id}-path`}
                value={values.path}
                placeholder="/healthz"
                onChange={(event) => onChange({ ...values, path: event.target.value })}
              />
            </Field>
          ) : null}
          <Field label="Port" htmlFor={`${id}-port`} hint="The container's first port if left blank.">
            <TextInput
              id={`${id}-port`}
              value={values.port}
              placeholder="8080"
              onChange={(event) => onChange({ ...values, port: event.target.value })}
            />
          </Field>
          <Field label="Initial delay (s)" htmlFor={`${id}-delay`}>
            <TextInput
              id={`${id}-delay`}
              value={values.initialDelaySeconds}
              placeholder="0"
              onChange={(event) => onChange({ ...values, initialDelaySeconds: event.target.value })}
            />
          </Field>
          <Field label="Period (s)" htmlFor={`${id}-period`}>
            <TextInput
              id={`${id}-period`}
              value={values.periodSeconds}
              placeholder="10"
              onChange={(event) => onChange({ ...values, periodSeconds: event.target.value })}
            />
          </Field>
        </Grid>
      )}
    </div>
  )
}

/* -------------------------------------------------------------- workload --- */

function WorkloadFields({
  values,
  onChange,
}: {
  values: WorkloadFormValues
  onChange: (next: WorkloadFormValues) => void
}) {
  const id = useId()
  const set = (patch: Partial<WorkloadFormValues>) => onChange({ ...values, ...patch })

  return (
    <>
      <Group title="Identity">
        <Grid>
          <Field
            label="Name"
            htmlFor={`${id}-name`}
            hint="Also the app label, and what the selector matches."
          >
            <TextInput
              id={`${id}-name`}
              value={values.name}
              placeholder="payments-api"
              onChange={(event) => set({ name: event.target.value })}
            />
          </Field>
          {values.kind === 'deployments' ? (
            <Field label="Replicas" htmlFor={`${id}-replicas`}>
              <TextInput
                id={`${id}-replicas`}
                value={values.replicas}
                placeholder="2"
                onChange={(event) => set({ replicas: event.target.value })}
              />
            </Field>
          ) : null}
          {values.kind === 'cronjobs' ? (
            <>
              <Field label="Schedule" htmlFor={`${id}-schedule`} hint="Five-field cron.">
                <TextInput
                  id={`${id}-schedule`}
                  className="font-mono text-[12.5px]"
                  value={values.schedule}
                  placeholder="0 3 * * *"
                  onChange={(event) => set({ schedule: event.target.value })}
                />
              </Field>
              <Field
                label="Time zone"
                htmlFor={`${id}-tz`}
                hint="Without one, the cluster's own zone decides when this fires."
              >
                <TextInput
                  id={`${id}-tz`}
                  value={values.timeZone}
                  placeholder="Europe/Istanbul"
                  onChange={(event) => set({ timeZone: event.target.value })}
                />
              </Field>
            </>
          ) : null}
        </Grid>
      </Group>

      <Group title="Container">
        <Grid>
          <Field label="Container name" htmlFor={`${id}-cname`}>
            <TextInput
              id={`${id}-cname`}
              value={values.containerName}
              placeholder="app"
              onChange={(event) => set({ containerName: event.target.value })}
            />
          </Field>
          <Field label="Image" htmlFor={`${id}-image`}>
            <TextInput
              id={`${id}-image`}
              className="font-mono text-[12.5px]"
              value={values.image}
              placeholder="nginx"
              onChange={(event) => set({ image: event.target.value })}
            />
          </Field>
          <Field
            label="Tag"
            htmlFor={`${id}-tag`}
            hint="Left blank the cluster pulls :latest, which is a different image tomorrow."
          >
            <TextInput
              id={`${id}-tag`}
              className="font-mono text-[12.5px]"
              value={values.tag}
              placeholder="1.27-alpine"
              onChange={(event) => set({ tag: event.target.value })}
            />
          </Field>
          <Field
            label="ServiceAccount"
            htmlFor={`${id}-sa`}
            hint="Blank uses the namespace's default account."
          >
            <TextInput
              id={`${id}-sa`}
              value={values.serviceAccountName}
              placeholder="default"
              onChange={(event) => set({ serviceAccountName: event.target.value })}
            />
          </Field>
        </Grid>

        <RowList<PortEntry>
          label="Ports"
          addLabel="Add port"
          empty="No ports. A container serving nothing needs none."
          entries={values.ports}
          onChange={(ports) => set({ ports })}
          create={() => ({ id: entryId(), name: '', port: '', protocol: 'TCP' })}
        >
          {(entry, update) => (
            <>
              <TextInput
                aria-label="Port name"
                className="w-32"
                value={entry.name}
                placeholder="http"
                onChange={(event) => update({ name: event.target.value })}
              />
              <TextInput
                aria-label="Container port"
                className="w-28 font-mono text-[12.5px]"
                value={entry.port}
                placeholder="8080"
                onChange={(event) => update({ port: event.target.value })}
              />
              <Select
                aria-label="Protocol"
                size="sm"
                className="w-24"
                value={entry.protocol}
                onChange={(event) => update({ protocol: event.target.value as PortEntry['protocol'] })}
              >
                <option value="TCP">TCP</option>
                <option value="UDP">UDP</option>
              </Select>
            </>
          )}
        </RowList>

        <RowList<EnvEntry>
          label="Environment"
          hint="A literal value, or one key out of a ConfigMap or a Secret."
          addLabel="Add variable"
          empty="No environment variables."
          entries={values.env}
          onChange={(env) => set({ env })}
          create={() => ({
            id: entryId(),
            name: '',
            from: 'value',
            value: '',
            refName: '',
            refKey: '',
          })}
        >
          {(entry, update) => (
            <>
              <TextInput
                aria-label="Variable name"
                className="w-40 font-mono text-[12.5px]"
                value={entry.name}
                placeholder="LOG_LEVEL"
                onChange={(event) => update({ name: event.target.value })}
              />
              <Select
                aria-label="Source"
                size="sm"
                className="w-32"
                value={entry.from}
                onChange={(event) => update({ from: event.target.value as EnvEntry['from'] })}
              >
                <option value="value">Value</option>
                <option value="configmap">ConfigMap</option>
                <option value="secret">Secret</option>
              </Select>
              {entry.from === 'value' ? (
                <TextInput
                  aria-label="Value"
                  className="w-44"
                  value={entry.value}
                  placeholder="info"
                  onChange={(event) => update({ value: event.target.value })}
                />
              ) : (
                <>
                  <TextInput
                    aria-label={entry.from === 'secret' ? 'Secret name' : 'ConfigMap name'}
                    className="w-36"
                    value={entry.refName}
                    placeholder={entry.from === 'secret' ? 'db-credentials' : 'app-config'}
                    onChange={(event) => update({ refName: event.target.value })}
                  />
                  <TextInput
                    aria-label="Key"
                    className="w-32 font-mono text-[12.5px]"
                    value={entry.refKey}
                    placeholder="password"
                    onChange={(event) => update({ refKey: event.target.value })}
                  />
                </>
              )}
            </>
          )}
        </RowList>
      </Group>

      <Group
        title="Resources"
        hint="A container with no limit is accounted for as nothing, which is what makes a node run out. Blank leaves the field out."
      >
        <Grid>
          <Field label="CPU request" htmlFor={`${id}-cpureq`}>
            <TextInput
              id={`${id}-cpureq`}
              className="font-mono text-[12.5px]"
              value={values.cpuRequest}
              placeholder="50m"
              onChange={(event) => set({ cpuRequest: event.target.value })}
            />
          </Field>
          <Field label="CPU limit" htmlFor={`${id}-cpulim`}>
            <TextInput
              id={`${id}-cpulim`}
              className="font-mono text-[12.5px]"
              value={values.cpuLimit}
              placeholder="250m"
              onChange={(event) => set({ cpuLimit: event.target.value })}
            />
          </Field>
          <Field label="Memory request" htmlFor={`${id}-memreq`}>
            <TextInput
              id={`${id}-memreq`}
              className="font-mono text-[12.5px]"
              value={values.memoryRequest}
              placeholder="64Mi"
              onChange={(event) => set({ memoryRequest: event.target.value })}
            />
          </Field>
          <Field label="Memory limit" htmlFor={`${id}-memlim`}>
            <TextInput
              id={`${id}-memlim`}
              className="font-mono text-[12.5px]"
              value={values.memoryLimit}
              placeholder="128Mi"
              onChange={(event) => set({ memoryLimit: event.target.value })}
            />
          </Field>
        </Grid>
      </Group>

      <Group title="Health">
        <ProbeFields
          label="Liveness probe"
          hint="Failing it restarts the container."
          values={values.liveness}
          onChange={(liveness) => set({ liveness })}
        />
        <ProbeFields
          label="Readiness probe"
          hint="Failing it takes the pod out of its Service."
          values={values.readiness}
          onChange={(readiness) => set({ readiness })}
        />
      </Group>

      <Group
        title="Security"
        hint="On, the manifest declares non-root and forbids privilege escalation — what the workload posture scan reads. An image whose own USER is root will refuse to start under it, which is the point: change the image rather than the manifest."
      >
        <label className="flex items-center gap-2 text-[13px] text-fg">
          <input
            type="checkbox"
            className="size-4 accent-[var(--color-accent)]"
            checked={values.nonRoot}
            onChange={(event) => set({ nonRoot: event.target.checked })}
          />
          Run as non-root
        </label>
      </Group>
    </>
  )
}

/* --------------------------------------------------------------- service --- */

function ServiceFields({
  values,
  onChange,
}: {
  values: ServiceFormValues
  onChange: (next: ServiceFormValues) => void
}) {
  const id = useId()
  const set = (patch: Partial<ServiceFormValues>) => onChange({ ...values, ...patch })

  return (
    <>
      <Group title="Identity">
        <Grid>
          <Field label="Name" htmlFor={`${id}-name`}>
            <TextInput
              id={`${id}-name`}
              value={values.name}
              placeholder="payments-api"
              onChange={(event) => set({ name: event.target.value })}
            />
          </Field>
          <Field
            label="Type"
            htmlFor={`${id}-type`}
            hint="ClusterIP is reachable inside the cluster only."
          >
            <Select
              id={`${id}-type`}
              value={values.type}
              onChange={(event) => set({ type: event.target.value as ServiceFormValues['type'] })}
            >
              <option value="ClusterIP">ClusterIP</option>
              <option value="NodePort">NodePort</option>
              <option value="LoadBalancer">LoadBalancer</option>
            </Select>
          </Field>
        </Grid>
      </Group>

      <Group title="Selector" hint="The labels a pod has to carry for this Service to send it traffic.">
        <RowList<LabelEntry>
          label="Labels"
          addLabel="Add label"
          empty="No selector. A Service with none matches nothing."
          entries={values.selector}
          onChange={(selector) => set({ selector })}
          create={() => ({ id: entryId(), key: '', value: '' })}
        >
          {(entry, update) => (
            <>
              <TextInput
                aria-label="Label key"
                className="w-40 font-mono text-[12.5px]"
                value={entry.key}
                placeholder="app"
                onChange={(event) => update({ key: event.target.value })}
              />
              <TextInput
                aria-label="Label value"
                className="w-44 font-mono text-[12.5px]"
                value={entry.value}
                placeholder="payments-api"
                onChange={(event) => update({ value: event.target.value })}
              />
            </>
          )}
        </RowList>
      </Group>

      <Group title="Ports">
        <RowList<ServicePortEntry>
          label="Ports"
          hint="The target may be a number or the container port's name."
          addLabel="Add port"
          empty="No ports."
          entries={values.ports}
          onChange={(ports) => set({ ports })}
          create={() => ({ id: entryId(), name: '', port: '', targetPort: '', protocol: 'TCP' })}
        >
          {(entry, update) => (
            <>
              <TextInput
                aria-label="Port name"
                className="w-28"
                value={entry.name}
                placeholder="http"
                onChange={(event) => update({ name: event.target.value })}
              />
              <TextInput
                aria-label="Service port"
                className="w-24 font-mono text-[12.5px]"
                value={entry.port}
                placeholder="80"
                onChange={(event) => update({ port: event.target.value })}
              />
              <TextInput
                aria-label="Target port"
                className="w-28 font-mono text-[12.5px]"
                value={entry.targetPort}
                placeholder="8080"
                onChange={(event) => update({ targetPort: event.target.value })}
              />
              <Select
                aria-label="Protocol"
                size="sm"
                className="w-24"
                value={entry.protocol}
                onChange={(event) =>
                  update({ protocol: event.target.value as ServicePortEntry['protocol'] })
                }
              >
                <option value="TCP">TCP</option>
                <option value="UDP">UDP</option>
              </Select>
            </>
          )}
        </RowList>
      </Group>
    </>
  )
}

/* --------------------------------------------------------------- ingress --- */

function IngressFields({
  values,
  onChange,
}: {
  values: IngressFormValues
  onChange: (next: IngressFormValues) => void
}) {
  const id = useId()
  const set = (patch: Partial<IngressFormValues>) => onChange({ ...values, ...patch })

  return (
    <>
      <Group title="Identity">
        <Grid>
          <Field label="Name" htmlFor={`${id}-name`}>
            <TextInput
              id={`${id}-name`}
              value={values.name}
              placeholder="payments"
              onChange={(event) => set({ name: event.target.value })}
            />
          </Field>
          <Field
            label="Ingress class"
            htmlFor={`${id}-class`}
            hint="Has to name a controller this cluster runs — the IngressClass list says which."
          >
            <TextInput
              id={`${id}-class`}
              value={values.className}
              placeholder="nginx"
              onChange={(event) => set({ className: event.target.value })}
            />
          </Field>
        </Grid>
      </Group>

      <Group
        title="Routes"
        hint="One row per URL. Rows sharing a host become one rule; a row with no host matches any."
      >
        <RowList<IngressFormValues['rules'][number]>
          label="Routes"
          addLabel="Add route"
          empty="No routes. An Ingress with none forwards nothing."
          entries={values.rules}
          onChange={(rules) => set({ rules })}
          create={() => ({
            id: entryId(),
            host: '',
            path: '/',
            pathType: 'Prefix',
            serviceName: '',
            servicePort: '80',
          })}
        >
          {(entry, update) => (
            <>
              <TextInput
                aria-label="Host"
                className="w-44"
                value={entry.host}
                placeholder="payments.internal"
                onChange={(event) => update({ host: event.target.value })}
              />
              <TextInput
                aria-label="Path"
                className="w-28 font-mono text-[12.5px]"
                value={entry.path}
                placeholder="/"
                onChange={(event) => update({ path: event.target.value })}
              />
              <Select
                aria-label="Path type"
                size="sm"
                className="w-40"
                value={entry.pathType}
                onChange={(event) =>
                  update({ pathType: event.target.value as IngressRulePathType })
                }
              >
                <option value="Prefix">Prefix</option>
                <option value="Exact">Exact</option>
                <option value="ImplementationSpecific">ImplementationSpecific</option>
              </Select>
              <TextInput
                aria-label="Service name"
                className="w-36"
                value={entry.serviceName}
                placeholder="payments-api"
                onChange={(event) => update({ serviceName: event.target.value })}
              />
              <TextInput
                aria-label="Service port"
                className="w-24 font-mono text-[12.5px]"
                value={entry.servicePort}
                placeholder="80"
                onChange={(event) => update({ servicePort: event.target.value })}
              />
            </>
          )}
        </RowList>
      </Group>

      <Group title="TLS">
        <Field
          label="Certificate Secret"
          htmlFor={`${id}-tls`}
          hint="Named, the hosts above are terminated with it. Blank writes no tls block at all."
        >
          <TextInput
            id={`${id}-tls`}
            value={values.tlsSecretName}
            placeholder="payments-tls"
            onChange={(event) => set({ tlsSecretName: event.target.value })}
          />
        </Field>
      </Group>
    </>
  )
}

type IngressRulePathType = IngressFormValues['rules'][number]['pathType']

/* ------------------------------------------------------------- httproute --- */

function HTTPRouteFields({
  values,
  onChange,
}: {
  values: HTTPRouteFormValues
  onChange: (next: HTTPRouteFormValues) => void
}) {
  const id = useId()
  const set = (patch: Partial<HTTPRouteFormValues>) => onChange({ ...values, ...patch })

  return (
    <>
      <Group title="Identity">
        <Grid>
          <Field label="Name" htmlFor={`${id}-name`}>
            <TextInput
              id={`${id}-name`}
              value={values.name}
              placeholder="payments"
              onChange={(event) => set({ name: event.target.value })}
            />
          </Field>
          <Field label="Gateway" htmlFor={`${id}-gw`} hint="The Gateway this route attaches to.">
            <TextInput
              id={`${id}-gw`}
              value={values.gatewayName}
              placeholder="external"
              onChange={(event) => set({ gatewayName: event.target.value })}
            />
          </Field>
          <Field
            label="Gateway namespace"
            htmlFor={`${id}-gwns`}
            hint="Blank looks for it in this object's own namespace."
          >
            <TextInput
              id={`${id}-gwns`}
              value={values.gatewayNamespace}
              placeholder="gateway-system"
              onChange={(event) => set({ gatewayNamespace: event.target.value })}
            />
          </Field>
        </Grid>
      </Group>

      <Group title="Hostnames">
        <RowList<HTTPRouteFormValues['hostnames'][number]>
          label="Hostnames"
          addLabel="Add hostname"
          empty="No hostnames. The route then answers for every host the Gateway does."
          entries={values.hostnames}
          onChange={(hostnames) => set({ hostnames })}
          create={() => ({ id: entryId(), value: '' })}
        >
          {(entry, update) => (
            <TextInput
              aria-label="Hostname"
              className="w-64"
              value={entry.value}
              placeholder="payments.internal"
              onChange={(event) => update({ value: event.target.value })}
            />
          )}
        </RowList>
      </Group>

      <Group title="Rules">
        <RowList<HTTPRouteFormValues['rules'][number]>
          label="Rules"
          addLabel="Add rule"
          empty="No rules. A route with none forwards nothing."
          entries={values.rules}
          onChange={(rules) => set({ rules })}
          create={() => ({
            id: entryId(),
            path: '/',
            pathType: 'PathPrefix',
            backendName: '',
            backendPort: '80',
          })}
        >
          {(entry, update) => (
            <>
              <Select
                aria-label="Path type"
                size="sm"
                className="w-32"
                value={entry.pathType}
                onChange={(event) =>
                  update({
                    pathType: event.target.value as HTTPRouteFormValues['rules'][number]['pathType'],
                  })
                }
              >
                <option value="PathPrefix">PathPrefix</option>
                <option value="Exact">Exact</option>
              </Select>
              <TextInput
                aria-label="Path"
                className="w-28 font-mono text-[12.5px]"
                value={entry.path}
                placeholder="/"
                onChange={(event) => update({ path: event.target.value })}
              />
              <TextInput
                aria-label="Backend service"
                className="w-40"
                value={entry.backendName}
                placeholder="payments-api"
                onChange={(event) => update({ backendName: event.target.value })}
              />
              <TextInput
                aria-label="Backend port"
                className="w-24 font-mono text-[12.5px]"
                value={entry.backendPort}
                placeholder="80"
                onChange={(event) => update({ backendPort: event.target.value })}
              />
            </>
          )}
        </RowList>
      </Group>
    </>
  )
}

/* ------------------------------------------------------------- configmap --- */

function ConfigMapFields({
  values,
  onChange,
}: {
  values: ConfigMapFormValues
  onChange: (next: ConfigMapFormValues) => void
}) {
  const id = useId()

  return (
    <>
      <Group title="Identity">
        <Field label="Name" htmlFor={`${id}-name`}>
          <TextInput
            id={`${id}-name`}
            value={values.name}
            placeholder="app-config"
            onChange={(event) => onChange({ ...values, name: event.target.value })}
          />
        </Field>
      </Group>

      <Group
        title="Data"
        hint="A value with newlines in it is written as a block scalar, so a whole file pasted here keeps its own indentation."
      >
        <RowList<ConfigMapFormValues['entries'][number]>
          label="Entries"
          addLabel="Add entry"
          empty="No entries. The ConfigMap is created empty."
          entries={values.entries}
          onChange={(entries) => onChange({ ...values, entries })}
          create={() => ({ id: entryId(), key: '', value: '' })}
        >
          {(entry, update) => (
            <>
              <TextInput
                aria-label="Key"
                className="w-44 self-start font-mono text-[12.5px]"
                value={entry.key}
                placeholder="app.conf"
                onChange={(event) => update({ key: event.target.value })}
              />
              <TextArea
                aria-label="Value"
                rows={2}
                className="w-80"
                value={entry.value}
                placeholder="listen = 8080"
                onChange={(event) => update({ value: event.target.value })}
              />
            </>
          )}
        </RowList>
      </Group>
    </>
  )
}

/* ------------------------------------------------------------------ all ---- */

export function ObjectFormFields({
  values,
  onChange,
}: {
  values: ObjectFormValues
  onChange: (next: ObjectFormValues) => void
}) {
  switch (values.kind) {
    case 'pods':
    case 'deployments':
    case 'cronjobs':
      return <WorkloadFields values={values} onChange={onChange} />
    case 'services':
      return <ServiceFields values={values} onChange={onChange} />
    case 'ingresses':
      return <IngressFields values={values} onChange={onChange} />
    case 'httproutes':
      return <HTTPRouteFields values={values} onChange={onChange} />
    case 'configmaps':
      return <ConfigMapFields values={values} onChange={onChange} />
  }
}
