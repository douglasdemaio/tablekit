package payments

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
)

// ConnectAccountInput is the minimum needed to create a merchant account.
// Stripe collects the remaining business, owner and bank details during
// hosted onboarding.
type ConnectAccountInput struct {
	ContactEmail string
	DisplayName  string
	Country      string
}

// ConnectAccount is the subset of a Stripe v2 account needed by tablekit.
type ConnectAccount struct {
	ID                 string
	CardPaymentsStatus string
	RequirementsDue    int
	LiveMode           bool
	Closed             bool
}

// Status maps Stripe's capability state onto tablekit's connection states.
func (a ConnectAccount) Status() string {
	if a.Closed {
		return "disabled"
	}
	if a.CardPaymentsStatus == "active" {
		return "active"
	}
	return "pending"
}

type connectCapability struct {
	Status string `json:"status"`
}

type connectAccountResponse struct {
	ID            string `json:"id"`
	Livemode      bool   `json:"livemode"`
	Closed        bool   `json:"closed"`
	Configuration struct {
		Merchant struct {
			Capabilities struct {
				CardPayments connectCapability `json:"card_payments"`
			} `json:"capabilities"`
		} `json:"merchant"`
	} `json:"configuration"`
	Requirements struct {
		Entries []json.RawMessage `json:"entries"`
	} `json:"requirements"`
}

func (a connectAccountResponse) account() ConnectAccount {
	return ConnectAccount{
		ID:                 a.ID,
		CardPaymentsStatus: a.Configuration.Merchant.Capabilities.CardPayments.Status,
		RequirementsDue:    len(a.Requirements.Entries),
		LiveMode:           a.Livemode,
		Closed:             a.Closed,
	}
}

// CreateConnectAccount creates a full-dashboard connected account for direct
// charges. Card payments remain restricted until hosted onboarding is complete.
func (c *Client) CreateConnectAccount(ctx context.Context, in ConnectAccountInput) (ConnectAccount, error) {
	country := strings.ToLower(strings.TrimSpace(in.Country))
	if len(country) != 2 {
		return ConnectAccount{}, fmt.Errorf("%w: identity country is required", ErrProvider)
	}
	reqBody := struct {
		ContactEmail string `json:"contact_email,omitempty"`
		DisplayName  string `json:"display_name,omitempty"`
		Dashboard    string `json:"dashboard"`
		Identity     struct {
			Country string `json:"country"`
		} `json:"identity"`
		Configuration struct {
			Merchant struct {
				Capabilities struct {
					CardPayments struct {
						Requested bool `json:"requested"`
					} `json:"card_payments"`
				} `json:"capabilities"`
			} `json:"merchant"`
		} `json:"configuration"`
		Defaults struct {
			Responsibilities struct {
				FeesCollector   string `json:"fees_collector"`
				LossesCollector string `json:"losses_collector"`
			} `json:"responsibilities"`
		} `json:"defaults"`
		Include []string `json:"include"`
	}{}
	reqBody.ContactEmail = strings.TrimSpace(in.ContactEmail)
	reqBody.DisplayName = strings.TrimSpace(in.DisplayName)
	reqBody.Dashboard = "full"
	reqBody.Identity.Country = country
	reqBody.Configuration.Merchant.Capabilities.CardPayments.Requested = true
	reqBody.Defaults.Responsibilities.FeesCollector = "stripe"
	reqBody.Defaults.Responsibilities.LossesCollector = "stripe"
	reqBody.Include = []string{"configuration.merchant", "requirements"}

	var out connectAccountResponse
	if err := c.doJSON(ctx, http.MethodPost, "/v2/core/accounts", reqBody, &out); err != nil {
		return ConnectAccount{}, err
	}
	if out.ID == "" {
		return ConnectAccount{}, fmt.Errorf("%w: missing connected account id", ErrProvider)
	}
	return out.account(), nil
}

// GetConnectAccount refreshes onboarding and capability state.
func (c *Client) GetConnectAccount(ctx context.Context, accountID string) (ConnectAccount, error) {
	q := url.Values{}
	q.Add("include", "configuration.merchant")
	q.Add("include", "requirements")
	var out connectAccountResponse
	path := "/v2/core/accounts/" + url.PathEscape(accountID) + "?" + q.Encode()
	if err := c.doJSON(ctx, http.MethodGet, path, nil, &out); err != nil {
		return ConnectAccount{}, err
	}
	if out.ID == "" {
		return ConnectAccount{}, fmt.Errorf("%w: missing connected account id", ErrProvider)
	}
	return out.account(), nil
}

// CreateAccountLink returns a hosted Stripe onboarding URL for the account.
func (c *Client) CreateAccountLink(ctx context.Context, accountID, returnURL, refreshURL string) (string, error) {
	reqBody := struct {
		Account string `json:"account"`
		UseCase struct {
			Type              string `json:"type"`
			AccountOnboarding struct {
				ReturnURL  string `json:"return_url"`
				RefreshURL string `json:"refresh_url"`
			} `json:"account_onboarding"`
		} `json:"use_case"`
	}{}
	reqBody.Account = accountID
	reqBody.UseCase.Type = "account_onboarding"
	reqBody.UseCase.AccountOnboarding.ReturnURL = returnURL
	reqBody.UseCase.AccountOnboarding.RefreshURL = refreshURL

	var out struct {
		URL string `json:"url"`
	}
	if err := c.doJSON(ctx, http.MethodPost, "/v2/core/account_links", reqBody, &out); err != nil {
		return "", err
	}
	u, err := url.Parse(out.URL)
	if err != nil || u.Scheme != "https" || (u.Host != "stripe.com" && !strings.HasSuffix(u.Host, ".stripe.com")) {
		return "", fmt.Errorf("%w: invalid onboarding url", ErrProvider)
	}
	return out.URL, nil
}

func (c *Client) doJSON(ctx context.Context, method, path string, in, out any) error {
	var body io.Reader
	if in != nil {
		raw, err := json.Marshal(in)
		if err != nil {
			return fmt.Errorf("%w: %v", ErrProvider, err)
		}
		body = bytes.NewReader(raw)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.BaseURL+path, body)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+c.SecretKey)
	req.Header.Set("Stripe-Version", apiVersion)
	if in != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if c.Account != "" {
		req.Header.Set("Stripe-Account", c.Account)
	}
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return fmt.Errorf("%w: %v", ErrProvider, err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode/100 != 2 {
		return fmt.Errorf("%w: status %d: %s", ErrProvider, resp.StatusCode, strings.TrimSpace(string(raw)))
	}
	if out == nil {
		return nil
	}
	if err := json.Unmarshal(raw, out); err != nil {
		return fmt.Errorf("%w: %v", ErrProvider, err)
	}
	return nil
}
