import { describe, expect, it } from 'vitest'

import {
  hasPodLabels,
  supportsCordon,
  supportsRolloutHistory,
  supportsWorkloadLogs,
  supportsWorkloadPods,
  workloadCapability,
  workloadKeyFor,
} from './workloads'

/*
 * Five predicates over four nearly-identical kind lists, which is exactly the
 * shape a copy-paste edit quietly breaks: the lists differ on purpose, and the
 * differences are the assertions below.
 */

describe('workloadCapability', () => {
  it('offers scale where there is a replica count to set', () => {
    expect(workloadCapability('deployments')).toEqual({
      scale: true,
      restart: true,
      suspend: false,
    })
    expect(workloadCapability('statefulsets')?.scale).toBe(true)
    // A DaemonSet runs one pod per node; the node list is the count.
    expect(workloadCapability('daemonsets')?.scale).toBe(false)
    expect(workloadCapability('daemonsets')?.restart).toBe(true)
  })

  it("makes suspend the CronJob's own and only control", () => {
    expect(workloadCapability('cronjobs')).toEqual({
      scale: false,
      restart: false,
      suspend: true,
    })
    for (const key of ['deployments', 'statefulsets', 'daemonsets']) {
      expect(workloadCapability(key)?.suspend).toBe(false)
    }
  })

  it('answers for nothing at all where the controls do not apply', () => {
    for (const key of ['pods', 'jobs', 'services', 'replicasets', 'crd:acme.io/v1/widgets']) {
      expect(workloadCapability(key)).toBeUndefined()
    }
  })
})

describe('the pod-set predicates', () => {
  it("reads a CronJob's pods but not its logs", () => {
    // A CronJob owns Jobs rather than pods and has no selector, so the pooled
    // log view cannot derive a set — the pod list is resolved through its Jobs
    // on the server instead.
    expect(supportsWorkloadPods('cronjobs')).toBe(true)
    expect(supportsWorkloadLogs('cronjobs')).toBe(false)
    for (const key of ['deployments', 'statefulsets', 'daemonsets', 'jobs']) {
      expect(supportsWorkloadLogs(key)).toBe(true)
      expect(supportsWorkloadPods(key)).toBe(true)
    }
    // A pod has its own log and its own self; neither is a pooled read.
    expect(supportsWorkloadPods('pods')).toBe(false)
    expect(supportsWorkloadLogs('pods')).toBe(false)
  })

  it('offers a rollout history only where the controller keeps one', () => {
    for (const key of ['deployments', 'statefulsets', 'daemonsets']) {
      expect(supportsRolloutHistory(key)).toBe(true)
    }
    for (const key of ['jobs', 'cronjobs', 'replicasets', 'pods']) {
      expect(supportsRolloutHistory(key)).toBe(false)
    }
  })

  it('asks about pod labels for the kinds that carry them, Pods included', () => {
    for (const key of ['pods', 'deployments', 'statefulsets', 'daemonsets', 'jobs']) {
      expect(hasPodLabels(key)).toBe(true)
    }
    expect(hasPodLabels('cronjobs')).toBe(false)
    expect(hasPodLabels('services')).toBe(false)
  })
})

describe('workloadKeyFor', () => {
  it('turns the Kind a row carries into the key the API is addressed by', () => {
    expect(workloadKeyFor('Deployment')).toBe('deployments')
    expect(workloadKeyFor('StatefulSet')).toBe('statefulsets')
    expect(workloadKeyFor('DaemonSet')).toBe('daemonsets')
    // Only the kinds the lifecycle table answers for: a Job is not one of them,
    // and neither is anything the table has never heard of.
    expect(workloadKeyFor('Job')).toBeUndefined()
    expect(workloadKeyFor('Kafka')).toBeUndefined()
  })
})

describe('supportsCordon', () => {
  it("is the Node's alone", () => {
    expect(supportsCordon('nodes')).toBe(true)
    expect(supportsCordon('pods')).toBe(false)
    expect(supportsCordon('deployments')).toBe(false)
  })
})
