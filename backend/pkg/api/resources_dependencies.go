package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"slices"
	"strings"

	"github.com/gin-gonic/gin"
)

/*
 * What a workload needs in order to start.
 *
 * A pod that never starts is, more often than anything else, a pod whose spec
 * names something that is not there: a ConfigMap that was renamed, a key that
 * was dropped from it, a Secret that was never created in this namespace, a
 * volume claim that never bound. The pod says so in one line of a container's
 * waiting message, and the object it names is one `kubectl get` away — but
 * only once somebody thinks to look.
 *
 * This is the traffic map's other half: the same resolver, reading the pod
 * template instead of a route. Every ConfigMap, Secret, ServiceAccount and
 * PersistentVolumeClaim the template names becomes a node, with how it is used
 * on the edge — `env DB_HOST`, `volume config → /etc/app`, `image pull
 * secret` — and a claim is followed to the PersistentVolume it bound.
 *
 * A Secret is still never read. Whether it exists is learnt from the pods
 * themselves: a container waiting on `secret "db" not found` is the cluster
 * saying so, and a Secret no pod complains about is drawn as named, not as
 * checked. That keeps the traffic map's rule — looking at a workload must not
 * put a Secret `get` in the audit trail — and costs only the case of a Secret
 * volume that has not been mounted yet, which the notes say.
 */

// The kinds a dependency map can be asked for, keyed by sidebar key.
var dependencyRootKinds = map[string]string{
	"deployments":  "Deployment",
	"statefulsets": "StatefulSet",
	"daemonsets":   "DaemonSet",
	"replicasets":  "ReplicaSet",
	"jobs":         "Job",
	"cronjobs":     "CronJob",
	"pods":         "Pod",
}

const (
	dependencyColumnWorkload = iota
	dependencyColumnUses
	dependencyColumnVolume
)

var dependencyColumns = []string{"Workload", "Uses", "Bound volume"}

// maxDependencies bounds the nodes one map draws: a pod template naming fifty
// ConfigMaps is asking for a list, not a drawing.
const maxDependencies = 40

// maxClaimOrdinals is how many of a StatefulSet's per-replica claims are
// drawn, for the reason pods are folded: the first few say what the rest are.
const maxClaimOrdinals = 4

type keyReference struct {
	Name     string `json:"name"`
	Key      string `json:"key"`
	Optional *bool  `json:"optional"`
}

type nameReference struct {
	Name     string `json:"name"`
	Optional *bool  `json:"optional"`
}

type containerReferences struct {
	Name string `json:"name"`
	Env  []struct {
		Name      string `json:"name"`
		ValueFrom *struct {
			ConfigMapKeyRef *keyReference `json:"configMapKeyRef"`
			SecretKeyRef    *keyReference `json:"secretKeyRef"`
		} `json:"valueFrom"`
	} `json:"env"`
	EnvFrom []struct {
		ConfigMapRef *nameReference `json:"configMapRef"`
		SecretRef    *nameReference `json:"secretRef"`
	} `json:"envFrom"`
	VolumeMounts []struct {
		Name      string `json:"name"`
		MountPath string `json:"mountPath"`
	} `json:"volumeMounts"`
}

// podSpecReferences is the part of a pod spec that names other objects.
type podSpecReferences struct {
	ServiceAccountName string `json:"serviceAccountName"`
	ImagePullSecrets   []struct {
		Name string `json:"name"`
	} `json:"imagePullSecrets"`
	Volumes []struct {
		Name      string         `json:"name"`
		ConfigMap *nameReference `json:"configMap"`
		Secret    *struct {
			SecretName string `json:"secretName"`
			Optional   *bool  `json:"optional"`
		} `json:"secret"`
		PersistentVolumeClaim *struct {
			ClaimName string `json:"claimName"`
		} `json:"persistentVolumeClaim"`
		Projected *struct {
			Sources []struct {
				ConfigMap *nameReference `json:"configMap"`
				Secret    *nameReference `json:"secret"`
			} `json:"sources"`
		} `json:"projected"`
	} `json:"volumes"`
	Containers     []containerReferences `json:"containers"`
	InitContainers []containerReferences `json:"initContainers"`
}

// dependencyUse is one way a template names one object.
type dependencyUse struct {
	kind     string
	name     string
	label    string
	key      string
	optional bool
}

func (u dependencyUse) id() string {
	return dependencyResource(u.kind) + "/" + u.name
}

func dependencyResource(kind string) string {
	switch kind {
	case "ConfigMap":
		return "configmaps"
	case "Secret":
		return "secrets"
	case "ServiceAccount":
		return "serviceaccounts"
	case "PersistentVolumeClaim":
		return "persistentvolumeclaims"
	}
	return ""
}

// uses lists every object a pod spec names, in the order the spec names them.
func (spec podSpecReferences) uses() []dependencyUse {
	var out []dependencyUse
	mounts := map[string][]string{}
	containers := slices.Concat(spec.InitContainers, spec.Containers)
	for _, container := range containers {
		for _, mount := range container.VolumeMounts {
			if !slices.Contains(mounts[mount.Name], mount.MountPath) {
				mounts[mount.Name] = append(mounts[mount.Name], mount.MountPath)
			}
		}
	}
	volumeLabel := func(name string) string {
		if paths := mounts[name]; len(paths) > 0 {
			return "volume " + name + " → " + strings.Join(paths, ", ")
		}
		return "volume " + name
	}

	account := cmpOr(spec.ServiceAccountName, "default")
	out = append(out, dependencyUse{kind: "ServiceAccount", name: account, label: "service account"})
	for _, secret := range spec.ImagePullSecrets {
		out = append(out, dependencyUse{kind: "Secret", name: secret.Name, label: "image pull secret"})
	}
	for _, volume := range spec.Volumes {
		switch {
		case volume.ConfigMap != nil:
			out = append(out, dependencyUse{kind: "ConfigMap", name: volume.ConfigMap.Name,
				label: volumeLabel(volume.Name), optional: deref(volume.ConfigMap.Optional, false)})
		case volume.Secret != nil:
			out = append(out, dependencyUse{kind: "Secret", name: volume.Secret.SecretName,
				label: volumeLabel(volume.Name), optional: deref(volume.Secret.Optional, false)})
		case volume.PersistentVolumeClaim != nil:
			out = append(out, dependencyUse{kind: "PersistentVolumeClaim",
				name: volume.PersistentVolumeClaim.ClaimName, label: volumeLabel(volume.Name)})
		case volume.Projected != nil:
			for _, source := range volume.Projected.Sources {
				if source.ConfigMap != nil {
					out = append(out, dependencyUse{kind: "ConfigMap", name: source.ConfigMap.Name,
						label: volumeLabel(volume.Name), optional: deref(source.ConfigMap.Optional, false)})
				}
				if source.Secret != nil {
					out = append(out, dependencyUse{kind: "Secret", name: source.Secret.Name,
						label: volumeLabel(volume.Name), optional: deref(source.Secret.Optional, false)})
				}
			}
		}
	}
	for _, container := range containers {
		for _, env := range container.Env {
			if env.ValueFrom == nil {
				continue
			}
			if ref := env.ValueFrom.ConfigMapKeyRef; ref != nil {
				out = append(out, dependencyUse{kind: "ConfigMap", name: ref.Name, label: "env " + env.Name,
					key: ref.Key, optional: deref(ref.Optional, false)})
			}
			if ref := env.ValueFrom.SecretKeyRef; ref != nil {
				out = append(out, dependencyUse{kind: "Secret", name: ref.Name, label: "env " + env.Name,
					key: ref.Key, optional: deref(ref.Optional, false)})
			}
		}
		for _, from := range container.EnvFrom {
			if from.ConfigMapRef != nil {
				out = append(out, dependencyUse{kind: "ConfigMap", name: from.ConfigMapRef.Name,
					label: "env from every key", optional: deref(from.ConfigMapRef.Optional, false)})
			}
			if from.SecretRef != nil {
				out = append(out, dependencyUse{kind: "Secret", name: from.SecretRef.Name,
					label: "env from every key", optional: deref(from.SecretRef.Optional, false)})
			}
		}
	}
	return out
}

// workloadTemplate is a workload as the dependency map reads it: the pod
// template, wherever the kind keeps it, and what selects its pods.
type workloadTemplate struct {
	Metadata objectMeta `json:"metadata"`
	Spec     struct {
		Replicas *int32         `json:"replicas"`
		Selector *labelSelector `json:"selector"`
		Template struct {
			Spec podSpecReferences `json:"spec"`
		} `json:"template"`
		JobTemplate struct {
			Spec struct {
				Template struct {
					Spec podSpecReferences `json:"spec"`
				} `json:"template"`
			} `json:"spec"`
		} `json:"jobTemplate"`
		VolumeClaimTemplates []struct {
			Metadata objectMeta `json:"metadata"`
		} `json:"volumeClaimTemplates"`
	} `json:"spec"`
}

// The two sentences a container's waiting message uses for a Secret, which is
// how a missing one is learnt without reading it.
var (
	secretMissing    = regexp.MustCompile(`secret "([^"]+)" not found`)
	secretKeyMissing = regexp.MustCompile(`couldn't find key (\S+) in Secret [^/\s]+/(\S+)`)
)

// showDependencyMap answers GET .../resources/dependencies?kind=&namespace=&name=.
func (s *server) showDependencyMap(c *gin.Context) {
	user, cluster, grant, ok := s.resourceCluster(c)
	if !ok {
		return
	}
	key := strings.TrimSpace(c.Query("kind"))
	if _, known := dependencyRootKinds[key]; !known {
		c.JSON(http.StatusBadRequest, gin.H{"error": "kubemg draws a dependency map for workloads and pods only"})
		return
	}
	name := strings.TrimSpace(c.Query("name"))
	if name == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "a resource name is required"})
		return
	}
	namespace, ok := s.resourceNamespace(c, grant)
	if !ok {
		return
	}

	resolver := newTrafficResolver(tunnelReader{s: s, c: c, user: user, cluster: cluster, grant: grant},
		grant.NamespaceList())
	if failure := resolver.resolveDependencies(key, namespace, name); failure != nil {
		c.JSON(failure.status, gin.H{"error": failure.problem})
		return
	}
	c.JSON(http.StatusOK, resolver.out)
}

func (r *trafficResolver) resolveDependencies(key, namespace, name string) *readFailure {
	r.out.Columns = dependencyColumns
	kind := dependencyRootKinds[key]

	var raw json.RawMessage
	if failure := r.object(namespace, objectKinds[key].versions, name, &raw); failure != nil {
		return failure
	}
	var spec podSpecReferences
	var workload workloadTemplate
	var pods []json.RawMessage
	if key == "pods" {
		var pod struct {
			Spec podSpecReferences `json:"spec"`
		}
		if json.Unmarshal(raw, &pod) != nil {
			return &readFailure{trafficUnchecked, "the cluster returned an unreadable response", http.StatusBadGateway}
		}
		spec, pods = pod.Spec, []json.RawMessage{raw}
	} else {
		if json.Unmarshal(raw, &workload) != nil {
			return &readFailure{trafficUnchecked, "the cluster returned an unreadable response", http.StatusBadGateway}
		}
		spec = workload.Spec.Template.Spec
		if key == "cronjobs" {
			spec = workload.Spec.JobTemplate.Spec.Template.Spec
		}
		pods = r.selectedPods(namespace, workload.Spec.Selector)
	}

	root := trafficID(key, namespace, name)
	r.out.Root = root
	node := trafficNode{ID: root, Kind: kind, Resource: key, Namespace: namespace, Name: name,
		Column: dependencyColumnWorkload, State: trafficOK}
	if key != "pods" && key != "cronjobs" {
		ready := 0
		for _, pod := range pods {
			var object podObject
			if json.Unmarshal(pod, &object) == nil && podReady(object.view()) {
				ready++
			}
		}
		node.Detail = []string{fmt.Sprintf("%d of %d pods ready", ready, len(pods))}
	}
	r.addNode(node)

	uses := spec.uses()
	if key == "statefulsets" {
		uses = append(uses, workload.claimTemplateUses(name)...)
	}
	r.drawUses(root, namespace, uses, waitingMessages(pods))

	r.note("A Secret is named, never read: a missing one is drawn as broken once a pod reports it, and a Secret volume a pod has not tried to mount yet cannot be told apart from one that exists.")
	return nil
}

// claimTemplateUses names the claims a StatefulSet's volumeClaimTemplates
// make, one per replica, as `<template>-<statefulset>-<ordinal>`.
func (w workloadTemplate) claimTemplateUses(name string) []dependencyUse {
	replicas := int(deref(w.Spec.Replicas, 1))
	var out []dependencyUse
	for _, template := range w.Spec.VolumeClaimTemplates {
		for ordinal := 0; ordinal < min(replicas, maxClaimOrdinals); ordinal++ {
			out = append(out, dependencyUse{kind: "PersistentVolumeClaim",
				name:  fmt.Sprintf("%s-%s-%d", template.Metadata.Name, name, ordinal),
				label: "claim template " + template.Metadata.Name})
		}
	}
	return out
}

// selectedPods lists a workload's pods by its own selector, for what their
// containers say; nil for a workload with no selector to read.
func (r *trafficResolver) selectedPods(namespace string, selector *labelSelector) []json.RawMessage {
	if selector == nil {
		return nil
	}
	encoded, err := encodeLabelSelector(*selector)
	if err != nil {
		return nil
	}
	items, _, failure := r.list(namespace, []resourceListPath{{"/api/v1", "pods"}}, encoded)
	if failure != nil {
		return nil
	}
	return items
}

// waitingMessages is every container waiting message across the pods: where a
// missing Secret or key is written down.
func waitingMessages(pods []json.RawMessage) []string {
	var out []string
	for _, raw := range pods {
		var pod struct {
			Status struct {
				ContainerStatuses     []containerWaiting `json:"containerStatuses"`
				InitContainerStatuses []containerWaiting `json:"initContainerStatuses"`
			} `json:"status"`
		}
		if json.Unmarshal(raw, &pod) != nil {
			continue
		}
		for _, status := range slices.Concat(pod.Status.InitContainerStatuses, pod.Status.ContainerStatuses) {
			if waiting := status.State.Waiting; waiting != nil && waiting.Message != "" {
				out = append(out, waiting.Message)
			}
		}
	}
	return out
}

type containerWaiting struct {
	State struct {
		Waiting *struct {
			Reason  string `json:"reason"`
			Message string `json:"message"`
		} `json:"waiting"`
	} `json:"state"`
}

// drawUses draws one node per named object and one edge per object, labelled
// with every way the template uses it.
func (r *trafficResolver) drawUses(root, namespace string, uses []dependencyUse, messages []string) {
	grouped := map[string][]dependencyUse{}
	var order []string
	for _, use := range uses {
		if use.name == "" {
			continue
		}
		if _, seen := grouped[use.id()]; !seen {
			order = append(order, use.id())
		}
		grouped[use.id()] = append(grouped[use.id()], use)
	}
	if len(order) > maxDependencies {
		r.note(fmt.Sprintf("This template names %d objects; the map draws the first %d.", len(order), maxDependencies))
		order = order[:maxDependencies]
	}

	for _, id := range order {
		group := grouped[id]
		first := group[0]
		optional := true
		for _, use := range group {
			optional = optional && use.optional
		}
		nodeID := trafficID(dependencyResource(first.kind), namespace, first.name)
		node := trafficNode{ID: nodeID, Kind: first.kind, Resource: dependencyResource(first.kind),
			Namespace: namespace, Name: first.name, Column: dependencyColumnUses, State: trafficOK}

		// keyProblems are the uses naming a key the object does not have.
		keyProblem := map[string]string{}
		switch first.kind {
		case "ConfigMap":
			keys, failure := r.configMapKeys(namespace, first.name)
			switch {
			case failure != nil && failure.status == http.StatusNotFound && optional:
				node.State, node.Problem = trafficWarn, "absent — every reference to it is optional, so the pods start without it"
			case failure != nil && failure.status == http.StatusNotFound:
				node.State, node.Problem = trafficBad, "this ConfigMap does not exist"
			case failure != nil:
				node.State, node.Problem = failure.state, failure.problem
			default:
				node.Detail = []string{fmt.Sprintf("%d keys", len(keys))}
				for _, use := range group {
					if use.key != "" && !slices.Contains(keys, use.key) && !use.optional {
						keyProblem[use.label] = fmt.Sprintf("key %s is not in ConfigMap %s", use.key, first.name)
					}
				}
			}
		case "Secret":
			node.State = trafficUnchecked
			node.Detail = []string{"named only — never read"}
			for _, message := range messages {
				if match := secretMissing.FindStringSubmatch(message); match != nil && match[1] == first.name && !optional {
					node.State, node.Problem = trafficBad, "a pod reports it missing: "+message
				}
				if match := secretKeyMissing.FindStringSubmatch(message); match != nil && match[2] == first.name {
					for _, use := range group {
						if use.key == match[1] {
							keyProblem[use.label] = fmt.Sprintf("key %s is not in Secret %s", use.key, first.name)
						}
					}
				}
			}
		case "ServiceAccount":
			var account struct{}
			if failure := r.object(namespace, objectKinds["serviceaccounts"].versions, first.name, &account); failure != nil {
				node.State, node.Problem = failure.state, failure.problem
				if failure.status == http.StatusNotFound {
					node.Problem = "this ServiceAccount does not exist — the controller cannot create the pods"
				}
			}
		case "PersistentVolumeClaim":
			r.drawClaim(&node, namespace, first.name)
		}
		r.addNode(node)

		labels := make([]string, 0, len(group))
		for _, use := range group {
			if !slices.Contains(labels, use.label) {
				labels = append(labels, use.label)
			}
		}
		edgeState, edgeProblem := trafficOK, ""
		if node.State == trafficBad {
			edgeState, edgeProblem = trafficBad, node.Problem
		} else if node.State == trafficWarn {
			edgeState = trafficWarn
		} else if node.State == trafficUnchecked || node.State == trafficDenied || node.State == trafficOutside {
			edgeState = trafficUnchecked
		}
		for _, label := range labels {
			state, problem := edgeState, edgeProblem
			if keyed, broken := keyProblem[label]; broken {
				state, problem = trafficBad, keyed
			}
			r.addEdge(root, nodeID, label, state, problem)
		}
	}
}

// configMapKeys reads a ConfigMap for the names of its keys — never the values,
// which are read only as far as the tunnel and dropped here.
func (r *trafficResolver) configMapKeys(namespace, name string) ([]string, *readFailure) {
	var object struct {
		Data       map[string]json.RawMessage `json:"data"`
		BinaryData map[string]json.RawMessage `json:"binaryData"`
	}
	if failure := r.object(namespace, objectKinds["configmaps"].versions, name, &object); failure != nil {
		return nil, failure
	}
	keys := make([]string, 0, len(object.Data)+len(object.BinaryData))
	for key := range object.Data {
		keys = append(keys, key)
	}
	for key := range object.BinaryData {
		keys = append(keys, key)
	}
	slices.Sort(keys)
	return keys, nil
}

// drawClaim reads a claim and follows it to the PersistentVolume it bound.
func (r *trafficResolver) drawClaim(node *trafficNode, namespace, name string) {
	var claim struct {
		Spec struct {
			StorageClassName *string `json:"storageClassName"`
			VolumeName       string  `json:"volumeName"`
			Resources        struct {
				Requests map[string]string `json:"requests"`
			} `json:"resources"`
		} `json:"spec"`
		Status struct {
			Phase string `json:"phase"`
		} `json:"status"`
	}
	if failure := r.object(namespace, objectKinds["persistentvolumeclaims"].versions, name, &claim); failure != nil {
		node.State, node.Problem = failure.state, failure.problem
		if failure.status == http.StatusNotFound {
			node.Problem = "this claim does not exist — a pod that mounts it stays Pending"
		}
		return
	}
	node.Detail = []string{strings.TrimSpace(fmt.Sprintf("%s %s %s",
		claim.Status.Phase, claim.Spec.Resources.Requests["storage"], deref(claim.Spec.StorageClassName, "")))}
	switch claim.Status.Phase {
	case "Pending":
		node.State, node.Problem = trafficWarn, "Pending — not bound to a volume yet"
		return
	case "Lost":
		node.State, node.Problem = trafficBad, "Lost — the volume it was bound to is gone"
		return
	}
	if claim.Spec.VolumeName == "" {
		return
	}

	volumeID := trafficID("persistentvolumes", "", claim.Spec.VolumeName)
	volume := trafficNode{ID: volumeID, Kind: "PersistentVolume", Resource: "persistentvolumes",
		Name: claim.Spec.VolumeName, Column: dependencyColumnVolume, State: trafficOK}
	var pv struct {
		Spec struct {
			Capacity                      map[string]string `json:"capacity"`
			PersistentVolumeReclaimPolicy string            `json:"persistentVolumeReclaimPolicy"`
		} `json:"spec"`
		Status struct {
			Phase string `json:"phase"`
		} `json:"status"`
	}
	if failure := r.clusterObject("/api/v1/persistentvolumes/"+url.PathEscape(claim.Spec.VolumeName), &pv); failure != nil {
		volume.State, volume.Problem = failure.state, failure.problem
	} else {
		volume.Detail = []string{strings.TrimSpace(fmt.Sprintf("%s %s · reclaim %s",
			pv.Status.Phase, pv.Spec.Capacity["storage"], pv.Spec.PersistentVolumeReclaimPolicy))}
		if pv.Status.Phase == "Failed" || pv.Status.Phase == "Released" {
			volume.State, volume.Problem = trafficBad, pv.Status.Phase+" — no longer bound to this claim"
		}
	}
	r.addNode(volume)
	r.addEdge(node.ID, volumeID, "", trafficOK, "")
}

// clusterObject reads a cluster-scoped object. A namespace-scoped grant never
// reaches one — the inventory refuses those lists for the same reason — so for
// such a grant it is drawn outside and not asked for.
func (r *trafficResolver) clusterObject(path string, out any) *readFailure {
	if len(r.allowed) > 0 {
		return &readFailure{trafficOutside,
			"cluster-scoped — outside a namespace-scoped grant, so kubemg did not read it", http.StatusForbidden}
	}
	status, body, err := r.read.get(path)
	if err != nil {
		return &readFailure{trafficUnchecked, "could not read from the cluster", http.StatusBadGateway}
	}
	if failure := statusFailure(status, body); failure != nil {
		return failure
	}
	if err := json.Unmarshal(body, out); err != nil {
		return &readFailure{trafficUnchecked, "the cluster returned an unreadable response", http.StatusBadGateway}
	}
	return nil
}
