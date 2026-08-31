package api

import (
	"net"
	"net/http"
	"strconv"
	"testing"

	"github.com/kubemg/kubemg/backend/pkg/db"
)

func TestAuditForwardersAreAdminOnly(t *testing.T) {
	env := newTestEnv(t)
	user := env.store.addUser("devops", "pw", db.RoleUser)

	// Reading the list is administrative too, not only writing it: the set read
	// at once says where this platform's whole trail is being sent.
	rec := env.do(t, http.MethodGet, "/api/v1/audit/forwarders", env.tokenFor(t, user), nil)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("expected %d for a non-admin, got %d", http.StatusForbidden, rec.Code)
	}

	rec = env.do(t, http.MethodPost, "/api/v1/audit/forwarders", env.tokenFor(t, user), map[string]any{
		"name": "logsign", "host": "logsign.example.com",
	})
	if rec.Code != http.StatusForbidden {
		t.Fatalf("expected %d for a non-admin create, got %d", http.StatusForbidden, rec.Code)
	}
}

func TestCreateAuditForwarderFillsTheSyslogDefaults(t *testing.T) {
	env := newTestEnv(t)
	admin := env.store.addUser("admin", "pw", db.RoleAdmin)

	rec := env.do(t, http.MethodPost, "/api/v1/audit/forwarders", env.tokenFor(t, admin), map[string]any{
		"name": "logsign", "host": "logsign.example.com",
	})
	if rec.Code != http.StatusCreated {
		t.Fatalf("expected %d, got %d (%s)", http.StatusCreated, rec.Code, rec.Body.String())
	}

	body := decode[db.AuditForwarder](t, rec)
	if body.Kind != db.ForwarderSyslog {
		t.Errorf("kind = %q, want the only transport there is", body.Kind)
	}
	if body.Protocol != db.ForwarderProtoTCP {
		t.Errorf("protocol = %q, want tcp — udp loses records silently", body.Protocol)
	}
	// The port default follows the protocol on purpose: Logsign listens for
	// syslog on UDP 514 and TCP 515, and one default for both would produce a
	// destination that connects to nothing.
	if body.Port != db.DefaultForwarderTCPPort {
		t.Errorf("port = %d, want %d", body.Port, db.DefaultForwarderTCPPort)
	}
	if body.Facility != db.DefaultForwarderFacility {
		t.Errorf("facility = %d, want local0", body.Facility)
	}
	if body.AppName != db.DefaultForwarderAppName {
		t.Errorf("app_name = %q", body.AppName)
	}
	if !body.Enabled {
		t.Error("a forwarder an operator just configured starts enabled")
	}
}

func TestCreateAuditForwarderDefaultsUDPToItsOwnPort(t *testing.T) {
	env := newTestEnv(t)
	admin := env.store.addUser("admin", "pw", db.RoleAdmin)

	rec := env.do(t, http.MethodPost, "/api/v1/audit/forwarders", env.tokenFor(t, admin), map[string]any{
		"name": "logsign-udp", "host": "logsign.example.com", "protocol": db.ForwarderProtoUDP,
	})
	if rec.Code != http.StatusCreated {
		t.Fatalf("expected %d, got %d (%s)", http.StatusCreated, rec.Code, rec.Body.String())
	}
	if got := decode[db.AuditForwarder](t, rec).Port; got != db.DefaultForwarderUDPPort {
		t.Fatalf("port = %d, want %d", got, db.DefaultForwarderUDPPort)
	}
}

func TestCreateAuditForwarderRefusals(t *testing.T) {
	env := newTestEnv(t)
	admin := env.store.addUser("admin", "pw", db.RoleAdmin)
	token := env.tokenFor(t, admin)

	cases := map[string]map[string]any{
		// The mistake an operator makes coming from the alarm channels, where
		// the field is a URL. Refused by name rather than dialled.
		"a host with a scheme":    {"name": "a", "host": "syslog://logsign.example.com"},
		"a host with a port":      {"name": "b", "host": "logsign.example.com:515"},
		"no host at all":          {"name": "c", "host": "  "},
		"no name":                 {"name": "", "host": "logsign.example.com"},
		"an unknown protocol":     {"name": "d", "host": "h", "protocol": "quic"},
		"an unknown kind":         {"name": "e", "host": "h", "kind": "http"},
		"a port out of range":     {"name": "f", "host": "h", "port": 70000},
		"a facility out of range": {"name": "g", "host": "h", "facility": 99},
		// A space in APP-NAME shifts every RFC 5424 field after it, so the
		// receiver reads the message as the structured data.
		"an app name with a space": {"name": "h", "host": "h", "app_name": "kube mg"},
		// A CA bundle on a plaintext transport is a configuration that cannot do
		// what its author thinks it does.
		"a ca bundle without tls": {"name": "i", "host": "h", "tls_ca_bundle": "-----BEGIN CERTIFICATE-----"},
	}

	for name, payload := range cases {
		t.Run(name, func(t *testing.T) {
			rec := env.do(t, http.MethodPost, "/api/v1/audit/forwarders", token, payload)
			if rec.Code != http.StatusBadRequest {
				t.Fatalf("expected %d, got %d (%s)", http.StatusBadRequest, rec.Code, rec.Body.String())
			}
		})
	}
}

// A duplicate name is a conflict rather than a second row: the name is what an
// operator recognises a destination by in a list of delivery health.
func TestCreateAuditForwarderRefusesADuplicateName(t *testing.T) {
	env := newTestEnv(t)
	admin := env.store.addUser("admin", "pw", db.RoleAdmin)
	env.store.addAuditForwarder(db.AuditForwarder{Name: "logsign", Host: "h", Port: 515})

	rec := env.do(t, http.MethodPost, "/api/v1/audit/forwarders", env.tokenFor(t, admin), map[string]any{
		"name": "logsign", "host": "other.example.com",
	})
	if rec.Code != http.StatusConflict {
		t.Fatalf("expected %d, got %d (%s)", http.StatusConflict, rec.Code, rec.Body.String())
	}
}

// TLS verification can only be waived on a TLS destination — carrying the flag
// on a plaintext one would leave a row claiming a guarantee it never had.
func TestInsecureSkipVerifyOnlySticksOnTLS(t *testing.T) {
	env := newTestEnv(t)
	admin := env.store.addUser("admin", "pw", db.RoleAdmin)

	rec := env.do(t, http.MethodPost, "/api/v1/audit/forwarders", env.tokenFor(t, admin), map[string]any{
		"name": "logsign", "host": "h", "protocol": db.ForwarderProtoTCP,
		"tls_insecure_skip_verify": true,
	})
	if rec.Code != http.StatusCreated {
		t.Fatalf("expected %d, got %d (%s)", http.StatusCreated, rec.Code, rec.Body.String())
	}
	if decode[db.AuditForwarder](t, rec).TLSInsecureSkipVerify {
		t.Fatal("a plaintext destination must not record a waived TLS check")
	}
}

// An edit changes configuration; it does not erase the answer to "is this
// destination working".
func TestUpdateAuditForwarderKeepsDeliveryHealth(t *testing.T) {
	env := newTestEnv(t)
	admin := env.store.addUser("admin", "pw", db.RoleAdmin)
	forwarder := env.store.addAuditForwarder(db.AuditForwarder{
		Name: "logsign", Kind: db.ForwarderSyslog, Host: "h", Port: 515,
		Protocol: db.ForwarderProtoTCP, Facility: 16, AppName: "kubemg", Enabled: true,
		LastStatus: db.ForwarderStatusError, LastMessage: "connection refused",
	})

	path := "/api/v1/audit/forwarders/" + strconv.FormatUint(uint64(forwarder.ID), 10)
	rec := env.do(t, http.MethodPut, path, env.tokenFor(t, admin), map[string]any{
		"name": "logsign", "host": "logsign.example.com", "port": 515,
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("expected %d, got %d (%s)", http.StatusOK, rec.Code, rec.Body.String())
	}
	body := decode[db.AuditForwarder](t, rec)
	if body.Host != "logsign.example.com" {
		t.Errorf("host = %q", body.Host)
	}
	if body.LastStatus != db.ForwarderStatusError {
		t.Errorf("delivery health should survive an edit, got %q", body.LastStatus)
	}
}

func TestAuditForwarderNotFound(t *testing.T) {
	env := newTestEnv(t)
	token := env.tokenFor(t, env.store.addUser("admin", "pw", db.RoleAdmin))

	for _, call := range []struct {
		method string
		path   string
	}{
		{http.MethodPut, "/api/v1/audit/forwarders/404"},
		{http.MethodDelete, "/api/v1/audit/forwarders/404"},
		{http.MethodPost, "/api/v1/audit/forwarders/404/test"},
	} {
		rec := env.do(t, call.method, call.path, token, map[string]any{"name": "x", "host": "h"})
		if rec.Code != http.StatusNotFound {
			t.Fatalf("%s %s: expected %d, got %d", call.method, call.path, http.StatusNotFound, rec.Code)
		}
	}
}

// The test button delivers a real record, and its outcome updates delivery
// health like any other delivery — a successful test beside a stale "error"
// would be a console contradicting itself.
func TestTestAuditForwarderDelivers(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer listener.Close()
	accepted := make(chan struct{}, 1)
	go func() {
		conn, err := listener.Accept()
		if err != nil {
			return
		}
		buf := make([]byte, 4096)
		_, _ = conn.Read(buf)
		accepted <- struct{}{}
	}()

	host, port, _ := net.SplitHostPort(listener.Addr().String())
	number, _ := strconv.Atoi(port)

	env := newTestEnv(t)
	admin := env.store.addUser("admin", "pw", db.RoleAdmin)
	forwarder := env.store.addAuditForwarder(db.AuditForwarder{
		Name: "logsign", Kind: db.ForwarderSyslog, Host: host, Port: number,
		Protocol: db.ForwarderProtoTCP, Facility: 16, AppName: "kubemg",
		Enabled: true, LastStatus: db.ForwarderStatusError,
	})

	path := "/api/v1/audit/forwarders/" + strconv.FormatUint(uint64(forwarder.ID), 10) + "/test"
	rec := env.do(t, http.MethodPost, path, env.tokenFor(t, admin), nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected %d, got %d (%s)", http.StatusOK, rec.Code, rec.Body.String())
	}
	<-accepted

	stored, _ := env.store.AuditForwarderByID(t.Context(), forwarder.ID)
	if stored.LastStatus != db.ForwarderStatusOK {
		t.Fatalf("a successful test must clear the stale failure, got %q", stored.LastStatus)
	}
}

// A UDP test cannot prove delivery, and the response says so rather than
// letting a green tick be read as proof.
func TestUDPTestSaysWhatItCannotProve(t *testing.T) {
	env := newTestEnv(t)
	admin := env.store.addUser("admin", "pw", db.RoleAdmin)
	forwarder := env.store.addAuditForwarder(db.AuditForwarder{
		Name: "logsign", Kind: db.ForwarderSyslog, Host: "127.0.0.1", Port: 1,
		Protocol: db.ForwarderProtoUDP, Facility: 16, AppName: "kubemg", Enabled: true,
	})

	path := "/api/v1/audit/forwarders/" + strconv.FormatUint(uint64(forwarder.ID), 10) + "/test"
	rec := env.do(t, http.MethodPost, path, env.tokenFor(t, admin), nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected %d, got %d (%s)", http.StatusOK, rec.Code, rec.Body.String())
	}
	if !contains(rec.Body.String(), "fire-and-forget") {
		t.Fatalf("the response must say what udp cannot prove: %s", rec.Body.String())
	}
}

func TestDeleteAuditForwarder(t *testing.T) {
	env := newTestEnv(t)
	admin := env.store.addUser("admin", "pw", db.RoleAdmin)
	forwarder := env.store.addAuditForwarder(db.AuditForwarder{Name: "logsign", Host: "h", Port: 515})

	path := "/api/v1/audit/forwarders/" + strconv.FormatUint(uint64(forwarder.ID), 10)
	rec := env.do(t, http.MethodDelete, path, env.tokenFor(t, admin), nil)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("expected %d, got %d (%s)", http.StatusNoContent, rec.Code, rec.Body.String())
	}
	if _, err := env.store.AuditForwarderByID(t.Context(), forwarder.ID); err == nil {
		t.Fatal("the row should be gone")
	}
}
