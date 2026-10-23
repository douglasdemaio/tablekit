// Package email sends transactional email. In development without SMTP the
// message is written to the application log so flows can be tested offline.
package email

import (
	"context"
	"crypto/tls"
	"fmt"
	"log/slog"
	"net"
	"net/smtp"
	"strings"
	"time"

	"tablekit.de/tablekit/internal/config"
)

type Sender interface {
	Send(ctx context.Context, to, subject, body string) error
	Configured() bool
}

func New(cfg *config.Config, logger *slog.Logger) Sender {
	if cfg.SMTPHost == "" {
		return &LogSender{logger: logger}
	}
	return &SMTPSender{
		host: cfg.SMTPHost,
		port: cfg.SMTPPort,
		user: cfg.SMTPUsername,
		pass: cfg.SMTPPassword,
		from: cfg.SMTPFrom,
		tls:  cfg.SMTPTLS,
	}
}

type LogSender struct {
	logger *slog.Logger
}

func (s *LogSender) Configured() bool { return false }

func (s *LogSender) Send(_ context.Context, to, subject, body string) error {
	s.logger.Info("development email", "to", to, "subject", subject, "body", body)
	return nil
}

type SMTPSender struct {
	host string
	port int
	user string
	pass string
	from string
	tls  bool
}

func (s *SMTPSender) Configured() bool { return true }

func (s *SMTPSender) Send(ctx context.Context, to, subject, body string) error {
	addr := net.JoinHostPort(s.host, fmt.Sprint(s.port))
	headers := []string{
		"From: " + s.from,
		"To: " + to,
		"Subject: " + subject,
		"MIME-Version: 1.0",
		"Content-Type: text/plain; charset=UTF-8",
		"Date: " + time.Now().UTC().Format(time.RFC1123Z),
	}
	msg := []byte(strings.Join(headers, "\r\n") + "\r\n\r\n" + body + "\r\n")

	var auth smtp.Auth
	if s.user != "" {
		auth = smtp.PlainAuth("", s.user, s.pass, s.host)
	}

	conn, err := (&net.Dialer{Timeout: 10 * time.Second}).DialContext(ctx, "tcp", addr)
	if err != nil {
		return fmt.Errorf("smtp dial: %w", err)
	}
	if s.port == 465 {
		conn = tls.Client(conn, &tls.Config{ServerName: s.host, MinVersion: tls.VersionTLS12})
	}
	c, err := smtp.NewClient(conn, s.host)
	if err != nil {
		_ = conn.Close()
		return fmt.Errorf("smtp client: %w", err)
	}
	defer c.Close()
	if s.tls && s.port != 465 {
		if ok, _ := c.Extension("STARTTLS"); ok {
			if err := c.StartTLS(&tls.Config{ServerName: s.host, MinVersion: tls.VersionTLS12}); err != nil {
				return fmt.Errorf("smtp starttls: %w", err)
			}
		}
	}
	if auth != nil {
		if err := c.Auth(auth); err != nil {
			return fmt.Errorf("smtp auth: %w", err)
		}
	}
	if err := c.Mail(extractAddress(s.from)); err != nil {
		return fmt.Errorf("smtp mail: %w", err)
	}
	if err := c.Rcpt(to); err != nil {
		return fmt.Errorf("smtp rcpt: %w", err)
	}
	w, err := c.Data()
	if err != nil {
		return fmt.Errorf("smtp data: %w", err)
	}
	if _, err := w.Write(msg); err != nil {
		return fmt.Errorf("smtp write: %w", err)
	}
	if err := w.Close(); err != nil {
		return fmt.Errorf("smtp close: %w", err)
	}
	return c.Quit()
}

func extractAddress(from string) string {
	if i := strings.LastIndex(from, "<"); i >= 0 {
		if j := strings.LastIndex(from, ">"); j > i {
			return from[i+1 : j]
		}
	}
	return strings.TrimSpace(from)
}
