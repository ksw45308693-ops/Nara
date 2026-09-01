package app

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/mail"
	"net/smtp"
	"strconv"
	"strings"
	"sync"
	"time"

	"g2b-monitor/internal/matcher"
	"g2b-monitor/internal/model"
	"g2b-monitor/internal/procurement"
)

type RuleMatcher struct{}

func (RuleMatcher) Match(now time.Time, notice model.Notice, rule matcher.Rule) matcher.Result {
	return matcher.MatchAt(now, notice, rule)
}

type ProcurementSource struct {
	BaseURL, ServiceKey string
	HTTPClient          *http.Client
	LookupBudget        int
	CallBudget          procurement.CallBudget
	regionOnce          sync.Once
	regionClient        *procurement.Client
}

func (s *ProcurementSource) Fetch(ctx context.Context, category model.Category, start, end time.Time) (FetchResult, error) {
	if s == nil {
		return FetchResult{}, errors.New("procurement source is required")
	}
	result := FetchResult{}
	client := procurement.NewClient(procurement.Config{
		BaseURL: s.BaseURL, ServiceKey: s.ServiceKey, HTTPClient: s.HTTPClient,
		CallBudget: s.CallBudget,
		Warning: func(w procurement.FieldWarning) {
			result.Warnings = append(result.Warnings, SourceWarning{
				Category: category, Page: w.Page, Item: w.Item, Field: w.Field, Code: w.Code, RawJSON: w.RawJSON,
			})
		},
	})
	notices, err := client.List(ctx, category, procurement.ListQuery{StartDate: start, EndDate: end, PageSize: 1000})
	if err != nil {
		return FetchResult{}, err
	}
	result.Notices = notices
	return result, nil
}

func (s *ProcurementSource) LookupRegion(ctx context.Context, bidNumber, bidSequence string) (string, error) {
	if s == nil {
		return "", errors.New("procurement source is required")
	}
	s.regionOnce.Do(func() {
		s.regionClient = procurement.NewClient(procurement.Config{
			BaseURL: s.BaseURL, ServiceKey: s.ServiceKey, HTTPClient: s.HTTPClient,
			LookupBudget: s.LookupBudget, CallBudget: s.CallBudget,
		})
	})
	return s.regionClient.LookupRegion(ctx, bidNumber, bidSequence)
}

type SMTPMailer struct {
	Host, User, Password string
	Port                 int
	Timeout              time.Duration
	AllowInsecure        bool
}

func (m SMTPMailer) Send(ctx context.Context, from, to string, message []byte) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	fromAddress, err := mail.ParseAddress(from)
	if err != nil {
		return fmt.Errorf("parse sender: %w", err)
	}
	toAddress, err := mail.ParseAddress(to)
	if err != nil {
		return fmt.Errorf("parse recipient: %w", err)
	}
	if strings.TrimSpace(m.Host) == "" || m.Port < 1 || m.Port > 65535 {
		return errors.New("valid SMTP host and port are required")
	}
	timeout := m.Timeout
	if timeout <= 0 {
		timeout = 30 * time.Second
	}
	address := net.JoinHostPort(m.Host, strconv.Itoa(m.Port))
	conn, err := (&net.Dialer{Timeout: timeout}).DialContext(ctx, "tcp", address)
	if err != nil {
		return fmt.Errorf("dial SMTP: %w", err)
	}
	defer conn.Close()
	deadline := time.Now().Add(timeout)
	if contextDeadline, ok := ctx.Deadline(); ok && contextDeadline.Before(deadline) {
		deadline = contextDeadline
	}
	if err := conn.SetDeadline(deadline); err != nil {
		return fmt.Errorf("set SMTP deadline: %w", err)
	}
	canceled := make(chan struct{})
	go func() {
		select {
		case <-ctx.Done():
			_ = conn.Close()
		case <-canceled:
		}
	}()
	defer close(canceled)

	client, err := smtp.NewClient(conn, m.Host)
	if err != nil {
		return fmt.Errorf("open SMTP client: %w", err)
	}
	defer client.Close()
	if ok, _ := client.Extension("STARTTLS"); ok {
		if err := client.StartTLS(&tls.Config{ServerName: m.Host, MinVersion: tls.VersionTLS12}); err != nil {
			return fmt.Errorf("start SMTP TLS: %w", err)
		}
	} else if !m.AllowInsecure {
		return errors.New("SMTP server does not offer STARTTLS")
	}
	if m.User != "" {
		if err := client.Auth(smtp.PlainAuth("", m.User, m.Password, m.Host)); err != nil {
			return fmt.Errorf("authenticate SMTP: %w", err)
		}
	}
	if err := client.Mail(fromAddress.Address); err != nil {
		return fmt.Errorf("set SMTP sender: %w", err)
	}
	if err := client.Rcpt(toAddress.Address); err != nil {
		return fmt.Errorf("set SMTP recipient: %w", err)
	}
	writer, err := client.Data()
	if err != nil {
		return fmt.Errorf("begin SMTP data: %w", err)
	}
	if _, err := writer.Write(message); err != nil {
		_ = writer.Close()
		return fmt.Errorf("write SMTP data: %w", err)
	}
	if err := writer.Close(); err != nil {
		return fmt.Errorf("finish SMTP data: %w", err)
	}
	if err := client.Quit(); err != nil {
		return fmt.Errorf("quit SMTP: %w", err)
	}
	return nil
}
