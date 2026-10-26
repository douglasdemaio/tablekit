package payments

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strconv"
	"testing"
	"time"
)

func sign(payload []byte, secret string, ts int64) string {
	mac := hmac.New(sha256.New, []byte(secret))
	fmt.Fprintf(mac, "%d.%s", ts, payload)
	return fmt.Sprintf("t=%d,v1=%s", ts, hex.EncodeToString(mac.Sum(nil)))
}

func TestVerifyWebhook(t *testing.T) {
	now := time.Unix(1700000000, 0)
	payload := []byte(`{"id":"evt_1","type":"checkout.session.completed","data":{"object":{"id":"cs_1","payment_status":"paid"}}}`)
	header := sign(payload, "whsec_test", now.Unix())
	ev, err := VerifyWebhook(payload, header, "whsec_test", now)
	if err != nil {
		t.Fatalf("verify: %v", err)
	}
	if ev.ID != "evt_1" || ev.Data.Object.PaymentStatus != "paid" {
		t.Fatalf("unexpected event: %+v", ev)
	}
	if _, err := VerifyWebhook(payload, header, "wrong", now); err == nil {
		t.Fatal("wrong secret must fail")
	}
	old := sign(payload, "whsec_test", now.Add(-10*time.Minute).Unix())
	if _, err := VerifyWebhook(payload, old, "whsec_test", now); err == nil {
		t.Fatal("stale signature must fail")
	}
	if _, err := VerifyWebhook(payload, "t="+strconv.FormatInt(now.Unix(), 10), "whsec_test", now); err == nil {
		t.Fatal("missing v1 must fail")
	}
}

func TestVerifyConnectWebhookAccount(t *testing.T) {
	now := time.Unix(1700000000, 0)
	payload := []byte(`{"id":"evt_1","type":"checkout.session.completed","account":"acct_1","data":{"object":{"id":"cs_1","payment_status":"paid"}}}`)
	ev, err := VerifyWebhook(payload, sign(payload, "whsec_test", now.Unix()), "whsec_test", now)
	if err != nil {
		t.Fatalf("verify: %v", err)
	}
	if ev.Account != "acct_1" {
		t.Fatalf("account = %q", ev.Account)
	}
}
