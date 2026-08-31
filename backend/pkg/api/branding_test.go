package api

import (
	"encoding/base64"
	"net/http"
	"strings"
	"testing"

	"github.com/kubemg/kubemg/backend/pkg/db"
)

// onePNG is a data: URI carrying a real, tiny PNG — enough bytes to be a file
// rather than a placeholder, which is what the size and base64 checks are
// measuring.
func onePNG() string {
	pixel := []byte{
		0x89, 'P', 'N', 'G', 0x0d, 0x0a, 0x1a, 0x0a,
		0x00, 0x00, 0x00, 0x0d, 'I', 'H', 'D', 'R',
		0x00, 0x00, 0x00, 0x01, 0x00, 0x00, 0x00, 0x01,
		0x08, 0x06, 0x00, 0x00, 0x00, 0x1f, 0x15, 0xc4,
		0x89,
	}
	return "data:image/png;base64," + base64.StdEncoding.EncodeToString(pixel)
}

func TestBrandingIsReadableWithoutASession(t *testing.T) {
	env := newTestEnv(t)

	// The banner's whole job is to be read before somebody types a password, so
	// a stranger on the sign-in page has to be able to read it.
	rec := env.do(t, http.MethodGet, "/api/v1/branding", "", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected status %d, got %d (%s)", http.StatusOK, rec.Code, rec.Body.String())
	}

	body := decode[brandingResponse](t, rec)
	if body != (brandingResponse{}) {
		t.Fatalf("an unconfigured console must carry no branding, got %+v", body)
	}
}

func TestBrandingWriteIsAdminOnly(t *testing.T) {
	env := newTestEnv(t)
	user := env.store.addUser("devops", "pw", db.RoleUser)

	rec := env.do(t, http.MethodPut, "/api/v1/branding", env.tokenFor(t, user), map[string]string{
		"organisation_name": "Acme",
	})
	if rec.Code != http.StatusForbidden {
		t.Fatalf("expected status %d, got %d", http.StatusForbidden, rec.Code)
	}
}

func TestBrandingRoundTrips(t *testing.T) {
	env := newTestEnv(t)
	admin := env.store.addUser("admin", "pw", db.RoleAdmin)

	rec := env.do(t, http.MethodPut, "/api/v1/branding", env.tokenFor(t, admin), map[string]string{
		"organisation_name": "  Acme Platform  ",
		"organisation_mark": onePNG(),
		"banner_text":       "PRODUCTION\n— handle with care",
		"banner_tone":       BannerToneCritical,
		"footer_notice":     "Internal — Restricted",
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("expected status %d, got %d (%s)", http.StatusOK, rec.Code, rec.Body.String())
	}

	body := decode[brandingResponse](t, rec)
	if body.OrganisationName != "Acme Platform" {
		t.Fatalf("expected a trimmed name, got %q", body.OrganisationName)
	}
	// A banner is one line by construction: a pasted newline would push every
	// page's content down by however many came with it.
	if body.BannerText != "PRODUCTION — handle with care" {
		t.Fatalf("expected the banner folded to one line, got %q", body.BannerText)
	}
	if body.BannerTone != BannerToneCritical {
		t.Fatalf("expected tone %q, got %q", BannerToneCritical, body.BannerTone)
	}
	if body.FooterNotice != "Internal — Restricted" {
		t.Fatalf("unexpected footer notice %q", body.FooterNotice)
	}

	// And it is there for the next stranger who loads the sign-in page.
	rec = env.do(t, http.MethodGet, "/api/v1/branding", "", nil)
	again := decode[brandingResponse](t, rec)
	if again.BannerText != body.BannerText || again.OrganisationMark != onePNG() {
		t.Fatalf("branding did not survive the round trip: %+v", again)
	}
}

func TestBrandingOmittedFieldsAreLeftAlone(t *testing.T) {
	env := newTestEnv(t)
	admin := env.store.addUser("admin", "pw", db.RoleAdmin)
	token := env.tokenFor(t, admin)

	env.do(t, http.MethodPut, "/api/v1/branding", token, map[string]string{
		"organisation_name": "Acme",
		"footer_notice":     "Internal",
	})

	rec := env.do(t, http.MethodPut, "/api/v1/branding", token, map[string]string{
		"footer_notice": "Internal — Restricted",
	})
	body := decode[brandingResponse](t, rec)
	if body.OrganisationName != "Acme" {
		t.Fatalf("an omitted field must be kept, got %q", body.OrganisationName)
	}
	if body.FooterNotice != "Internal — Restricted" {
		t.Fatalf("expected the notice to change, got %q", body.FooterNotice)
	}
}

func TestBrandingClearsWhenSentEmpty(t *testing.T) {
	env := newTestEnv(t)
	admin := env.store.addUser("admin", "pw", db.RoleAdmin)
	token := env.tokenFor(t, admin)

	env.do(t, http.MethodPut, "/api/v1/branding", token, map[string]string{
		"banner_text": "STAGING",
		"banner_tone": BannerToneCaution,
	})

	rec := env.do(t, http.MethodPut, "/api/v1/branding", token, map[string]string{
		"banner_text": "",
	})
	body := decode[brandingResponse](t, rec)
	if body.BannerText != "" {
		t.Fatalf("expected the banner cleared, got %q", body.BannerText)
	}
	// A tone with no text is not a banner, so the leftover tone must not be
	// reported as one.
	if body.BannerTone != "" {
		t.Fatalf("expected no tone alongside an empty banner, got %q", body.BannerTone)
	}
}

func TestBrandingRefusesUnknownTone(t *testing.T) {
	env := newTestEnv(t)
	admin := env.store.addUser("admin", "pw", db.RoleAdmin)

	rec := env.do(t, http.MethodPut, "/api/v1/branding", env.tokenFor(t, admin), map[string]string{
		"banner_text": "PRODUCTION",
		"banner_tone": "lime",
	})
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected status %d, got %d (%s)", http.StatusBadRequest, rec.Code, rec.Body.String())
	}
}

func TestBrandingRefusesAnSVGMark(t *testing.T) {
	env := newTestEnv(t)
	admin := env.store.addUser("admin", "pw", db.RoleAdmin)

	svg := "data:image/svg+xml;base64," + base64.StdEncoding.EncodeToString(
		[]byte(`<svg xmlns="http://www.w3.org/2000/svg"><script>alert(1)</script></svg>`))

	rec := env.do(t, http.MethodPut, "/api/v1/branding", env.tokenFor(t, admin), map[string]string{
		"organisation_mark": svg,
	})
	// An SVG is a document that can carry script, and this one renders in every
	// viewer's browser including on the unauthenticated sign-in page.
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected status %d, got %d (%s)", http.StatusBadRequest, rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "SVG") {
		t.Fatalf("the refusal should name what was refused: %s", rec.Body.String())
	}
}

func TestBrandingRefusesARemoteMark(t *testing.T) {
	env := newTestEnv(t)
	admin := env.store.addUser("admin", "pw", db.RoleAdmin)

	rec := env.do(t, http.MethodPut, "/api/v1/branding", env.tokenFor(t, admin), map[string]string{
		"organisation_mark": "https://cdn.example.com/logo.png",
	})
	// A linked mark is an image every viewer's browser fetches from somewhere
	// else — impossible air-gapped, and a beacon where it is possible.
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected status %d, got %d (%s)", http.StatusBadRequest, rec.Code, rec.Body.String())
	}
}

func TestBrandingRefusesAnOversizedMark(t *testing.T) {
	env := newTestEnv(t)
	admin := env.store.addUser("admin", "pw", db.RoleAdmin)

	oversized := "data:image/png;base64," + base64.StdEncoding.EncodeToString(
		make([]byte, maxMarkBytes+1))

	rec := env.do(t, http.MethodPut, "/api/v1/branding", env.tokenFor(t, admin), map[string]string{
		"organisation_mark": oversized,
	})
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected status %d, got %d (%s)", http.StatusBadRequest, rec.Code, rec.Body.String())
	}
}

func TestBrandingRefusesAnOverlongName(t *testing.T) {
	env := newTestEnv(t)
	admin := env.store.addUser("admin", "pw", db.RoleAdmin)

	rec := env.do(t, http.MethodPut, "/api/v1/branding", env.tokenFor(t, admin), map[string]string{
		"organisation_name": strings.Repeat("a", maxOrganisationNameLen+1),
	})
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected status %d, got %d (%s)", http.StatusBadRequest, rec.Code, rec.Body.String())
	}
}

// A hand-edited row is the one input no form validates, so the read side has to
// answer for it too.
func TestBrandingReadRepairsAnUnknownStoredTone(t *testing.T) {
	env := newTestEnv(t)
	env.store.settings[db.SettingEnvironmentBannerText] = "PRODUCTION"
	env.store.settings[db.SettingEnvironmentBannerTone] = "chartreuse"

	rec := env.do(t, http.MethodGet, "/api/v1/branding", "", nil)
	body := decode[brandingResponse](t, rec)
	if body.BannerTone != BannerToneNeutral {
		t.Fatalf("expected an unreadable tone to fall back to %q, got %q", BannerToneNeutral, body.BannerTone)
	}
}
