// Package payments talks to Stripe over its REST API. Restaurant payments are
// direct charges on connected accounts; the platform never holds, routes or
// splits customer money.
package payments

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

const (
	apiBase    = "https://api.stripe.com"
	apiVersion = "2026-09-30.endive"
)

var (
	ErrSignature = errors.New("payments: invalid webhook signature")
	ErrProvider  = errors.New("payments: provider error")
	ErrNotPaid   = errors.New("payments: session not paid")
)

// Client calls Stripe with a platform secret key. Account is set for Connect
// requests, which makes the call act on a restaurant's connected account.
type Client struct {
	SecretKey string
	Account   string
	HTTP      *http.Client
	BaseURL   string
}

func NewClient(secretKey string) *Client {
	return &Client{SecretKey: secretKey, HTTP: &http.Client{Timeout: 20 * time.Second}, BaseURL: apiBase}
}

// NewConnectClient returns a client that sends Stripe-Account for direct charges.
func NewConnectClient(secretKey, accountID string) *Client {
	c := NewClient(secretKey)
	c.Account = accountID
	return c
}

// CheckoutInput describes a direct charge for one order.
type CheckoutInput struct {
	OrderID      string
	MerchantID   string
	PublicRef    string
	CustomerMail string
	AmountMinor  int32
	Currency     string
	Description  string
	SuccessURL   string
	CancelURL    string
}

// CheckoutSession is the subset of the Stripe object we need.
type CheckoutSession struct {
	ID            string
	URL           string
	PaymentStatus string
}

func (c *Client) CreateCheckoutSession(ctx context.Context, in CheckoutInput) (CheckoutSession, error) {
	form := url.Values{}
	form.Set("mode", "payment")
	form.Set("success_url", in.SuccessURL)
	form.Set("cancel_url", in.CancelURL)
	form.Set("client_reference_id", in.OrderID)
	form.Set("metadata[order_id]", in.OrderID)
	form.Set("metadata[merchant_id]", in.MerchantID)
	form.Set("payment_intent_data[metadata][order_id]", in.OrderID)
	form.Set("payment_intent_data[metadata][merchant_id]", in.MerchantID)
	if in.CustomerMail != "" {
		form.Set("customer_email", in.CustomerMail)
	}
	form.Set("line_items[0][quantity]", "1")
	form.Set("line_items[0][price_data][currency]", strings.ToLower(in.Currency))
	form.Set("line_items[0][price_data][unit_amount]", strconv.Itoa(int(in.AmountMinor)))
	form.Set("line_items[0][price_data][product_data][name]", in.Description)

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.BaseURL+"/v1/checkout/sessions", strings.NewReader(form.Encode()))
	if err != nil {
		return CheckoutSession{}, err
	}
	req.Header.Set("Authorization", "Bearer "+c.SecretKey)
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Stripe-Version", apiVersion)
	if c.Account != "" {
		req.Header.Set("Stripe-Account", c.Account)
	}
	if in.OrderID != "" {
		req.Header.Set("Idempotency-Key", "checkout_"+in.OrderID)
	}
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return CheckoutSession{}, fmt.Errorf("%w: %v", ErrProvider, err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode/100 != 2 {
		return CheckoutSession{}, fmt.Errorf("%w: status %d: %s", ErrProvider, resp.StatusCode, strings.TrimSpace(string(body)))
	}
	var out struct {
		ID            string `json:"id"`
		URL           string `json:"url"`
		PaymentStatus string `json:"payment_status"`
	}
	if err := json.Unmarshal(body, &out); err != nil {
		return CheckoutSession{}, fmt.Errorf("%w: %v", ErrProvider, err)
	}
	if out.ID == "" || out.URL == "" {
		return CheckoutSession{}, ErrProvider
	}
	return CheckoutSession{ID: out.ID, URL: out.URL, PaymentStatus: out.PaymentStatus}, nil
}

// Event is the webhook payload subset used for settlement.
type Event struct {
	ID      string `json:"id"`
	Type    string `json:"type"`
	Account string `json:"account"`
	Data    struct {
		Object struct {
			ID                string            `json:"id"`
			PaymentStatus     string            `json:"payment_status"`
			PaymentIntent     string            `json:"payment_intent"`
			ClientReferenceID string            `json:"client_reference_id"`
			Metadata          map[string]string `json:"metadata"`
		} `json:"object"`
	} `json:"data"`
}

// VerifyWebhook validates the Stripe-Signature header and parses the event.
func VerifyWebhook(payload []byte, sigHeader, secret string, now time.Time) (Event, error) {
	var timestamp string
	var signatures []string
	for _, part := range strings.Split(sigHeader, ",") {
		k, v, ok := strings.Cut(strings.TrimSpace(part), "=")
		if !ok {
			continue
		}
		switch k {
		case "t":
			timestamp = v
		case "v1":
			signatures = append(signatures, v)
		}
	}
	if timestamp == "" || len(signatures) == 0 || secret == "" {
		return Event{}, ErrSignature
	}
	ts, err := strconv.ParseInt(timestamp, 10, 64)
	if err != nil || now.Sub(time.Unix(ts, 0)) > 5*time.Minute || time.Unix(ts, 0).Sub(now) > 5*time.Minute {
		return Event{}, ErrSignature
	}
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(timestamp))
	mac.Write([]byte("."))
	mac.Write(payload)
	want := mac.Sum(nil)
	for _, sig := range signatures {
		got, err := hex.DecodeString(sig)
		if err == nil && hmac.Equal(got, want) {
			var ev Event
			if err := json.Unmarshal(payload, &ev); err != nil {
				return Event{}, ErrSignature
			}
			return ev, nil
		}
	}
	return Event{}, ErrSignature
}
