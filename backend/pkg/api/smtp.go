package api

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/kubemg/kubemg/backend/pkg/db"
	"github.com/kubemg/kubemg/backend/pkg/mailer"
)

// smtpTestTimeout bounds a test send, which an administrator is waiting on.
const smtpTestTimeout = 30 * time.Second

// smtpResponse is the relay as the console reads it. The password is never
// returned — only whether one is stored.
type smtpResponse struct {
	Configured  bool       `json:"configured"`
	Host        string     `json:"host,omitempty"`
	Port        int        `json:"port,omitempty"`
	Security    string     `json:"security,omitempty"`
	Username    string     `json:"username,omitempty"`
	HasPassword bool       `json:"has_password"`
	From        string     `json:"from,omitempty"`
	UpdatedAt   *time.Time `json:"updated_at,omitempty"`
}

type smtpRequest struct {
	Host     string `json:"host"`
	Port     int    `json:"port"`
	Security string `json:"security"`
	Username string `json:"username"`
	// Password is write-only: absent keeps the stored one, "" clears it.
	Password *string `json:"password"`
	From     string  `json:"from"`
}

type smtpTestRequest struct {
	// To defaults to the caller's own address.
	To string `json:"to"`
}

func toSMTPResponse(config *db.SMTPConfig) smtpResponse {
	if config == nil {
		return smtpResponse{}
	}
	updated := config.UpdatedAt
	return smtpResponse{
		Configured:  true,
		Host:        config.Host,
		Port:        config.Port,
		Security:    config.Security,
		Username:    config.Username,
		HasPassword: config.Password != "",
		From:        config.From,
		UpdatedAt:   &updated,
	}
}

func smtpMailerConfig(config *db.SMTPConfig) mailer.Config {
	return mailer.Config{
		Host:     config.Host,
		Port:     config.Port,
		Security: config.Security,
		Username: config.Username,
		Password: config.Password,
		From:     config.From,
	}
}

// loadSMTP reads the relay, answering nil when none is configured.
func (s *server) loadSMTP(ctx context.Context) (*db.SMTPConfig, error) {
	config, err := s.store.SMTPConfig(ctx)
	if errors.Is(err, db.ErrNotFound) {
		return nil, nil
	}
	return config, err
}

// getSMTP returns the relay (admin only).
func (s *server) getSMTP(c *gin.Context) {
	config, err := s.loadSMTP(c.Request.Context())
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "could not load the mail relay"})
		return
	}
	c.JSON(http.StatusOK, toSMTPResponse(config))
}

// putSMTP stores the relay (admin only). It is validated, never dialled —
// sending a test is its own act.
func (s *server) putSMTP(c *gin.Context) {
	caller, ok := s.currentUser(c)
	if !ok {
		return
	}
	var req smtpRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid request body"})
		return
	}
	ctx := c.Request.Context()
	existing, err := s.loadSMTP(ctx)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "could not load the mail relay"})
		return
	}

	config := db.SMTPConfig{
		Host:      strings.TrimSpace(req.Host),
		Port:      req.Port,
		Security:  strings.ToLower(strings.TrimSpace(req.Security)),
		Username:  strings.TrimSpace(req.Username),
		From:      strings.TrimSpace(req.From),
		UpdatedBy: caller.ID,
	}
	if config.Security == "" {
		config.Security = mailer.SecurityStartTLS
	}
	switch {
	case req.Password != nil:
		config.Password = *req.Password
	case existing != nil:
		config.Password = existing.Password
	}
	// A login with no password, or a password with no login, is a relay that
	// cannot work: the second would be sent nowhere.
	if config.Username == "" {
		config.Password = ""
	}
	if err := smtpMailerConfig(&config).Validate(); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	if err := s.store.PutSMTPConfig(ctx, &config); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "could not save the mail relay"})
		return
	}
	c.JSON(http.StatusOK, toSMTPResponse(&config))
}

// deleteSMTP removes the relay (admin only). Reminders stop.
func (s *server) deleteSMTP(c *gin.Context) {
	if err := s.store.DeleteSMTPConfig(c.Request.Context()); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "could not remove the mail relay"})
		return
	}
	c.Status(http.StatusNoContent)
}

// testSMTP sends one message through the stored relay (admin only), and
// answers with the relay's own words when it refuses.
func (s *server) testSMTP(c *gin.Context) {
	caller, ok := s.currentUser(c)
	if !ok {
		return
	}
	var req smtpTestRequest
	if c.Request.ContentLength != 0 {
		if err := c.ShouldBindJSON(&req); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "invalid request body"})
			return
		}
	}
	to := strings.TrimSpace(req.To)
	if to == "" {
		to = strings.TrimSpace(caller.Email)
	}
	if to == "" {
		c.JSON(http.StatusBadRequest, gin.H{
			"error": "your account has no email address — name a recipient for the test",
		})
		return
	}
	config, err := s.loadSMTP(c.Request.Context())
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "could not load the mail relay"})
		return
	}
	if config == nil {
		c.JSON(http.StatusConflict, gin.H{"error": "no mail relay is configured — save one first"})
		return
	}
	ctx, cancel := context.WithTimeout(c.Request.Context(), smtpTestTimeout)
	defer cancel()
	err = s.mail(ctx, smtpMailerConfig(config), mailer.Message{
		To:      to,
		Subject: "KubeMG test message",
		Body: "This is a test message from KubeMG, sent by " + caller.Username + " from the mail " +
			"relay settings. If you are reading it, password expiry reminders will reach this address.\n",
	})
	if err != nil {
		c.JSON(http.StatusBadGateway, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"sent": true, "to": to})
}
