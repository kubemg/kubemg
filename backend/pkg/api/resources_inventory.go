package api

import (
	"fmt"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/kubemg/kubemg/backend/pkg/cronsched"
	"github.com/kubemg/kubemg/backend/pkg/db"
)

// The resource inventory behind the Explore sidebar. Everything here follows the
// rules resources.go already sets: reads go through bastion.Proxy.Call, so they
// carry the caller's impersonated identity, obey the same namespace scope and
// land in the same audit trail. Each list is normalised into the few columns a
// list view actually shows, because the browser should not have to know fifteen
// Kubernetes list shapes and a slow link should not carry fifteen full objects.

// resourceListPath is a candidate API path for a list. Optional resources are
// declared as several candidates so a cluster serving an older API version of a
// CRD still answers.
type resourceListPath struct {
	// group is the API group path, e.g. "/apis/gateway.networking.k8s.io/v1".
	group string
	// resource is the plural resource name.
	resource string
}

// namespaced renders the path for a namespaced list.
func (p resourceListPath) namespaced(namespace string) string {
	return fmt.Sprintf("%s/namespaces/%s/%s", p.group, url.PathEscape(namespace), p.resource)
}

// clusterWide renders the path for a cluster-scoped list.
func (p resourceListPath) clusterWide() string {
	return fmt.Sprintf("%s/%s", p.group, p.resource)
}

// candidates renders one candidate group per namespace a scope reads: the same
// list at every API version worth trying, in preference order.
func (r readScope) candidates(versions ...resourceListPath) [][]string {
	if len(r.Namespaces) == 0 {
		group := make([]string, 0, len(versions))
		for _, version := range versions {
			group = append(group, version.clusterWide())
		}
		return [][]string{group}
	}

	out := make([][]string, 0, len(r.Namespaces))
	for _, namespace := range r.Namespaces {
		group := make([]string, 0, len(versions))
		for _, version := range versions {
			group = append(group, version.namespaced(namespace))
		}
		out = append(out, group)
	}
	return out
}

// listMeta is the metadata every normalised list carries.
type listMeta struct {
	Name      string    `json:"name"`
	Namespace string    `json:"namespace,omitempty"`
	Created   time.Time `json:"created_at"`
}

// objectMeta is the slice of a Kubernetes object's metadata every view needs.
type objectMeta struct {
	Name              string            `json:"name"`
	Namespace         string            `json:"namespace"`
	CreationTimestamp time.Time         `json:"creationTimestamp"`
	Labels            map[string]string `json:"labels"`
	Annotations       map[string]string `json:"annotations"`
}

func (m objectMeta) meta() listMeta {
	return listMeta{Name: m.Name, Namespace: m.Namespace, Created: m.CreationTimestamp}
}

// Every normalised view embeds listMeta, so every list sorts by namespace then
// name without each handler saying so.
func (m listMeta) sortKey() (string, string) { return m.Namespace, m.Name }

// requireClusterScope refuses a cluster-scoped read for a namespace-scoped
// grant. The proxy would refuse it too — it is the enforcement point — but a
// scoped caller deserves to be told why rather than seeing a generic refusal
// from a list they cannot ever be shown.
func (s *server) requireClusterScope(c *gin.Context, grant db.UserClusterAccess, resource string) bool {
	allowed := grant.NamespaceList()
	if len(allowed) == 0 {
		return true
	}
	c.JSON(http.StatusForbidden, gin.H{
		"error": resource + " are not namespaced, and your access to this cluster is limited to " +
			strings.Join(allowed, ", "),
	})
	return false
}

// fetchOptional reads the first candidate path that answers. A 404 means the
// resource is not served by this cluster at all — an uninstalled Gateway API or
// Istio — which is an answer, not a failure: the caller reports it as an empty
// list the UI can label. Any other refusal is passed through as itself.
func (s *server) fetchOptional(c *gin.Context, user *db.User, cluster *db.Cluster,
	grant db.UserClusterAccess, paths []string, out any,
) (found bool, ok bool) {
	for _, path := range paths {
		resp, callOK := s.callResource(c, user, cluster, grant, path)
		if !callOK {
			return false, false
		}
		if resp.Status == http.StatusNotFound {
			continue
		}
		if !s.decodeResource(c, resp, out) {
			return false, false
		}
		return true, true
	}
	return false, true
}

/* ------------------------------------------------------------- workloads --- */

type jobView struct {
	listMeta
	Completions int32    `json:"completions"`
	Succeeded   int32    `json:"succeeded"`
	Failed      int32    `json:"failed"`
	Active      int32    `json:"active"`
	State       string   `json:"state"`
	Images      []string `json:"images"`
}

type cronJobView struct {
	listMeta
	Schedule     string     `json:"schedule"`
	Suspended    bool       `json:"suspended"`
	Active       int        `json:"active"`
	LastSchedule *time.Time `json:"last_schedule_at,omitempty"`

	// NextSchedule is the firing this build derived from the schedule, which no
	// Kubernetes field reports — see pkg/cronsched. It is absent for three
	// different reasons and each is stated rather than collapsed into a blank:
	// a suspended CronJob has no next run, a schedule this build cannot read
	// says so in ScheduleError, and a valid expression that never fires again
	// (`0 0 31 2 *`) has neither.
	NextSchedule  *time.Time `json:"next_schedule_at,omitempty"`
	TimeZone      string     `json:"time_zone,omitempty"`
	ScheduleError string     `json:"schedule_error,omitempty"`
}

// listWorkloadsOf serves one apps/v1 kind on its own route, reusing the same
// normalisation as the combined workloads list.
func (s *server) listWorkloadsOf(kind string) gin.HandlerFunc {
	return func(c *gin.Context) {
		user, cluster, grant, ok := s.resourceCluster(c)
		if !ok {
			return
		}
		scope, ok := s.resourceScope(c, grant)
		if !ok {
			return
		}

		out, ok := s.collectWorkloads(c, user, cluster, grant, scope, kind)
		if !ok {
			return
		}
		c.JSON(http.StatusOK, gin.H{
			"workloads":      out,
			"namespace":      scope.Namespace,
			"all_namespaces": scope.All,
		})
	}
}

func (s *server) listJobs(c *gin.Context) {
	user, cluster, grant, ok := s.resourceCluster(c)
	if !ok {
		return
	}
	scope, ok := s.resourceScope(c, grant)
	if !ok {
		return
	}

	out := []jobView{}
	for _, path := range scope.paths(resourceListPath{"/apis/batch/v1", "jobs"}) {
		var list struct {
			Items []struct {
				Metadata objectMeta `json:"metadata"`
				Spec     struct {
					Completions *int32 `json:"completions"`
					Suspend     *bool  `json:"suspend"`
					Template    struct {
						Spec struct {
							Containers []struct {
								Image string `json:"image"`
							} `json:"containers"`
						} `json:"spec"`
					} `json:"template"`
				} `json:"spec"`
				Status struct {
					Active     int32 `json:"active"`
					Succeeded  int32 `json:"succeeded"`
					Failed     int32 `json:"failed"`
					Conditions []struct {
						Type   string `json:"type"`
						Status string `json:"status"`
					} `json:"conditions"`
				} `json:"status"`
			} `json:"items"`
		}
		if !s.fetch(c, user, cluster, grant, path, &list) {
			return
		}

		for _, item := range list.Items {
			view := jobView{
				listMeta:  item.Metadata.meta(),
				Succeeded: item.Status.Succeeded,
				Failed:    item.Status.Failed,
				Active:    item.Status.Active,
				// A job with no completions target runs exactly once.
				Completions: 1,
			}
			if item.Spec.Completions != nil {
				view.Completions = *item.Spec.Completions
			}
			for _, container := range item.Spec.Template.Spec.Containers {
				view.Images = append(view.Images, container.Image)
			}

			switch {
			case item.Spec.Suspend != nil && *item.Spec.Suspend:
				view.State = "Suspended"
			case item.Status.Active > 0:
				view.State = "Running"
			default:
				view.State = "Pending"
			}
			for _, condition := range item.Status.Conditions {
				if condition.Status != "True" {
					continue
				}
				if condition.Type == "Complete" || condition.Type == "Failed" {
					view.State = condition.Type
				}
			}
			out = append(out, view)
		}
	}

	sortResources(out)
	c.JSON(http.StatusOK, gin.H{
		"jobs":           out,
		"namespace":      scope.Namespace,
		"all_namespaces": scope.All,
	})
}

// cronJobNext derives the firing a CronJob's schedule implies, which is the one
// thing a list of them cannot read off the object. It never fails: a schedule
// this build cannot evaluate comes back as a reason to show on the row, because
// one unreadable expression must not cost the operator the whole list.
func cronJobNext(schedule, timeZone string, suspended bool, last *time.Time, now time.Time) (*time.Time, string) {
	// A suspended CronJob is not going to run, so deriving a time for it would
	// be a countdown to something that never happens.
	if suspended {
		return nil, ""
	}

	var lastRun time.Time
	if last != nil {
		lastRun = *last
	}

	next, err := cronsched.NextIn(schedule, timeZone, now, lastRun)
	if err != nil {
		return nil, strings.TrimPrefix(err.Error(), cronsched.ErrUnsupported.Error()+": ")
	}
	// A valid expression can still have no firing left within the search
	// horizon; that is silence rather than an error.
	if next.IsZero() {
		return nil, ""
	}
	return &next, ""
}

func (s *server) listCronJobs(c *gin.Context) {
	user, cluster, grant, ok := s.resourceCluster(c)
	if !ok {
		return
	}
	scope, ok := s.resourceScope(c, grant)
	if !ok {
		return
	}

	// One clock for the whole list: two rows on the same schedule must not be
	// answered a millisecond apart and report different countdowns.
	now := time.Now()

	out := []cronJobView{}
	for _, path := range scope.paths(resourceListPath{"/apis/batch/v1", "cronjobs"}) {
		var list struct {
			Items []struct {
				Metadata objectMeta `json:"metadata"`
				Spec     struct {
					Schedule string `json:"schedule"`
					Suspend  *bool  `json:"suspend"`
					TimeZone string `json:"timeZone"`
				} `json:"spec"`
				Status struct {
					Active           []struct{} `json:"active"`
					LastScheduleTime *time.Time `json:"lastScheduleTime"`
				} `json:"status"`
			} `json:"items"`
		}
		if !s.fetch(c, user, cluster, grant, path, &list) {
			return
		}

		for _, item := range list.Items {
			view := cronJobView{
				listMeta:     item.Metadata.meta(),
				Schedule:     item.Spec.Schedule,
				Active:       len(item.Status.Active),
				LastSchedule: item.Status.LastScheduleTime,
				TimeZone:     item.Spec.TimeZone,
			}
			if item.Spec.Suspend != nil {
				view.Suspended = *item.Spec.Suspend
			}
			view.NextSchedule, view.ScheduleError = cronJobNext(
				item.Spec.Schedule, item.Spec.TimeZone, view.Suspended,
				item.Status.LastScheduleTime, now)
			out = append(out, view)
		}
	}

	sortResources(out)
	c.JSON(http.StatusOK, gin.H{
		"cronjobs":       out,
		"namespace":      scope.Namespace,
		"all_namespaces": scope.All,
	})
}

/* ------------------------------------------------------------ networking --- */

type serviceView struct {
	listMeta
	Type        string   `json:"type"`
	ClusterIP   string   `json:"cluster_ip"`
	ExternalIPs []string `json:"external_ips"`
	Ports       []string `json:"ports"`
}

type ingressView struct {
	listMeta
	Class     string   `json:"class"`
	Hosts     []string `json:"hosts"`
	Addresses []string `json:"addresses"`
	Rules     int      `json:"rules"`
}

type routeView struct {
	listMeta
	Hostnames []string `json:"hostnames"`
	// Parents are the gateways (Gateway API) or gateways/meshes (Istio) the
	// route attaches to.
	Parents []string `json:"parents"`
	Rules   int      `json:"rules"`
}

func (s *server) listServices(c *gin.Context) {
	user, cluster, grant, ok := s.resourceCluster(c)
	if !ok {
		return
	}
	scope, ok := s.resourceScope(c, grant)
	if !ok {
		return
	}

	out := []serviceView{}
	for _, path := range scope.paths(resourceListPath{"/api/v1", "services"}) {
		var list struct {
			Items []struct {
				Metadata objectMeta `json:"metadata"`
				Spec     struct {
					Type        string   `json:"type"`
					ClusterIP   string   `json:"clusterIP"`
					ExternalIPs []string `json:"externalIPs"`
					Ports       []struct {
						Name     string `json:"name"`
						Port     int32  `json:"port"`
						NodePort int32  `json:"nodePort"`
						Protocol string `json:"protocol"`
					} `json:"ports"`
				} `json:"spec"`
				Status struct {
					LoadBalancer struct {
						Ingress []struct {
							IP       string `json:"ip"`
							Hostname string `json:"hostname"`
						} `json:"ingress"`
					} `json:"loadBalancer"`
				} `json:"status"`
			} `json:"items"`
		}
		if !s.fetch(c, user, cluster, grant, path, &list) {
			return
		}

		for _, item := range list.Items {
			view := serviceView{
				listMeta:    item.Metadata.meta(),
				Type:        item.Spec.Type,
				ClusterIP:   item.Spec.ClusterIP,
				ExternalIPs: item.Spec.ExternalIPs,
			}
			// A LoadBalancer's assigned address matters more than the field it
			// was asked for, so both end up in the same column.
			for _, ingress := range item.Status.LoadBalancer.Ingress {
				if ingress.IP != "" {
					view.ExternalIPs = append(view.ExternalIPs, ingress.IP)
				}
				if ingress.Hostname != "" {
					view.ExternalIPs = append(view.ExternalIPs, ingress.Hostname)
				}
			}
			for _, port := range item.Spec.Ports {
				protocol := port.Protocol
				if protocol == "" {
					protocol = "TCP"
				}
				if port.NodePort > 0 {
					view.Ports = append(view.Ports,
						fmt.Sprintf("%d:%d/%s", port.Port, port.NodePort, protocol))
					continue
				}
				view.Ports = append(view.Ports, fmt.Sprintf("%d/%s", port.Port, protocol))
			}
			out = append(out, view)
		}
	}

	sortResources(out)
	c.JSON(http.StatusOK, gin.H{
		"services":       out,
		"namespace":      scope.Namespace,
		"all_namespaces": scope.All,
	})
}

func (s *server) listIngresses(c *gin.Context) {
	user, cluster, grant, ok := s.resourceCluster(c)
	if !ok {
		return
	}
	scope, ok := s.resourceScope(c, grant)
	if !ok {
		return
	}

	out := []ingressView{}
	for _, path := range scope.paths(resourceListPath{"/apis/networking.k8s.io/v1", "ingresses"}) {
		var list struct {
			Items []struct {
				Metadata objectMeta `json:"metadata"`
				Spec     struct {
					IngressClassName string `json:"ingressClassName"`
					Rules            []struct {
						Host string `json:"host"`
					} `json:"rules"`
				} `json:"spec"`
				Status struct {
					LoadBalancer struct {
						Ingress []struct {
							IP       string `json:"ip"`
							Hostname string `json:"hostname"`
						} `json:"ingress"`
					} `json:"loadBalancer"`
				} `json:"status"`
			} `json:"items"`
		}
		if !s.fetch(c, user, cluster, grant, path, &list) {
			return
		}

		for _, item := range list.Items {
			view := ingressView{
				listMeta: item.Metadata.meta(),
				Class:    item.Spec.IngressClassName,
				Rules:    len(item.Spec.Rules),
			}
			for _, rule := range item.Spec.Rules {
				if rule.Host != "" && !slices.Contains(view.Hosts, rule.Host) {
					view.Hosts = append(view.Hosts, rule.Host)
				}
			}
			for _, address := range item.Status.LoadBalancer.Ingress {
				if address.IP != "" {
					view.Addresses = append(view.Addresses, address.IP)
				}
				if address.Hostname != "" {
					view.Addresses = append(view.Addresses, address.Hostname)
				}
			}
			out = append(out, view)
		}
	}

	sortResources(out)
	c.JSON(http.StatusOK, gin.H{
		"ingresses":      out,
		"namespace":      scope.Namespace,
		"all_namespaces": scope.All,
	})
}

// listHTTPRoutes reads Gateway API routes. The Gateway API is a CRD, so a
// cluster without it installed answers 404 — reported as an empty, unavailable
// list rather than as an error, because "Gateway API is not installed here" is
// something the UI should say plainly.
func (s *server) listHTTPRoutes(c *gin.Context) {
	user, cluster, grant, ok := s.resourceCluster(c)
	if !ok {
		return
	}
	scope, ok := s.resourceScope(c, grant)
	if !ok {
		return
	}

	groups := scope.candidates(
		resourceListPath{"/apis/gateway.networking.k8s.io/v1", "httproutes"},
		resourceListPath{"/apis/gateway.networking.k8s.io/v1beta1", "httproutes"},
	)

	out := []routeView{}
	for _, group := range groups {
		var list struct {
			Items []struct {
				Metadata objectMeta `json:"metadata"`
				Spec     struct {
					Hostnames  []string `json:"hostnames"`
					ParentRefs []struct {
						Name      string `json:"name"`
						Namespace string `json:"namespace"`
						Kind      string `json:"kind"`
					} `json:"parentRefs"`
					Rules []struct{} `json:"rules"`
				} `json:"spec"`
			} `json:"items"`
		}
		found, callOK := s.fetchOptional(c, user, cluster, grant, group, &list)
		if !callOK {
			return
		}
		// The CRD is either installed on the cluster or it is not; one namespace
		// answering 404 settles it for all of them.
		if !found {
			c.JSON(http.StatusOK, gin.H{
				"httproutes":     []routeView{},
				"namespace":      scope.Namespace,
				"all_namespaces": scope.All,
				"available":      false,
				"reason":         "the Gateway API is not installed on this cluster",
			})
			return
		}

		for _, item := range list.Items {
			view := routeView{
				listMeta:  item.Metadata.meta(),
				Hostnames: item.Spec.Hostnames,
				Rules:     len(item.Spec.Rules),
			}
			for _, parent := range item.Spec.ParentRefs {
				name := parent.Name
				if parent.Namespace != "" {
					name = parent.Namespace + "/" + parent.Name
				}
				view.Parents = append(view.Parents, name)
			}
			out = append(out, view)
		}
	}

	sortResources(out)
	c.JSON(http.StatusOK, gin.H{
		"httproutes":     out,
		"namespace":      scope.Namespace,
		"all_namespaces": scope.All,
		"available":      true,
	})
}

// listVirtualServices reads Istio virtual services, which are optional in the
// same way the Gateway API is.
func (s *server) listVirtualServices(c *gin.Context) {
	user, cluster, grant, ok := s.resourceCluster(c)
	if !ok {
		return
	}
	scope, ok := s.resourceScope(c, grant)
	if !ok {
		return
	}

	groups := scope.candidates(
		resourceListPath{"/apis/networking.istio.io/v1", "virtualservices"},
		resourceListPath{"/apis/networking.istio.io/v1beta1", "virtualservices"},
	)

	out := []routeView{}
	for _, group := range groups {
		var list struct {
			Items []struct {
				Metadata objectMeta `json:"metadata"`
				Spec     struct {
					Hosts    []string   `json:"hosts"`
					Gateways []string   `json:"gateways"`
					HTTP     []struct{} `json:"http"`
					TCP      []struct{} `json:"tcp"`
					TLS      []struct{} `json:"tls"`
				} `json:"spec"`
			} `json:"items"`
		}
		found, callOK := s.fetchOptional(c, user, cluster, grant, group, &list)
		if !callOK {
			return
		}
		if !found {
			c.JSON(http.StatusOK, gin.H{
				"virtualservices": []routeView{},
				"namespace":       scope.Namespace,
				"all_namespaces":  scope.All,
				"available":       false,
				"reason":          "Istio is not installed on this cluster",
			})
			return
		}

		for _, item := range list.Items {
			out = append(out, routeView{
				listMeta:  item.Metadata.meta(),
				Hostnames: item.Spec.Hosts,
				Parents:   item.Spec.Gateways,
				Rules:     len(item.Spec.HTTP) + len(item.Spec.TCP) + len(item.Spec.TLS),
			})
		}
	}

	sortResources(out)
	c.JSON(http.StatusOK, gin.H{
		"virtualservices": out,
		"namespace":       scope.Namespace,
		"all_namespaces":  scope.All,
		"available":       true,
	})
}

/* ------------------------------------------------------ storage & config --- */

type persistentVolumeView struct {
	listMeta
	Capacity      string   `json:"capacity"`
	AccessModes   []string `json:"access_modes"`
	ReclaimPolicy string   `json:"reclaim_policy"`
	Status        string   `json:"status"`
	Claim         string   `json:"claim,omitempty"`
	StorageClass  string   `json:"storage_class,omitempty"`
}

type persistentVolumeClaimView struct {
	listMeta
	Status       string   `json:"status"`
	Capacity     string   `json:"capacity"`
	AccessModes  []string `json:"access_modes"`
	StorageClass string   `json:"storage_class,omitempty"`
	Volume       string   `json:"volume,omitempty"`
}

type storageClassView struct {
	listMeta
	Provisioner   string `json:"provisioner"`
	ReclaimPolicy string `json:"reclaim_policy"`
	BindingMode   string `json:"binding_mode"`
	Default       bool   `json:"default"`
}

// configEntryView is a ConfigMap or a Secret reduced to what it holds rather
// than what is in it. Values never reach the browser: a config map can be
// megabytes, and a secret is a secret — the keys are enough to see the shape of
// a namespace, and reading a value is what kubectl through the audited proxy is
// for.
type configEntryView struct {
	listMeta
	Type string   `json:"type,omitempty"`
	Keys []string `json:"keys"`
	// Immutable marks a config map or secret that cannot be edited in place.
	Immutable bool `json:"immutable,omitempty"`
}

func (s *server) listPersistentVolumes(c *gin.Context) {
	user, cluster, grant, ok := s.resourceCluster(c)
	if !ok {
		return
	}
	if !s.requireClusterScope(c, grant, "persistent volumes") {
		return
	}

	var list struct {
		Items []struct {
			Metadata objectMeta `json:"metadata"`
			Spec     struct {
				Capacity                      map[string]string `json:"capacity"`
				AccessModes                   []string          `json:"accessModes"`
				PersistentVolumeReclaimPolicy string            `json:"persistentVolumeReclaimPolicy"`
				StorageClassName              string            `json:"storageClassName"`
				ClaimRef                      *struct {
					Namespace string `json:"namespace"`
					Name      string `json:"name"`
				} `json:"claimRef"`
			} `json:"spec"`
			Status struct {
				Phase string `json:"phase"`
			} `json:"status"`
		} `json:"items"`
	}

	if !s.fetch(c, user, cluster, grant, resourceListPath{"/api/v1", "persistentvolumes"}.clusterWide(), &list) {
		return
	}

	out := make([]persistentVolumeView, 0, len(list.Items))
	for _, item := range list.Items {
		view := persistentVolumeView{
			listMeta:      item.Metadata.meta(),
			Capacity:      item.Spec.Capacity["storage"],
			AccessModes:   item.Spec.AccessModes,
			ReclaimPolicy: item.Spec.PersistentVolumeReclaimPolicy,
			Status:        item.Status.Phase,
			StorageClass:  item.Spec.StorageClassName,
		}
		if item.Spec.ClaimRef != nil {
			view.Claim = item.Spec.ClaimRef.Namespace + "/" + item.Spec.ClaimRef.Name
		}
		out = append(out, view)
	}

	sortResources(out)
	c.JSON(http.StatusOK, gin.H{"persistentvolumes": out})
}

func (s *server) listPersistentVolumeClaims(c *gin.Context) {
	user, cluster, grant, ok := s.resourceCluster(c)
	if !ok {
		return
	}
	scope, ok := s.resourceScope(c, grant)
	if !ok {
		return
	}

	out := []persistentVolumeClaimView{}
	for _, path := range scope.paths(resourceListPath{"/api/v1", "persistentvolumeclaims"}) {
		var list struct {
			Items []struct {
				Metadata objectMeta `json:"metadata"`
				Spec     struct {
					AccessModes      []string `json:"accessModes"`
					StorageClassName *string  `json:"storageClassName"`
					VolumeName       string   `json:"volumeName"`
					Resources        struct {
						Requests map[string]string `json:"requests"`
					} `json:"resources"`
				} `json:"spec"`
				Status struct {
					Phase    string            `json:"phase"`
					Capacity map[string]string `json:"capacity"`
				} `json:"status"`
			} `json:"items"`
		}
		if !s.fetch(c, user, cluster, grant, path, &list) {
			return
		}

		for _, item := range list.Items {
			view := persistentVolumeClaimView{
				listMeta:    item.Metadata.meta(),
				Status:      item.Status.Phase,
				AccessModes: item.Spec.AccessModes,
				Volume:      item.Spec.VolumeName,
			}
			// A bound claim reports what it got; a pending one only what it
			// asked for, and that is the more useful thing while it waits.
			view.Capacity = item.Status.Capacity["storage"]
			if view.Capacity == "" {
				view.Capacity = item.Spec.Resources.Requests["storage"]
			}
			if item.Spec.StorageClassName != nil {
				view.StorageClass = *item.Spec.StorageClassName
			}
			out = append(out, view)
		}
	}

	sortResources(out)
	c.JSON(http.StatusOK, gin.H{
		"persistentvolumeclaims": out,
		"namespace":              scope.Namespace,
		"all_namespaces":         scope.All,
	})
}

func (s *server) listStorageClasses(c *gin.Context) {
	user, cluster, grant, ok := s.resourceCluster(c)
	if !ok {
		return
	}
	if !s.requireClusterScope(c, grant, "storage classes") {
		return
	}

	var list struct {
		Items []struct {
			Metadata          objectMeta `json:"metadata"`
			Provisioner       string     `json:"provisioner"`
			ReclaimPolicy     string     `json:"reclaimPolicy"`
			VolumeBindingMode string     `json:"volumeBindingMode"`
		} `json:"items"`
	}

	path := resourceListPath{"/apis/storage.k8s.io/v1", "storageclasses"}.clusterWide()
	if !s.fetch(c, user, cluster, grant, path, &list) {
		return
	}

	out := make([]storageClassView, 0, len(list.Items))
	for _, item := range list.Items {
		out = append(out, storageClassView{
			listMeta:      item.Metadata.meta(),
			Provisioner:   item.Provisioner,
			ReclaimPolicy: item.ReclaimPolicy,
			BindingMode:   item.VolumeBindingMode,
			// The default class is marked by an annotation, and which class is
			// default decides what an unqualified claim gets.
			Default: item.Metadata.Annotations["storageclass.kubernetes.io/is-default-class"] == "true",
		})
	}

	sortResources(out)
	c.JSON(http.StatusOK, gin.H{"storageclasses": out})
}

func (s *server) listConfigMaps(c *gin.Context) {
	user, cluster, grant, ok := s.resourceCluster(c)
	if !ok {
		return
	}
	scope, ok := s.resourceScope(c, grant)
	if !ok {
		return
	}

	out := []configEntryView{}
	for _, path := range scope.paths(resourceListPath{"/api/v1", "configmaps"}) {
		var list struct {
			Items []struct {
				Metadata   objectMeta        `json:"metadata"`
				Immutable  *bool             `json:"immutable"`
				Data       map[string]string `json:"data"`
				BinaryData map[string]string `json:"binaryData"`
			} `json:"items"`
		}
		if !s.fetch(c, user, cluster, grant, path, &list) {
			return
		}

		for _, item := range list.Items {
			view := configEntryView{listMeta: item.Metadata.meta(), Keys: []string{}}
			for key := range item.Data {
				view.Keys = append(view.Keys, key)
			}
			for key := range item.BinaryData {
				view.Keys = append(view.Keys, key)
			}
			slices.Sort(view.Keys)
			if item.Immutable != nil {
				view.Immutable = *item.Immutable
			}
			out = append(out, view)
		}
	}

	sortResources(out)
	c.JSON(http.StatusOK, gin.H{
		"configmaps":     out,
		"namespace":      scope.Namespace,
		"all_namespaces": scope.All,
	})
}

// listSecrets returns secret metadata only. The values are decoded off the wire
// here and dropped: they never enter a response, so no secret ends up in a
// browser cache or a screenshot because someone opened a list.
func (s *server) listSecrets(c *gin.Context) {
	user, cluster, grant, ok := s.resourceCluster(c)
	if !ok {
		return
	}
	scope, ok := s.resourceScope(c, grant)
	if !ok {
		return
	}

	out := []configEntryView{}
	for _, path := range scope.paths(resourceListPath{"/api/v1", "secrets"}) {
		var list struct {
			Items []struct {
				Metadata  objectMeta        `json:"metadata"`
				Type      string            `json:"type"`
				Immutable *bool             `json:"immutable"`
				Data      map[string]string `json:"data"`
			} `json:"items"`
		}
		if !s.fetch(c, user, cluster, grant, path, &list) {
			return
		}

		for _, item := range list.Items {
			view := configEntryView{
				listMeta: item.Metadata.meta(),
				Type:     item.Type,
				Keys:     []string{},
			}
			for key := range item.Data {
				view.Keys = append(view.Keys, key)
			}
			slices.Sort(view.Keys)
			if item.Immutable != nil {
				view.Immutable = *item.Immutable
			}
			out = append(out, view)
		}
	}

	sortResources(out)
	c.JSON(http.StatusOK, gin.H{
		"secrets":        out,
		"namespace":      scope.Namespace,
		"all_namespaces": scope.All,
	})
}

/* -------------------------------------------------------------- cluster --- */

type crdView struct {
	listMeta
	Group    string   `json:"group"`
	Kind     string   `json:"kind"`
	Plural   string   `json:"plural"`
	Scope    string   `json:"scope"`
	Versions []string `json:"versions"`
}

type nodeView struct {
	listMeta
	Ready         bool     `json:"ready"`
	Status        string   `json:"status"`
	Roles         []string `json:"roles"`
	Version       string   `json:"version"`
	InternalIP    string   `json:"internal_ip,omitempty"`
	OSImage       string   `json:"os_image,omitempty"`
	CPU           string   `json:"cpu,omitempty"`
	Memory        string   `json:"memory,omitempty"`
	Unschedulable bool     `json:"unschedulable,omitempty"`
}

func (s *server) listCRDs(c *gin.Context) {
	user, cluster, grant, ok := s.resourceCluster(c)
	if !ok {
		return
	}
	if !s.requireClusterScope(c, grant, "custom resource definitions") {
		return
	}

	var list struct {
		Items []struct {
			Metadata objectMeta `json:"metadata"`
			Spec     struct {
				Group string `json:"group"`
				Scope string `json:"scope"`
				Names struct {
					Kind   string `json:"kind"`
					Plural string `json:"plural"`
				} `json:"names"`
				Versions []struct {
					Name   string `json:"name"`
					Served bool   `json:"served"`
				} `json:"versions"`
			} `json:"spec"`
		} `json:"items"`
	}

	path := resourceListPath{"/apis/apiextensions.k8s.io/v1", "customresourcedefinitions"}.clusterWide()
	if !s.fetch(c, user, cluster, grant, path, &list) {
		return
	}

	out := make([]crdView, 0, len(list.Items))
	for _, item := range list.Items {
		view := crdView{
			listMeta: item.Metadata.meta(),
			Group:    item.Spec.Group,
			Kind:     item.Spec.Names.Kind,
			Plural:   item.Spec.Names.Plural,
			Scope:    item.Spec.Scope,
		}
		// Only served versions are worth listing: an unserved one cannot be
		// used, and every CRD keeps its history.
		for _, version := range item.Spec.Versions {
			if version.Served {
				view.Versions = append(view.Versions, version.Name)
			}
		}
		out = append(out, view)
	}

	sortResources(out)
	c.JSON(http.StatusOK, gin.H{"crds": out})
}

func (s *server) listNodes(c *gin.Context) {
	user, cluster, grant, ok := s.resourceCluster(c)
	if !ok {
		return
	}
	if !s.requireClusterScope(c, grant, "nodes") {
		return
	}

	var list struct {
		Items []struct {
			Metadata objectMeta `json:"metadata"`
			Spec     struct {
				Unschedulable bool `json:"unschedulable"`
			} `json:"spec"`
			Status struct {
				Capacity  map[string]string `json:"capacity"`
				Addresses []struct {
					Type    string `json:"type"`
					Address string `json:"address"`
				} `json:"addresses"`
				Conditions []struct {
					Type   string `json:"type"`
					Status string `json:"status"`
				} `json:"conditions"`
				NodeInfo struct {
					KubeletVersion string `json:"kubeletVersion"`
					OSImage        string `json:"osImage"`
				} `json:"nodeInfo"`
			} `json:"status"`
		} `json:"items"`
	}

	if !s.fetch(c, user, cluster, grant, resourceListPath{"/api/v1", "nodes"}.clusterWide(), &list) {
		return
	}

	out := make([]nodeView, 0, len(list.Items))
	for _, item := range list.Items {
		view := nodeView{
			listMeta:      item.Metadata.meta(),
			Version:       item.Status.NodeInfo.KubeletVersion,
			OSImage:       item.Status.NodeInfo.OSImage,
			CPU:           item.Status.Capacity["cpu"],
			Memory:        item.Status.Capacity["memory"],
			Unschedulable: item.Spec.Unschedulable,
		}
		for _, condition := range item.Status.Conditions {
			if condition.Type == "Ready" {
				view.Ready = condition.Status == "True"
			}
		}
		view.Status = "NotReady"
		if view.Ready {
			view.Status = "Ready"
		}
		if view.Unschedulable {
			view.Status += ",SchedulingDisabled"
		}
		for _, address := range item.Status.Addresses {
			if address.Type == "InternalIP" {
				view.InternalIP = address.Address
				break
			}
		}
		view.Roles = nodeRoles(item.Metadata.Labels)
		out = append(out, view)
	}

	sortResources(out)
	c.JSON(http.StatusOK, gin.H{"nodes": out})
}

// nodeRoles reads a node's roles the way kubectl does: from the
// node-role.kubernetes.io/<role> labels, falling back to "worker" when a node
// carries none.
func nodeRoles(labels map[string]string) []string {
	const prefix = "node-role.kubernetes.io/"

	roles := []string{}
	for label := range labels {
		if role, found := strings.CutPrefix(label, prefix); found && role != "" {
			roles = append(roles, role)
		}
	}
	slices.Sort(roles)
	if len(roles) == 0 {
		return []string{"worker"}
	}
	return roles
}
