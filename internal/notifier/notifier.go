package notifier

import (
	"fmt"
	"net/smtp"
)

// Notifier sends emails via SMTP
type Notifier struct {
	host string
	port string
	user string
	pass string
	from string
}

// create a new email Notifier
func New(host, port, user, pass, from string) *Notifier {
	return &Notifier{
		host: host,
		port: port,
		user: user,
		pass: pass,
		from: from,
	}
}

// sends an email with a confirmation link
func (n *Notifier) SendConfirmationEmail(to, confirmURL string) error {
	subject := "Confirm your GitHub release subscription"
	body := fmt.Sprintf(
		"Please confirm your subscription by clicking the link below:\n\n%s\n\nIf you did not subscribe, you can safely ignore this email.",
		confirmURL,
	)
	return n.sendEmail(to, subject, body)
}

// sends an email about a new release
func (n *Notifier) SendReleaseNotification(to, repo, tag, unsubscribeURL string) error {
	subject := fmt.Sprintf("New release: %s %s", repo, tag)
	body := fmt.Sprintf(
		"A new release has been published!\n\nRepository: %s\nRelease: %s\nURL: https://github.com/%s/releases/tag/%s\n\nTo unsubscribe: %s",
		repo, tag, repo, tag, unsubscribeURL,
	)
	return n.sendEmail(to, subject, body)
}

// low-level email sending function
func (n *Notifier) sendEmail(to, subject, body string) error {
	msg := fmt.Sprintf(
		"From: %s\r\nTo: %s\r\nSubject: %s\r\nMIME-Version: 1.0\r\nContent-Type: text/plain; charset=\"utf-8\"\r\n\r\n%s",
		n.from, to, subject, body,
	)
	// connects to (in my case Mailtrap) SMTP server and sends the email
	auth := smtp.PlainAuth("", n.user, n.pass, n.host)
	addr := fmt.Sprintf("%s:%s", n.host, n.port)

	return smtp.SendMail(addr, auth, n.from, []string{to}, []byte(msg))
}
