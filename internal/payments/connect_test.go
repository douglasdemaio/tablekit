package payments

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

func TestConnectClientCreateCheckoutSession(t *testing.T) {
	var gotAccount, gotIdempotency, gotVersion string
	var gotForm url.Values
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/checkout/sessions" {
			t.Errorf("path = %q", r.URL.Path)
		}
		if err := r.ParseForm(); err != nil {
			t.Errorf("parse form: %v", err)
		}
		gotForm = r.PostForm
		gotAccount = r.Header.Get("Stripe-Account")
		gotIdempotency = r.Header.Get("Idempotency-Key")
		gotVersion = r.Header.Get("Stripe-Version")
		_, _ = io.WriteString(w, `{"id":"cs_1","url":"https://checkout.stripe.com/c/pay/cs_1","payment_status":"unpaid"}`)
	}))
	defer srv.Close()

	c := NewConnectClient("sk_test", "acct_123")
	c.BaseURL = srv.URL
	_, err := c.CreateCheckoutSession(context.Background(), CheckoutInput{
		OrderID: "order-1", MerchantID: "merchant-1", AmountMinor: 1390, Currency: "EUR",
		Description: "Schnitzel", SuccessURL: "https://example.test/ok", CancelURL: "https://example.test/cancel",
	})
	if err != nil {
		t.Fatalf("create checkout: %v", err)
	}
	if gotAccount != "acct_123" {
		t.Fatalf("Stripe-Account = %q", gotAccount)
	}
	if gotIdempotency != "checkout_order-1" {
		t.Fatalf("Idempotency-Key = %q", gotIdempotency)
	}
	if gotVersion != apiVersion {
		t.Fatalf("Stripe-Version = %q", gotVersion)
	}
	if gotForm.Get("payment_method_types[0]") != "" {
		t.Fatal("payment_method_types must be omitted for dynamic payment methods")
	}
	if gotForm.Get("line_items[0][price_data][unit_amount]") != "1390" {
		t.Fatalf("amount = %q", gotForm.Get("line_items[0][price_data][unit_amount]"))
	}
}

func TestConnectAccountLifecycle(t *testing.T) {
	var createdBody map[string]any
	var linkedBody map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v2/core/accounts":
			if err := json.NewDecoder(r.Body).Decode(&createdBody); err != nil {
				t.Errorf("decode account: %v", err)
			}
			_, _ = io.WriteString(w, `{"id":"acct_1","livemode":false,"closed":false,"configuration":{"merchant":{"capabilities":{"card_payments":{"status":"restricted"}}}},"requirements":{"entries":[{},{}]}}`)
		case "/v2/core/accounts/acct_1":
			includes := r.URL.Query()["include"]
			if len(includes) != 2 || includes[0] != "configuration.merchant" || includes[1] != "requirements" {
				t.Errorf("include = %v", includes)
			}
			_, _ = io.WriteString(w, `{"id":"acct_1","livemode":true,"closed":false,"configuration":{"merchant":{"capabilities":{"card_payments":{"status":"active"}}}},"requirements":{"entries":[]}}`)
		case "/v2/core/account_links":
			if err := json.NewDecoder(r.Body).Decode(&linkedBody); err != nil {
				t.Errorf("decode link: %v", err)
			}
			_, _ = io.WriteString(w, `{"url":"https://connect.stripe.com/setup/s/acct_1/link"}`)
		default:
			t.Errorf("unexpected path %q", r.URL.Path)
		}
	}))
	defer srv.Close()

	c := NewClient("sk_test")
	c.BaseURL = srv.URL
	acct, err := c.CreateConnectAccount(context.Background(), ConnectAccountInput{
		ContactEmail: "owner@example.com", DisplayName: "Café", Country: "DE",
	})
	if err != nil {
		t.Fatalf("create account: %v", err)
	}
	if acct.ID != "acct_1" || acct.Status() != "pending" || acct.RequirementsDue != 2 {
		t.Fatalf("created account = %+v", acct)
	}
	identity, _ := createdBody["identity"].(map[string]any)
	if identity["country"] != "de" {
		t.Fatalf("identity.country = %v", identity["country"])
	}
	if createdBody["dashboard"] != "full" {
		t.Fatalf("dashboard = %v", createdBody["dashboard"])
	}
	cfg, _ := createdBody["configuration"].(map[string]any)
	merchant, _ := cfg["merchant"].(map[string]any)
	caps, _ := merchant["capabilities"].(map[string]any)
	card, _ := caps["card_payments"].(map[string]any)
	if card["requested"] != true {
		t.Fatalf("card_payments.requested = %v", card["requested"])
	}

	link, err := c.CreateAccountLink(context.Background(), "acct_1", "https://example.test/return", "https://example.test/refresh")
	if err != nil {
		t.Fatalf("create link: %v", err)
	}
	if !strings.HasPrefix(link, "https://connect.stripe.com/") {
		t.Fatalf("link = %q", link)
	}
	useCase, _ := linkedBody["use_case"].(map[string]any)
	if useCase["type"] != "account_onboarding" {
		t.Fatalf("use_case = %v", useCase)
	}

	refreshed, err := c.GetConnectAccount(context.Background(), "acct_1")
	if err != nil {
		t.Fatalf("get account: %v", err)
	}
	if refreshed.Status() != "active" || !refreshed.LiveMode {
		t.Fatalf("refreshed account = %+v", refreshed)
	}
}
