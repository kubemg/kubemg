package api

import (
	"bufio"
	"bytes"
	"compress/gzip"
	"context"
	"encoding/csv"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/kubemg/kubemg/backend/pkg/db"
)

/*
 * Exploit intelligence — KEV and EPSS — kept current behind a lease, in the
 * Helm index's shape.
 *
 * Both lists are off until an administrator turns them on, because turning one
 * on is KubeMG reaching the internet on its own. An air-gapped installation
 * uploads the file instead; an uploaded list is used whether or not the
 * scheduled fetch is on. A failed fetch or a malformed upload never replaces
 * what is stored.
 *
 * Nothing about the cluster leaves: the whole list is downloaded and matched
 * here. Asking a service "which of these CVEs are exploited" would hand it the
 * cluster's vulnerability map.
 */

const (
	// vulnIntelInterval: CISA updates KEV on working days and FIRST publishes
	// EPSS daily, so once a day is the data's own cadence.
	vulnIntelInterval     = 24 * time.Hour
	vulnIntelStartupDelay = 3 * time.Minute
	vulnIntelLeaseTTL     = 2 * time.Hour
	vulnIntelFetchTimeout = 3 * time.Minute

	// maxKEVBytes bounds the KEV document (about 1.5 MB today).
	maxKEVBytes = 32 << 20
	// maxEPSSBytes bounds the EPSS file, compressed or not, and its
	// decompressed size (about 300k rows, 10 MB compressed today).
	maxEPSSBytes        = 128 << 20
	maxEPSSDecompressed = 256 << 20
)

/* -------------------------------------------------------------- parsing --- */

type kevDocument struct {
	CatalogVersion  string `json:"catalogVersion"`
	DateReleased    string `json:"dateReleased"`
	Vulnerabilities []struct {
		CVEID                      string `json:"cveID"`
		VendorProject              string `json:"vendorProject"`
		Product                    string `json:"product"`
		VulnerabilityName          string `json:"vulnerabilityName"`
		DateAdded                  string `json:"dateAdded"`
		DueDate                    string `json:"dueDate"`
		KnownRansomwareCampaignUse string `json:"knownRansomwareCampaignUse"`
	} `json:"vulnerabilities"`
}

func parseDate(raw string) *time.Time {
	t, err := time.Parse("2006-01-02", strings.TrimSpace(raw))
	if err != nil {
		return nil
	}
	return &t
}

// parseKEV reads CISA's catalogue. A document with no entries is refused —
// replacing a catalogue with nothing is a failed download dressed as a list.
func parseKEV(body []byte) ([]db.VulnKEVEntry, string, error) {
	var doc kevDocument
	if err := json.Unmarshal(body, &doc); err != nil {
		return nil, "", errors.New("not a KEV catalogue: the document is not valid JSON")
	}
	seen := map[string]bool{}
	out := make([]db.VulnKEVEntry, 0, len(doc.Vulnerabilities))
	for _, v := range doc.Vulnerabilities {
		id := strings.ToUpper(strings.TrimSpace(v.CVEID))
		if !cveIDPattern.MatchString(id) || seen[id] {
			continue
		}
		seen[id] = true
		out = append(out, db.VulnKEVEntry{
			CVE: id, Vendor: clip(v.VendorProject, 200), Product: clip(v.Product, 200),
			Name: clip(v.VulnerabilityName, 1000), DateAdded: parseDate(v.DateAdded), DueDate: parseDate(v.DueDate),
			Ransomware: strings.EqualFold(strings.TrimSpace(v.KnownRansomwareCampaignUse), "Known"),
		})
	}
	if len(out) == 0 {
		return nil, "", errors.New("not a KEV catalogue: it lists no vulnerabilities")
	}
	version := strings.TrimSpace(doc.CatalogVersion)
	if version == "" {
		version = strings.TrimSpace(doc.DateReleased)
	}
	return out, clip(version, 80), nil
}

func clip(value string, limit int) string {
	value = strings.TrimSpace(value)
	if len(value) > limit {
		return value[:limit]
	}
	return value
}

// parseEPSS reads FIRST's CSV, gzip or plain: an optional `#model_version:…`
// comment line, a `cve,epss,percentile` header, then rows. Scores below
// db.EpssKeepThreshold are dropped.
func parseEPSS(body []byte) ([]db.VulnEPSSEntry, string, error) {
	var reader io.Reader = bytes.NewReader(body)
	if len(body) > 2 && body[0] == 0x1f && body[1] == 0x8b {
		gz, err := gzip.NewReader(bytes.NewReader(body))
		if err != nil {
			return nil, "", errors.New("not an EPSS file: the gzip stream could not be opened")
		}
		defer gz.Close()
		reader = io.LimitReader(gz, maxEPSSDecompressed)
	}
	buffered := bufio.NewReader(reader)
	version := ""
	if peek, _ := buffered.Peek(1); len(peek) == 1 && peek[0] == '#' {
		line, _ := buffered.ReadString('\n')
		for _, part := range strings.Split(strings.TrimPrefix(strings.TrimSpace(line), "#"), ",") {
			if key, value, ok := strings.Cut(part, ":"); ok && strings.TrimSpace(key) == "score_date" {
				version = clip(value, 80)
			}
		}
	}
	rows := csv.NewReader(buffered)
	rows.FieldsPerRecord = -1
	header, err := rows.Read()
	if err != nil || len(header) < 3 || strings.TrimSpace(strings.ToLower(header[0])) != "cve" {
		return nil, "", errors.New("not an EPSS file: expected a cve,epss,percentile header")
	}
	out := []db.VulnEPSSEntry{}
	total := 0
	for {
		record, err := rows.Read()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, "", fmt.Errorf("not an EPSS file: %v", err)
		}
		if len(record) < 3 {
			continue
		}
		total++
		score, errScore := strconv.ParseFloat(strings.TrimSpace(record[1]), 64)
		percentile, errPct := strconv.ParseFloat(strings.TrimSpace(record[2]), 64)
		id := strings.ToUpper(strings.TrimSpace(record[0]))
		if errScore != nil || errPct != nil || !cveIDPattern.MatchString(id) || score < db.EpssKeepThreshold {
			continue
		}
		out = append(out, db.VulnEPSSEntry{CVE: id, Score: score, Percentile: percentile})
	}
	if total == 0 {
		return nil, "", errors.New("not an EPSS file: it has no scores")
	}
	return out, version, nil
}

/* ------------------------------------------------------------- fetching --- */

func validIntelURL(raw string) error {
	parsed, err := url.Parse(raw)
	if err != nil || (parsed.Scheme != "https" && parsed.Scheme != "http") || parsed.Host == "" {
		return errors.New("the address must be an http or https URL")
	}
	if parsed.User != nil {
		return errors.New("the address must not carry credentials")
	}
	return nil
}

func fetchIntel(ctx context.Context, address string, limit int64) ([]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, vulnIntelFetchTimeout)
	defer cancel()
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, address, nil)
	if err != nil {
		return nil, err
	}
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		return nil, fmt.Errorf("could not reach %s: %w", request.URL.Host, err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("%s answered %s", request.URL.Host, response.Status)
	}
	body, err := io.ReadAll(io.LimitReader(response.Body, limit+1))
	if err != nil {
		return nil, fmt.Errorf("the download from %s was cut short: %w", request.URL.Host, err)
	}
	if int64(len(body)) > limit {
		return nil, fmt.Errorf("the file from %s is larger than %d MB", request.URL.Host, limit>>20)
	}
	return body, nil
}

func intelLimit(kind string) int64 {
	if kind == db.IntelEPSS {
		return maxEPSSBytes
	}
	return maxKEVBytes
}

// storeIntel parses a list and, only if it parsed, replaces the stored one.
func (s *server) storeIntel(ctx context.Context, kind, origin string, body []byte) (int, error) {
	now := time.Now().UTC()
	switch kind {
	case db.IntelKEV:
		entries, version, err := parseKEV(body)
		if err != nil {
			return 0, err
		}
		message := fmt.Sprintf("%d known-exploited vulnerabilities.", len(entries))
		return len(entries), s.store.ReplaceKEV(ctx, entries, db.IntelOutcome{Origin: origin, Version: version, Message: message, At: now})
	default:
		entries, version, err := parseEPSS(body)
		if err != nil {
			return 0, err
		}
		message := fmt.Sprintf("%d scores of %.0f%% or more kept.", len(entries), db.EpssKeepThreshold*100)
		return len(entries), s.store.ReplaceEPSS(ctx, entries, db.IntelOutcome{Origin: origin, Version: version, Message: message, At: now})
	}
}

// syncIntel fetches one source now, recording a failure on its row.
func (s *server) syncIntel(ctx context.Context, source db.VulnIntelSource) error {
	address := source.URL
	if strings.TrimSpace(address) == "" {
		address = db.DefaultIntelURL(source.Kind)
	}
	body, err := fetchIntel(ctx, address, intelLimit(source.Kind))
	if err == nil {
		_, err = s.storeIntel(ctx, source.Kind, "sync", body)
	}
	if err != nil {
		_ = s.store.RecordVulnIntelFailure(ctx, source.Kind, "The last refresh failed: "+err.Error())
		return err
	}
	return nil
}

func (s *server) startVulnIntelSync(ctx context.Context) {
	select {
	case <-ctx.Done():
		return
	case <-time.After(vulnIntelStartupDelay):
		s.vulnIntelTick(ctx)
	}
	ticker := time.NewTicker(vulnIntelInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			s.vulnIntelTick(ctx)
		}
	}
}

func (s *server) vulnIntelTick(ctx context.Context) {
	held, err := s.store.AcquireLease(ctx, db.LeaseVulnIntel, s.instanceID, vulnIntelLeaseTTL)
	if err != nil {
		slog.Error("vulnerability intelligence lease", "error", err)
		return
	}
	if !held {
		return
	}
	sources, err := s.store.EnabledVulnIntel(ctx)
	if err != nil {
		slog.Error("vulnerability intelligence sources", "error", err)
		return
	}
	for _, source := range sources {
		if ctx.Err() != nil {
			return
		}
		// Not more often than the interval, whichever replica held the lease
		// last: a restart is not a reason to download the list again.
		if source.Origin == "sync" && source.SyncedAt != nil && time.Since(*source.SyncedAt) < vulnIntelInterval-time.Hour {
			continue
		}
		if err := s.syncIntel(ctx, source); err != nil {
			slog.Warn("vulnerability intelligence sync", "kind", source.Kind, "error", err)
		}
	}
}

/* ------------------------------------------------------------- handlers --- */

func (s *server) listVulnIntel(c *gin.Context) {
	sources, err := s.store.VulnIntelSources(c.Request.Context())
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "could not load the intelligence sources"})
		return
	}
	c.JSON(http.StatusOK, gin.H{"sources": sources, "epss_threshold": db.EpssKeepThreshold})
}

func intelKind(c *gin.Context) (string, bool) {
	kind := strings.TrimSpace(c.Param("kind"))
	if !db.ValidIntelKind(kind) {
		c.JSON(http.StatusNotFound, gin.H{"error": "unknown intelligence source; use kev or epss"})
		return "", false
	}
	return kind, true
}

func (s *server) putVulnIntel(c *gin.Context) {
	kind, ok := intelKind(c)
	if !ok {
		return
	}
	var request struct {
		Enabled bool   `json:"enabled"`
		URL     string `json:"url"`
	}
	if err := c.ShouldBindJSON(&request); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "the request could not be read"})
		return
	}
	address := strings.TrimSpace(request.URL)
	if address == "" {
		address = db.DefaultIntelURL(kind)
	}
	if err := validIntelURL(address); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	if err := s.store.PutVulnIntelSource(c.Request.Context(), kind, request.Enabled, address); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "could not save the source"})
		return
	}
	s.listVulnIntel(c)
}

// syncVulnIntelNow fetches once, now — an explicit act, so it runs whether or
// not the scheduled fetch is on.
func (s *server) syncVulnIntelNow(c *gin.Context) {
	kind, ok := intelKind(c)
	if !ok {
		return
	}
	sources, err := s.store.VulnIntelSources(c.Request.Context())
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "could not load the intelligence sources"})
		return
	}
	for _, source := range sources {
		if source.Kind != kind {
			continue
		}
		if err := s.syncIntel(c.Request.Context(), source); err != nil {
			c.JSON(http.StatusBadGateway, gin.H{"error": err.Error()})
			return
		}
	}
	s.listVulnIntel(c)
}

// uploadVulnIntel replaces a list from a file an administrator brought in —
// the air-gapped path.
func (s *server) uploadVulnIntel(c *gin.Context) {
	kind, ok := intelKind(c)
	if !ok {
		return
	}
	limit := intelLimit(kind)
	body, err := io.ReadAll(io.LimitReader(c.Request.Body, limit+1))
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "the file could not be read"})
		return
	}
	if int64(len(body)) > limit {
		c.JSON(http.StatusRequestEntityTooLarge, gin.H{"error": fmt.Sprintf("the file is larger than %d MB", limit>>20)})
		return
	}
	if _, err := s.storeIntel(c.Request.Context(), kind, "upload", body); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	s.listVulnIntel(c)
}
