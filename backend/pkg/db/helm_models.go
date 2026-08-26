package db

import (
	"encoding/json"
	"strings"
	"time"

	"github.com/kubemg/kubemg/backend/pkg/helm"
)

/*
 * Where charts may be installed from.
 *
 * A repository is an **administrator's declaration**, and it is **server-wide
 * rather than per-cluster**. That second half is the decision worth recording,
 * because per-cluster is the shape everything else in this table follows —
 * observability sources, consoles and CRD visibility are all keyed by cluster.
 * A chart repository is not: what this installation may reach out to and pull
 * executable templates from is a fact about *this installation*, and duplicating
 * it per cluster would mean an operator adding their internal mirror once per
 * cluster and a fleet where half the clusters can install cert-manager.
 *
 * Adding one is an **outbound-egress decision**, like an alarm channel: the
 * bastion will make a network call to a host an operator named, on a schedule,
 * and then execute what it downloads as a Go template. So writing is admin-only.
 * **Reading the catalogue is open to anyone the console is open to**, which is
 * the recording-policy precedent — a form offering a chart must not discover the
 * list of repositories by being refused, and the catalogue is a list of public
 * chart names rather than anything about the fleet.
 *
 * The credential follows the `observability_sources` rule exactly: stored,
 * `json:"-"` so it cannot be serialized by any handler that forgets, and an edit
 * that omits it keeps the stored one rather than clearing it.
 */

// HelmRepository is one place charts are read from.
type HelmRepository struct {
	ID uint `gorm:"primaryKey" json:"id"`
	// Name is the identity, and it is unique across the installation because it
	// is what a release records as its source and what an install names.
	Name string `gorm:"size:63;uniqueIndex;not null" json:"name"`
	URL  string `gorm:"type:text;not null" json:"url"`

	Username string `gorm:"size:255" json:"username,omitempty"`
	// Credential never leaves the process. See the package comment on
	// observability sources: the DTO reports whether one is set, never what.
	Credential string `gorm:"type:text" json:"-"`

	Description string `gorm:"type:text" json:"description,omitempty"`

	// Seeded marks a row this build put there on first boot rather than one an
	// operator added. It changes nothing about how the row behaves — a seeded
	// repository is editable and deletable like any other — and exists so the
	// console can say where a row came from.
	Seeded bool `gorm:"not null;default:false" json:"seeded"`

	// Status is the last sync's verdict, and StatusMessage its reason. They are
	// stored rather than derived because a repository that cannot be reached
	// keeps serving what it last held, and the operator has to be able to see
	// *why* what they are looking at is three days old.
	Status        string     `gorm:"size:20;not null;default:'pending'" json:"status"`
	StatusMessage string     `gorm:"type:text" json:"status_message,omitempty"`
	ChartCount    int        `gorm:"not null;default:0" json:"chart_count"`
	SyncedAt      *time.Time `json:"synced_at,omitempty"`

	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

func (HelmRepository) TableName() string { return "helm_repositories" }

// HasCredential is what a response says instead of the credential.
func (r HelmRepository) HasCredential() bool { return strings.TrimSpace(r.Credential) != "" }

// Repository sheds the storage fields, leaving what the fetcher needs.
func (r HelmRepository) Repository() helm.Repository {
	return helm.Repository{
		Name:       r.Name,
		URL:        r.URL,
		Username:   r.Username,
		Credential: r.Credential,
	}
}

// Repository sync statuses.
const (
	// HelmRepoPending is a repository that has been declared and not yet read.
	HelmRepoPending = "pending"
	HelmRepoOK      = "ok"
	HelmRepoError   = "error"
)

// HelmChart is one chart of one repository, as the last successful sync of that
// repository left it.
//
// The versions are a **JSON column rather than a third table**, and that is a
// deliberate narrowing rather than a shortcut. A version is never queried
// independently of its chart — every read here is "the versions of this chart",
// for a dropdown — and the set is already bounded to `MaxVersionsPerChart`
// before it is written, so the row cannot grow. A versions table would buy
// exactly one thing, the ability to ask a question nothing asks, and cost a join
// on every catalogue read plus a second delete on every sync.
type HelmChart struct {
	ID           uint   `gorm:"primaryKey" json:"id"`
	RepositoryID uint   `gorm:"uniqueIndex:idx_helm_chart_repo_name;not null" json:"repository_id"`
	Name         string `gorm:"size:255;uniqueIndex:idx_helm_chart_repo_name;not null" json:"name"`

	Description string `gorm:"type:text" json:"description,omitempty"`
	Icon        string `gorm:"type:text" json:"icon,omitempty"`
	Home        string `gorm:"type:text" json:"home,omitempty"`
	Deprecated  bool   `gorm:"not null;default:false" json:"deprecated"`

	// Versions is a JSON array of helm.ChartVersion, newest first.
	Versions string `gorm:"type:text" json:"-"`

	UpdatedAt time.Time `json:"updated_at"`
}

func (HelmChart) TableName() string { return "helm_charts" }

// ChartVersions decodes the stored version list. A row whose JSON does not
// decode reads as a chart with no versions rather than as an error: it can only
// have been written by a different build, and the next sync replaces it.
func (c HelmChart) ChartVersions() []helm.ChartVersion {
	if strings.TrimSpace(c.Versions) == "" {
		return nil
	}
	var versions []helm.ChartVersion
	if err := json.Unmarshal([]byte(c.Versions), &versions); err != nil {
		return nil
	}
	return versions
}

// Chart puts a stored row back into the shape the fetcher and the renderer use,
// so nothing downstream has to know charts are stored at all.
func (c HelmChart) Chart() helm.Chart {
	return helm.Chart{
		Name:        c.Name,
		Description: c.Description,
		Icon:        c.Icon,
		Home:        c.Home,
		Deprecated:  c.Deprecated,
		Versions:    c.ChartVersions(),
	}
}

// HelmChartRowsOf turns a fetched catalogue into rows for one repository.
func HelmChartRowsOf(repositoryID uint, charts []helm.Chart) []HelmChart {
	rows := make([]HelmChart, 0, len(charts))
	for _, chart := range charts {
		encoded, err := json.Marshal(chart.Versions)
		if err != nil {
			continue
		}
		rows = append(rows, HelmChart{
			RepositoryID: repositoryID,
			Name:         chart.Name,
			Description:  chart.Description,
			Icon:         chart.Icon,
			Home:         chart.Home,
			Deprecated:   chart.Deprecated,
			Versions:     string(encoded),
		})
	}
	return rows
}

// SeededHelmRepositories is the catalogue a fresh installation starts with.
//
// It is **seeded into the table rather than hard-coded into a form**, and the
// difference is the whole point: an air-gapped site has to be able to point this
// feature at its own mirror, and a list nobody can edit is a list that is wrong
// by the second week. Every row here is deletable and editable like one an
// operator typed; the only thing `Seeded` buys is being able to say where it
// came from.
//
// The set is the half-dozen a new install would add first, and it is
// deliberately short. This is not a directory of the ecosystem — it is the
// smallest set that makes the feature usable before an operator has configured
// anything, and each of these is a repository whose charts an operator installs
// on day one of a cluster.
var SeededHelmRepositories = []HelmRepository{
	{
		Name:        "ingress-nginx",
		URL:         "https://kubernetes.github.io/ingress-nginx",
		Description: "The Kubernetes project's own NGINX ingress controller.",
	},
	{
		Name:        "jetstack",
		URL:         "https://charts.jetstack.io",
		Description: "cert-manager, from the project that maintains it.",
	},
	{
		Name:        "prometheus-community",
		URL:         "https://prometheus-community.github.io/helm-charts",
		Description: "Prometheus, Alertmanager, the node exporter and kube-state-metrics.",
	},
	{
		Name:        "grafana",
		URL:         "https://grafana.github.io/helm-charts",
		Description: "Grafana, Loki and the rest of the Grafana stack.",
	},
	{
		Name:        "bitnami",
		URL:         "https://charts.bitnami.com/bitnami",
		Description: "Packaged databases, brokers and runtimes.",
	},
	{
		Name:        "argo",
		URL:         "https://argoproj.github.io/argo-helm",
		Description: "Argo CD, Workflows, Rollouts and Events.",
	},
}
