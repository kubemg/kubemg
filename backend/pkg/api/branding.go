package api

import (
	"encoding/base64"
	"fmt"
	"net/http"
	"slices"
	"strings"

	"github.com/gin-gonic/gin"

	"github.com/kubemg/kubemg/backend/pkg/db"
)

// Branding is the customer's own identity on their console: the name of the
// team or company running it, their mark beside the lockup, a banner saying
// which installation this is, and a line along the bottom.
//
// It is not decoration. An operator with a production console and a staging one
// open in two tabs has, today, no way to tell them apart before typing into one
// of them — and the console's own identity is deliberately uniform, so there is
// nothing accidental to read either. A banner is how that is fixed, which is
// also why it is rendered before sign-in rather than after: the keystrokes worth
// protecting are the ones going into the password field.
//
// The brand rules hold through all of it. The lockup stays lowercase `kubemg`
// and stays where it is — an organisation's mark sits *beside* it rather than
// replacing it, because a console that presents itself as somebody else's
// product is one nobody can get support for. The banner's tones are the
// semantic three (sage, amber, rust) and never the accent: lime means "you can
// press this", and a banner is not pressable.
type brandingResponse struct {
	// OrganisationName is drawn beside the lockup. Empty means the console
	// carries no second name, which is the default and a complete answer.
	OrganisationName string `json:"organisation_name,omitempty"`
	// OrganisationMark is a bounded data: URI. See validateMark for why it is
	// stored rather than linked.
	OrganisationMark string `json:"organisation_mark,omitempty"`
	// BannerText is the strip across the top of every page. Empty means no
	// banner, and no banner is the default: a console that always shouts is one
	// nobody reads.
	BannerText string `json:"banner_text,omitempty"`
	// BannerTone is how loudly it is drawn. It is only meaningful alongside
	// BannerText.
	BannerTone string `json:"banner_tone,omitempty"`
	// FooterNotice is the classification line under every page — "Internal —
	// Restricted", a handling caveat, whatever the site is obliged to state.
	FooterNotice string `json:"footer_notice,omitempty"`
}

// Banner tones. Three, and they are the deck's semantic colours rather than a
// palette of its own — an operator who has learned what amber means on a pod's
// phase should not have to learn a second meaning for it on a banner.
const (
	// BannerToneNeutral is an ordinary statement of fact: which install this is.
	BannerToneNeutral = "neutral"
	// BannerToneCaution says handle with care — a staging console wired to
	// production data, a maintenance window.
	BannerToneCaution = "caution"
	// BannerToneCritical is for the console where a mistake is expensive.
	BannerToneCritical = "critical"
)

var bannerTones = []string{BannerToneNeutral, BannerToneCaution, BannerToneCritical}

// Bounds. Each one is the point past which the thing stops working as what it
// is rather than an arbitrary number: a name longer than this stops fitting
// beside the lockup, a banner longer than this stops being read, and a mark
// larger than this stops being a mark and starts being a page weight every
// viewer pays for on every load.
const (
	maxOrganisationNameLen = 60
	maxBannerTextLen       = 120
	maxFooterNoticeLen     = 160
	maxMarkBytes           = 64 * 1024
)

// markMediaTypes are the image types a mark may be. SVG is deliberately absent:
// an SVG is a document that can carry script and external references, and this
// one is uploaded by an administrator and then rendered in every viewer's
// browser including on the unauthenticated sign-in page. A raster mark loses
// nothing that matters at the size a mark is drawn.
var markMediaTypes = []string{"image/png", "image/jpeg", "image/gif", "image/webp"}

// branding resolves the stored branding. A store failure reads as "no branding
// configured" rather than as an error, for the same reason the settings
// resolver falls back to its environment values: this is drawn on the sign-in
// page, and a console that will not render because it could not read an
// optional banner is a worse failure than a console with no banner.
func (s *server) branding(c *gin.Context) brandingResponse {
	stored, err := s.store.Settings(c.Request.Context())
	if err != nil {
		return brandingResponse{}
	}

	out := brandingResponse{
		OrganisationName: strings.TrimSpace(stored[db.SettingOrganisationName]),
		OrganisationMark: strings.TrimSpace(stored[db.SettingOrganisationMark]),
		BannerText:       strings.TrimSpace(stored[db.SettingEnvironmentBannerText]),
		BannerTone:       strings.TrimSpace(stored[db.SettingEnvironmentBannerTone]),
		FooterNotice:     strings.TrimSpace(stored[db.SettingFooterNotice]),
	}

	// A tone with no text is not a banner, and a hand-edited row must not be
	// able to produce one the form cannot then explain.
	if out.BannerText == "" {
		out.BannerTone = ""
	} else if !slices.Contains(bannerTones, out.BannerTone) {
		out.BannerTone = BannerToneNeutral
	}
	return out
}

// getBranding serves the console's own identity.
//
// It is unauthenticated on purpose, and it is the second route here that is —
// the first being the setup state the sign-in page reads. The reasoning is not
// the same, though, so it is worth writing down: setup answers one boolean
// because a stranger must learn nothing, whereas branding is content an
// administrator has deliberately chosen to publish on the page strangers see.
// The banner exists to be read before sign-in; served only afterwards it would
// warn people about the console they have already typed a password into.
//
// It carries nothing that is not that: no version, no provider, no address, no
// cluster. Everything here was typed into a form by an administrator who could
// see the sign-in page while doing it.
func (s *server) getBranding(c *gin.Context) {
	c.JSON(http.StatusOK, s.branding(c))
}

type updateBrandingRequest struct {
	OrganisationName *string `json:"organisation_name"`
	OrganisationMark *string `json:"organisation_mark"`
	BannerText       *string `json:"banner_text"`
	BannerTone       *string `json:"banner_tone"`
	FooterNotice     *string `json:"footer_notice"`
}

// updateBranding stores the console's identity (admin only). The settings
// convention holds: an omitted field is left alone, and a field sent empty is
// cleared.
func (s *server) updateBranding(c *gin.Context) {
	caller, ok := s.currentUser(c)
	if !ok {
		return
	}

	var req updateBrandingRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	values := map[string]string{}

	if req.OrganisationName != nil {
		name := strings.TrimSpace(*req.OrganisationName)
		if len([]rune(name)) > maxOrganisationNameLen {
			c.JSON(http.StatusBadRequest, gin.H{
				"error": fmt.Sprintf("the organisation name must be %d characters or fewer", maxOrganisationNameLen),
			})
			return
		}
		values[db.SettingOrganisationName] = name
	}

	if req.OrganisationMark != nil {
		mark := strings.TrimSpace(*req.OrganisationMark)
		if mark != "" {
			if err := validateMark(mark); err != nil {
				c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
				return
			}
		}
		values[db.SettingOrganisationMark] = mark
	}

	if req.BannerText != nil {
		text := singleLine(*req.BannerText)
		if len([]rune(text)) > maxBannerTextLen {
			c.JSON(http.StatusBadRequest, gin.H{
				"error": fmt.Sprintf("the banner must be %d characters or fewer", maxBannerTextLen),
			})
			return
		}
		values[db.SettingEnvironmentBannerText] = text
	}

	if req.BannerTone != nil {
		tone := strings.ToLower(strings.TrimSpace(*req.BannerTone))
		if tone != "" && !slices.Contains(bannerTones, tone) {
			c.JSON(http.StatusBadRequest, gin.H{
				"error": fmt.Sprintf("the banner tone must be one of %s", strings.Join(bannerTones, ", ")),
			})
			return
		}
		values[db.SettingEnvironmentBannerTone] = tone
	}

	if req.FooterNotice != nil {
		notice := singleLine(*req.FooterNotice)
		if len([]rune(notice)) > maxFooterNoticeLen {
			c.JSON(http.StatusBadRequest, gin.H{
				"error": fmt.Sprintf("the footer notice must be %d characters or fewer", maxFooterNoticeLen),
			})
			return
		}
		values[db.SettingFooterNotice] = notice
	}

	if len(values) == 0 {
		c.JSON(http.StatusOK, s.branding(c))
		return
	}

	if err := s.store.PutSettings(c.Request.Context(), values, caller.ID); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "could not save the branding"})
		return
	}

	c.JSON(http.StatusOK, s.branding(c))
}

// validateMark checks an organisation mark is a small raster image carried
// inline.
//
// It is a data: URI rather than a URL because both alternatives are worse. A
// URL means every viewer's browser fetches an image from somewhere else on
// every page load — which an air-gapped console cannot do at all, and which on
// a console that can turns the sign-in page into a beacon telling a third party
// who is looking at it and from where. An upload endpoint with a file on disk
// means a volume to mount, back up and mirror for one small image. A bounded
// row in the settings table travels with the database that already has to be
// backed up, and costs nothing to restore.
func validateMark(mark string) error {
	const prefix = "data:"
	if !strings.HasPrefix(mark, prefix) {
		return fmt.Errorf("the mark must be an inline data: URI — an image file, not a link to one")
	}

	head, payload, found := strings.Cut(mark[len(prefix):], ",")
	if !found {
		return fmt.Errorf("the mark is not a readable data: URI")
	}

	mediaType, encoding, _ := strings.Cut(head, ";")
	mediaType = strings.ToLower(strings.TrimSpace(mediaType))
	if !slices.Contains(markMediaTypes, mediaType) {
		return fmt.Errorf("the mark must be one of %s — an SVG can carry script and is refused",
			strings.Join(markMediaTypes, ", "))
	}
	if strings.ToLower(strings.TrimSpace(encoding)) != "base64" {
		return fmt.Errorf("the mark must be base64-encoded")
	}

	// Measure the decoded image rather than the URI: base64 is a third larger
	// than what it carries, and the number an administrator is told to stay
	// under has to be the size of the file they picked.
	decoded, err := base64.StdEncoding.DecodeString(payload)
	if err != nil {
		return fmt.Errorf("the mark is not valid base64")
	}
	if len(decoded) == 0 {
		return fmt.Errorf("the mark is empty")
	}
	if len(decoded) > maxMarkBytes {
		return fmt.Errorf("the mark must be %d KB or smaller — it is drawn at 28 pixels", maxMarkBytes/1024)
	}
	return nil
}

// singleLine folds any whitespace run to one space. A banner and a footer are
// each one line by construction — a newline in either would push the page's
// content down by however many an administrator happened to paste.
func singleLine(raw string) string {
	return strings.Join(strings.Fields(raw), " ")
}
