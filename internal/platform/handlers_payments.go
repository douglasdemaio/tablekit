package platform

import (
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"time"

	"github.com/google/uuid"

	"tablekit.de/tablekit/internal/customer"
	"tablekit.de/tablekit/internal/db"
	"tablekit.de/tablekit/internal/payments"
	"tablekit.de/tablekit/internal/storefront"
	"tablekit.de/tablekit/internal/tenant"
)

func (s *Server) createStripeCheckout(r *http.Request, m tenant.Merchant, cid customer.Identity, placed storefront.PlacedOrder) (string, error) {
	pc, err := s.storefrontSvc.PaymentConnection(r.Context(), m.ID)
	if err != nil {
		return "", err
	}
	if !s.cfg.StripeEnabled() || pc.Status != "active" || pc.ConnectedAccountID == "" || pc.LiveMode != s.cfg.StripeLiveMode {
		return "", storefront.ErrCardUnavailable
	}
	order, err := s.storefrontSvc.OrderByRef(r.Context(), m.ID, placed.PublicRef)
	if err != nil {
		return "", err
	}
	base := fmt.Sprintf("%s://%s", s.cfg.PublicScheme, r.Host)
	client := payments.NewConnectClient(s.cfg.StripeSecretKey, pc.ConnectedAccountID)
	session, err := client.CreateCheckoutSession(r.Context(), payments.CheckoutInput{
		OrderID:      placed.ID.String(),
		MerchantID:   m.ID.String(),
		PublicRef:    placed.PublicRef,
		CustomerMail: cid.Account.Email,
		AmountMinor:  placed.Total,
		Currency:     placed.Currency,
		Description:  fmt.Sprintf("%s – %s", m.Name, order.Order.PublicRef),
		SuccessURL:   base + "/orders/" + placed.PublicRef + "?paid=1",
		CancelURL:    base + "/checkout?canceled=1",
	})
	if err != nil {
		return "", err
	}
	if err := s.storefrontSvc.CreatePaymentRecord(r.Context(), m.ID, placed.ID, session.ID, placed.Total, placed.Currency); err != nil {
		return "", err
	}
	u, err := url.Parse(session.URL)
	if err != nil || u.Scheme != "https" || u.Host != "checkout.stripe.com" {
		return "", storefront.ErrCardUnavailable
	}
	return session.URL, nil
}

func (s *Server) handleStripeWebhook(w http.ResponseWriter, r *http.Request) {
	if !s.cfg.StripeEnabled() {
		http.Error(w, "not configured", http.StatusServiceUnavailable)
		return
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
	if err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	event, err := payments.VerifyWebhook(body, r.Header.Get("Stripe-Signature"), s.cfg.StripeWebhookSecret, time.Now())
	if err != nil {
		s.log.Warn("stripe webhook signature rejected", "error", err)
		http.Error(w, "invalid signature", http.StatusBadRequest)
		return
	}
	if event.Account == "" {
		s.log.Warn("stripe webhook missing connected account", "event", event.ID, "type", event.Type)
		w.WriteHeader(http.StatusOK)
		return
	}
	route, err := s.store.ResolveMerchantByPaymentAccount(r.Context(), event.Account)
	if err != nil {
		if errors.Is(err, db.ErrNotFound) {
			s.log.Warn("stripe webhook for unknown account", "account", event.Account, "event", event.ID)
			w.WriteHeader(http.StatusOK)
			return
		}
		s.log.Error("resolve stripe account", "error", err, "account", event.Account)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	merchantID := route.ID
	eventID, fresh, err := s.storefrontSvc.BeginWebhookEvent(r.Context(), merchantID, event.ID, event.Type, body)
	if err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	if !fresh {
		w.WriteHeader(http.StatusOK)
		return
	}
	processErr := s.processStripeEvent(r, merchantID, event)
	if processErr != nil {
		msg := processErr.Error()
		_ = s.storefrontSvc.FinishWebhookEvent(r.Context(), merchantID, eventID, "failed", &msg)
		s.log.Error("stripe webhook processing failed", "error", processErr, "event", event.ID, "type", event.Type)
		http.Error(w, "processing failed", http.StatusInternalServerError)
		return
	}
	_ = s.storefrontSvc.FinishWebhookEvent(r.Context(), merchantID, eventID, "processed", nil)
	w.WriteHeader(http.StatusOK)
}

func (s *Server) processStripeEvent(r *http.Request, merchantID uuid.UUID, event payments.Event) error {
	ref := event.Data.Object.ID
	if ref == "" {
		return errors.New("missing object id")
	}
	rec, err := s.storefrontSvc.PaymentRecordByRef(r.Context(), merchantID, ref)
	if err != nil || rec.OrderID == nil {
		if event.Type == "checkout.session.completed" || event.Type == "checkout.session.async_payment_succeeded" {
			return fmt.Errorf("payment record not found for %s", ref)
		}
		return nil
	}
	switch event.Type {
	case "checkout.session.completed":
		if event.Data.Object.PaymentStatus != "paid" {
			return nil
		}
		return s.storefrontSvc.SettlePayment(r.Context(), merchantID, rec.ID, *rec.OrderID)
	case "checkout.session.async_payment_succeeded":
		return s.storefrontSvc.SettlePayment(r.Context(), merchantID, rec.ID, *rec.OrderID)
	case "checkout.session.expired", "checkout.session.async_payment_failed":
		return s.storefrontSvc.FailPayment(r.Context(), merchantID, rec.ID, *rec.OrderID)
	default:
		return nil
	}
}
