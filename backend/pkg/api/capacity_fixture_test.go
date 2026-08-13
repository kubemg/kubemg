package api

import (
	"encoding/json"
	"os"
	"testing"
)

/*
 * The same arithmetic, over a real API server's own output.
 *
 * The tests beside this one build their pods by hand, which pins the formula
 * but cannot pin the decode: a field renamed, a `restartPolicy` that arrives
 * somewhere other than where this code looks for it, a quantity in a form
 * apimachinery spells differently — none of that shows up against a struct
 * literal. So these two files are a captured `/api/v1/nodes` and
 * `/api/v1/pods` from a live cluster (minikube, Kubernetes v1.35), carrying a
 * deployment with a native sidecar, a plain init container that requests more
 * than either, and a pod the scheduler could not place.
 *
 * The expected numbers are not this package's own: they are what
 * `kubectl describe node` printed for the same cluster at the same moment, and
 * that command computes them with Kubernetes' own resource helper. Matching it
 * is the point — a capacity view that disagrees with `kubectl describe node`
 * is wrong no matter how internally consistent it is.
 *
 * The fixtures are trimmed of what nothing here reads (managedFields,
 * annotations, volumes, container statuses) and of nothing else.
 */

// What `kubectl describe node minikube` reported for this capture:
//
//	Resource  Requests      Limits
//	cpu       2100m (17%)   3200m (26%)
//	memory    2882Mi (18%)  1514Mi (9%)
const (
	fixtureCPURequests = 2100
	fixtureCPULimits   = 3200
	fixtureMemRequests = 2882 // Mi
	fixtureMemLimits   = 1514 // Mi
)

func readFixture[T any](t *testing.T, path string) T {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading %s: %v", path, err)
	}
	var out T
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatalf("decoding %s: %v", path, err)
	}
	return out
}

func TestBuildCapacityMatchesKubectlDescribeNode(t *testing.T) {
	nodes := readFixture[nodeList](t, "testdata/capacity/nodes.json").records()
	pods := readFixture[capacityPodList](t, "testdata/capacity/pods.json").Items

	if len(nodes) != 1 {
		t.Fatalf("the capture is a single-node cluster, got %d nodes", len(nodes))
	}
	rows, summary, unscheduled := buildCapacity(nodes, pods, nil)
	node := rows[0]

	// The decode first: a node whose allocatable did not come through would
	// make every assertion below vacuously true.
	if node.CPU.Allocatable != 12_000 || node.Pods.Allocatable != 110 {
		t.Fatalf("allocatable decoded as %d millicores / %d pod slots, want 12000 / 110",
			node.CPU.Allocatable, node.Pods.Allocatable)
	}
	if !node.Ready || !node.Schedulable {
		t.Errorf("the captured node was Ready and uncordoned, got ready=%v schedulable=%v",
			node.Ready, node.Schedulable)
	}

	if node.CPU.Requested != fixtureCPURequests {
		t.Errorf("cpu requested = %dm, want %dm — this is what kubectl describe node reports, "+
			"and the sidecar in the capture is why summing the containers is not enough",
			node.CPU.Requested, fixtureCPURequests)
	}
	if node.CPU.Limited != fixtureCPULimits {
		t.Errorf("cpu limits = %dm, want %dm", node.CPU.Limited, fixtureCPULimits)
	}
	if got := mebibytes(node.Memory.Requested); got != fixtureMemRequests {
		t.Errorf("memory requested = %dMi, want %dMi", got, fixtureMemRequests)
	}
	if got := mebibytes(node.Memory.Limited); got != fixtureMemLimits {
		t.Errorf("memory limits = %dMi, want %dMi", got, fixtureMemLimits)
	}

	// The capture holds one pod the scheduler refused, which is the other half
	// of an oversubscription report.
	if unscheduled.count != 1 {
		t.Errorf("the capture has one unplaceable pod, got %d", unscheduled.count)
	}
	if len(unscheduled.sample) == 1 && unscheduled.sample[0].Reason == "" {
		t.Error("the scheduler's own explanation must survive the decode")
	}

	// And the summary of a single-node cluster is that node.
	if summary.CPU.Requested != node.CPU.Requested || summary.Nodes != 1 {
		t.Errorf("summary disagrees with its only node: %+v", summary.CPU)
	}
}

// The sidecar case, against the real object rather than a struct literal: a
// pod whose init containers request more than its app containers, one of which
// keeps running afterwards.
func TestFixtureSidecarPodDemand(t *testing.T) {
	pods := readFixture[capacityPodList](t, "testdata/capacity/pods.json").Items

	var found bool
	for _, pod := range pods {
		if pod.Metadata.Namespace != "capacity-check" ||
			len(pod.Spec.InitContainers) == 0 {
			continue
		}
		found = true

		got := demandOf(pod)
		// app 150m, sidecar 100m, migrate 500m. Running is 250m; the migration
		// peaks at 600m on top of the sidecar already up, and 600m is what the
		// node has to have free.
		if got.cpuRequest != 600 {
			t.Errorf("%s: cpu request = %dm, want 600m", pod.Metadata.Name, got.cpuRequest)
		}
		// Only the sidecar declares limits, and the finished init container's
		// are ignored, so 200m is the whole ceiling.
		if got.cpuLimit != 200 {
			t.Errorf("%s: cpu limit = %dm, want 200m", pod.Metadata.Name, got.cpuLimit)
		}
		// The app container declares no limit; the sidecar does.
		if got.cpuUnlimited != 1 {
			t.Errorf("%s: containers with no cpu limit = %d, want 1",
				pod.Metadata.Name, got.cpuUnlimited)
		}
	}

	if !found {
		t.Fatal("the capture must carry the sidecar workload it was taken for")
	}
}

func mebibytes(bytes int64) int64 { return bytes / (1 << 20) }
