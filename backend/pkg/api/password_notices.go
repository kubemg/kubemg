package api

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"math"
	"strings"
	"time"

	"github.com/kubemg/kubemg/backend/pkg/db"
	"github.com/kubemg/kubemg/backend/pkg/mailer"
)

/*
 * Telling people before their password expires.
 *
 * A pass runs hourly on the replica holding the lease and mails each account
 * whose password falls due within the reminder stages — 14, 7, 3 and 1 days
 * before, and once when it has expired (an administrator's forced expiry is
 * that last one, sent at once). Each stage is mailed once per due date: the
 * account row records the last stage sent and the due date it was about, so a
 * new password starts the stages again without anything resetting them, and an
 * account first seen five days out gets the 7-day reminder rather than every
 * reminder it missed.
 *
 * No relay configured, no mail — and an account with no email address gets
 * none either; the console says both. A failed send is not recorded, so the
 * next pass tries again.
 */

// passwordNoticeStages are the reminders, in days before the due date. 0 is
// "it has expired".
var passwordNoticeStages = []int{14, 7, 3, 1, 0}

const (
	passwordNoticeInterval = time.Hour
	passwordNoticeLeaseTTL = 3 * passwordNoticeInterval
	// passwordNoticeMaxFailures ends a pass early: the relay is the same for
	// everyone, so a relay that refused three in a row will refuse the rest.
	passwordNoticeMaxFailures = 3
	passwordNoticeSendTimeout = 45 * time.Second
)

// passwordNoticeStage is the reminder owed now for a password due at due, and
// false while it is further away than the first stage.
func passwordNoticeStage(due, now time.Time) (int, bool) {
	left := due.Sub(now)
	if left <= 0 {
		return 0, true
	}
	days := int(math.Ceil(left.Hours() / 24))
	stage, found := 0, false
	for _, s := range passwordNoticeStages {
		if s > 0 && days <= s {
			stage, found = s, true
		}
	}
	return stage, found
}

// passwordNoticeSent reports whether the stage reminder for due already went.
func passwordNoticeSent(account db.PasswordAccount, due time.Time, stage int) bool {
	if account.PasswordNoticeDue == nil {
		return false
	}
	if !account.PasswordNoticeDue.Truncate(time.Second).Equal(due.Truncate(time.Second)) {
		return false
	}
	return account.PasswordNoticeDays <= stage
}

// startPasswordNotices runs a pass every interval for as long as ctx lives.
func (s *server) startPasswordNotices(ctx context.Context) {
	ticker := time.NewTicker(passwordNoticeInterval)
	defer ticker.Stop()
	for {
		s.sendPasswordNotices(ctx)
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

// kickPasswordNotices runs a pass now, off the request, so a forced expiry is
// mailed without waiting for the hour. The lease keeps it from racing the
// timer's pass.
func (s *server) kickPasswordNotices() {
	if s.jobs == nil {
		return
	}
	go s.sendPasswordNotices(s.jobs)
}

// sendPasswordNotices is one pass.
func (s *server) sendPasswordNotices(ctx context.Context) {
	held, err := s.store.AcquireLease(ctx, db.LeasePasswordNotices, s.instanceID, passwordNoticeLeaseTTL)
	if err != nil || !held {
		// A store error is "do not run", the lease's rule: every replica would
		// get it, and all of them mailing is what the lease exists to prevent.
		if err != nil && ctx.Err() == nil {
			s.log().Warn("password reminders could not take their lease", slog.String("error", err.Error()))
		}
		return
	}
	relay, err := s.store.SMTPConfig(ctx)
	if errors.Is(err, db.ErrNotFound) {
		return
	}
	if err != nil {
		s.log().Warn("password reminders could not read the mail relay", slog.String("error", err.Error()))
		return
	}
	accounts, err := s.store.PasswordAccounts(ctx)
	if err != nil {
		s.log().Warn("password reminders could not list accounts", slog.String("error", err.Error()))
		return
	}
	resolved := s.settings(ctx)
	config := smtpMailerConfig(relay)
	now := time.Now().UTC()
	failures := 0
	for _, account := range accounts {
		if ctx.Err() != nil || failures >= passwordNoticeMaxFailures {
			return
		}
		if strings.TrimSpace(account.Email) == "" {
			continue
		}
		due, ok := passwordDue(account, resolved.PasswordMaxAgeDays)
		if !ok {
			continue
		}
		stage, ok := passwordNoticeStage(due, now)
		if !ok || passwordNoticeSent(account, due, stage) {
			continue
		}
		forced := account.PasswordExpiredAt != nil && account.PasswordExpiredAt.Equal(due)
		msg := passwordNoticeMessage(account, due, stage, forced, resolved.PublicURL)
		sendCtx, cancel := context.WithTimeout(ctx, passwordNoticeSendTimeout)
		err := s.mail(sendCtx, config, msg)
		cancel()
		if err != nil {
			failures++
			s.log().Warn("could not mail a password reminder",
				slog.String("username", account.Username), slog.String("error", err.Error()))
			continue
		}
		failures = 0
		if err := s.store.RecordPasswordNotice(ctx, account.ID, due, stage); err != nil {
			s.log().Warn("could not record a password reminder; it may be sent again",
				slog.String("username", account.Username), slog.String("error", err.Error()))
		}
	}
}

// passwordNoticeMessage is the reminder itself. It names the account and the
// date and says where the password is changed — never a link that signs in.
func passwordNoticeMessage(account db.PasswordAccount, due time.Time, stage int, forced bool, publicURL string) mailer.Message {
	when := due.UTC().Format("Monday 2 January 2006, 15:04 MST")
	profile := strings.TrimRight(publicURL, "/") + "/me/profile"
	var subject, lead string
	switch {
	case forced:
		subject = "An administrator has asked you to change your KubeMG password"
		lead = fmt.Sprintf("An administrator has required a new password for your KubeMG account %q.", account.Username)
	case stage == 0:
		subject = "Your KubeMG password has expired"
		lead = fmt.Sprintf("The password for your KubeMG account %q expired on %s.", account.Username, when)
	case stage == 1:
		subject = "Your KubeMG password expires within a day"
		lead = fmt.Sprintf("The password for your KubeMG account %q expires on %s.", account.Username, when)
	default:
		subject = fmt.Sprintf("Your KubeMG password expires within %d days", stage)
		lead = fmt.Sprintf("The password for your KubeMG account %q expires on %s.", account.Username, when)
	}
	var body strings.Builder
	body.WriteString("Hello " + account.Username + ",\n\n" + lead + "\n\n")
	if stage == 0 {
		body.WriteString("The next time you sign in you will be asked to choose a new password before " +
			"anything else. A console session already open is held at the same screen.\n")
	} else {
		body.WriteString("You can change it now from your profile:\n\n  " + profile + "\n\n" +
			"After it expires you can still sign in with it, and you will be asked to choose a new one " +
			"before anything else.\n")
	}
	body.WriteString("\nThis message was sent automatically by KubeMG. If you did not expect it, tell your " +
		"administrator.\n")
	return mailer.Message{To: account.Email, Subject: subject, Body: body.String()}
}
