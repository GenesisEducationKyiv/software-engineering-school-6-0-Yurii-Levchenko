package main

import (
	"fmt"
	"net/smtp"
)

// SMTPSender is the SMTP-backed notification sender. It lives in the notifier
// service only — the monolith no longer imports net/smtp.
type SMTPSender struct {
	host string
	port string
	user string
	pass string
	from string
}

// NewSMTPSender creates a sender for the given SMTP server. Empty user/pass
// means no authentication (the local Mailpit setup).
func NewSMTPSender(host, port, user, pass, from string) *SMTPSender {
	return &SMTPSender{host: host, port: port, user: user, pass: pass, from: from}
}

// SendConfirmationEmail sends the subscribe-confirmation email.
func (s *SMTPSender) SendConfirmationEmail(to, confirmURL string) error {
	subject := "Confirm your GitHub release subscription"
	body := fmt.Sprintf(
		"Please confirm your subscription by clicking the link below:\n\n%s\n\nIf you did not subscribe, you can safely ignore this email.",
		confirmURL,
	)
	return s.sendEmail(to, subject, body)
}

// SendReleaseNotification sends the new-release email for a repo.
func (s *SMTPSender) SendReleaseNotification(to, repo, tag, unsubscribeURL string) error {
	subject := fmt.Sprintf("New release: %s %s", repo, tag)
	body := fmt.Sprintf(
		"A new release has been published!\n\nRepository: %s\nRelease: %s\nURL: https://github.com/%s/releases/tag/%s\n\nTo unsubscribe: %s",
		repo, tag, repo, tag, unsubscribeURL,
	)
	return s.sendEmail(to, subject, body)
}

func (s *SMTPSender) sendEmail(to, subject, body string) error {
	msg := fmt.Sprintf(
		"From: %s\r\nTo: %s\r\nSubject: %s\r\nMIME-Version: 1.0\r\nContent-Type: text/plain; charset=\"utf-8\"\r\n\r\n%s",
		s.from, to, subject, body,
	)
	addr := fmt.Sprintf("%s:%s", s.host, s.port)

	// Skip AUTH when no credentials are set (Mailpit / local relays accept
	// anonymous SMTP; PlainAuth with an empty user is rejected by some servers).
	var auth smtp.Auth
	if s.user != "" {
		auth = smtp.PlainAuth("", s.user, s.pass, s.host)
	}
	return smtp.SendMail(addr, auth, s.from, []string{to}, []byte(msg))
}
