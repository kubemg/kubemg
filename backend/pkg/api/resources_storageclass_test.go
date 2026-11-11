package api

import (
	"net/http"
	"testing"

	"github.com/kubemg/kubemg/backend/pkg/db"
)

/*
 * StorageClasses.
 *
 * Which class is default decides what an unnamed claim binds to, and the
 * insight above the list warns when there is none. The admission plugin that
 * fills the class in reads two annotations, the GA one and the beta one, so the
 * list has to read both or it warns about a default the cluster is using.
 */

func TestStorageClassDefaultAgreesWithTheAdmissionPlugin(t *testing.T) {
	env := newTestEnv(t)
	admin := env.store.addUser("admin", "secret123", db.RoleAdmin)
	cluster := env.store.addAgentCluster("edge", "dev", "kmg_agent-token")
	serveAgent(t, env, cluster, http.StatusOK, `{"kind":"StorageClassList","apiVersion":"storage.k8s.io/v1","items":[
		{"metadata":{"name":"ga","annotations":{"storageclass.kubernetes.io/is-default-class":"true"}},"provisioner":"p"},
		{"metadata":{"name":"beta","annotations":{"storageclass.beta.kubernetes.io/is-default-class":"true"}},"provisioner":"p"},
		{"metadata":{"name":"both","annotations":{"storageclass.kubernetes.io/is-default-class":"true","storageclass.beta.kubernetes.io/is-default-class":"true"}},"provisioner":"p"},
		{"metadata":{"name":"plain"},"provisioner":"p"},
		{"metadata":{"name":"ga-false","annotations":{"storageclass.kubernetes.io/is-default-class":"false"}},"provisioner":"p"},
		{"metadata":{"name":"beta-false","annotations":{"storageclass.beta.kubernetes.io/is-default-class":"false"}},"provisioner":"p"},
		{"metadata":{"name":"ga-capitalised","annotations":{"storageclass.kubernetes.io/is-default-class":"True"}},"provisioner":"p"},
		{"metadata":{"name":"beta-capitalised","annotations":{"storageclass.beta.kubernetes.io/is-default-class":"True"}},"provisioner":"p"}
	]}`)

	rec := env.do(t, http.MethodGet,
		"/api/v1/clusters/"+itoa(cluster.ID)+"/resources/storageclasses", env.tokenFor(t, admin), nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected %d, got %d (%s)", http.StatusOK, rec.Code, rec.Body.String())
	}
	body := decode[struct {
		StorageClasses []storageClassView `json:"storageclasses"`
	}](t, rec)

	// The plugin compares with "true" exactly, so a capitalised value is not a
	// default to it either.
	want := map[string]bool{
		"ga": true, "beta": true, "both": true,
		"plain": false, "ga-false": false, "beta-false": false,
		"ga-capitalised": false, "beta-capitalised": false,
	}
	if len(body.StorageClasses) != len(want) {
		t.Fatalf("expected %d classes, got %+v", len(want), body.StorageClasses)
	}
	for _, class := range body.StorageClasses {
		if class.Default != want[class.Name] {
			t.Errorf("%s: default = %v, want %v", class.Name, class.Default, want[class.Name])
		}
	}
}
