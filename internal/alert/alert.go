// Package alert tells people when something needs them, by email or
// Telegram, using settings kept in the store.
package alert

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/smtp"
	"strconv"
	"strings"
	"time"

	"github.com/gsoultan/mimir-server/internal/store"
)

// Email is how to send mail.
type Email struct {
	Enabled  bool     `json:"enabled"`
	Host     string   `json:"host"`
	Port     int      `json:"port"`
	Username string   `json:"username"`
	Password string   `json:"password"`
	From     string   `json:"from"`
	To       []string `json:"to"`
}

// Telegram is how to message a chat through a bot.
type Telegram struct {
	Enabled  bool   `json:"enabled"`
	BotToken string `json:"bot_token"`
	ChatID   string `json:"chat_id"`
}

// Settings keys in the store.
const (
	KeyEmail    = "alerts.email"
	KeyTelegram = "alerts.telegram"
)

// Load reads both channels' settings.
func Load(ctx context.Context, st *store.Store) (Email, Telegram, error) {
	var e Email
	var t Telegram
	if raw, err := st.GetSetting(ctx, KeyEmail); err != nil {
		return e, t, err
	} else if raw != "" {
		if err := json.Unmarshal([]byte(raw), &e); err != nil {
			return e, t, err
		}
	}
	if raw, err := st.GetSetting(ctx, KeyTelegram); err != nil {
		return e, t, err
	} else if raw != "" {
		if err := json.Unmarshal([]byte(raw), &t); err != nil {
			return e, t, err
		}
	}
	return e, t, nil
}

// Save writes both channels' settings.
func Save(ctx context.Context, st *store.Store, e Email, t Telegram) error {
	eb, _ := json.Marshal(e)
	tb, _ := json.Marshal(t)
	if err := st.SetSetting(ctx, KeyEmail, string(eb)); err != nil {
		return err
	}
	return st.SetSetting(ctx, KeyTelegram, string(tb))
}

// Notifier sends alerts through every enabled channel.
type Notifier struct {
	store  *store.Store
	log    *slog.Logger
	client *http.Client
	// TelegramAPI can be changed in tests.
	TelegramAPI string
}

// New returns a Notifier.
func New(st *store.Store, log *slog.Logger) *Notifier {
	return &Notifier{store: st, log: log, client: &http.Client{Timeout: 15 * time.Second}, TelegramAPI: "https://api.telegram.org"}
}

// Notify sends subject and body everywhere that's enabled, logging
// failures rather than returning them: alerts are best effort.
func (n *Notifier) Notify(ctx context.Context, subject, body string) {
	e, t, err := Load(ctx, n.store)
	if err != nil {
		n.log.ErrorContext(ctx, "alerts: load settings", "error", err)
		return
	}
	if e.Enabled {
		if err := SendEmail(ctx, e, subject, body); err != nil {
			n.log.ErrorContext(ctx, "alerts: email", "error", err)
		}
	}
	if t.Enabled {
		if err := n.SendTelegram(ctx, t, subject+"\n\n"+body); err != nil {
			n.log.ErrorContext(ctx, "alerts: telegram", "error", err)
		}
	}
}

// SendEmail sends one message through SMTP, with STARTTLS when the server
// offers it.
func SendEmail(ctx context.Context, e Email, subject, body string) error {
	if e.Host == "" || e.From == "" || len(e.To) == 0 {
		return errors.New("email alerts need a server, a from address and at least one to address")
	}
	port := e.Port
	if port == 0 {
		port = 587
	}
	addr := net.JoinHostPort(e.Host, strconv.Itoa(port))
	var auth smtp.Auth
	if e.Username != "" {
		auth = smtp.PlainAuth("", e.Username, e.Password, e.Host)
	}
	msg := fmt.Sprintf("From: %s\r\nTo: %s\r\nSubject: %s\r\nDate: %s\r\nMIME-Version: 1.0\r\nContent-Type: text/plain; charset=utf-8\r\n\r\n%s\r\n",
		e.From, strings.Join(e.To, ", "), strings.ReplaceAll(subject, "\n", " "), time.Now().Format(time.RFC1123Z), body)
	done := make(chan error, 1)
	go func() { done <- smtp.SendMail(addr, auth, e.From, e.To, []byte(msg)) }()
	select {
	case err := <-done:
		return err
	case <-ctx.Done():
		return ctx.Err()
	}
}

// SendTelegram sends one message to the configured chat.
func (n *Notifier) SendTelegram(ctx context.Context, t Telegram, text string) error {
	if t.BotToken == "" || t.ChatID == "" {
		return errors.New("telegram alerts need a bot token and a chat id")
	}
	b, _ := json.Marshal(map[string]string{"chat_id": t.ChatID, "text": text})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, n.TelegramAPI+"/bot"+t.BotToken+"/sendMessage", bytes.NewReader(b))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	res, err := n.client.Do(req)
	if err != nil {
		return err
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		var tg struct {
			Description string `json:"description"`
		}
		json.NewDecoder(io.LimitReader(res.Body, 4096)).Decode(&tg)
		if tg.Description == "" {
			tg.Description = res.Status
		}
		return fmt.Errorf("telegram: %s", tg.Description)
	}
	return nil
}
