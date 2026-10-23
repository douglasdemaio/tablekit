// Package storefront implements the guest-facing restaurant page and checkout.
// It only ever reads the merchant's own menu and writes orders for that
// merchant; tenants are always supplied by the host-based tenant resolver.
package storefront

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"tablekit.de/tablekit/internal/db"
	"tablekit.de/tablekit/internal/db/sqlcgen"
)

var (
	ErrNotFound            = errors.New("storefront: not found")
	ErrClosed              = errors.New("storefront: restaurant is closed")
	ErrEmptyCart           = errors.New("storefront: cart is empty")
	ErrItemUnavailable     = errors.New("storefront: item unavailable")
	ErrDeliveryUnavailable = errors.New("storefront: delivery not available")
	ErrMinOrder            = errors.New("storefront: below minimum order")
	ErrInvalidInput        = errors.New("storefront: invalid input")
	ErrInvalidCode         = errors.New("storefront: invalid pickup code")
	ErrCodeLocked          = errors.New("storefront: pickup code locked")
	ErrNotReady            = errors.New("storefront: order is not ready")
	ErrInvalidSlot         = errors.New("storefront: invalid time slot")
	ErrCardUnavailable     = errors.New("storefront: card payment not configured")
)

const (
	// MaxItemsPerOrder bounds a cart to protect the database and kitchen.
	MaxItemsPerOrder = 50
	// MaxQuantityPerItem bounds a single line.
	MaxQuantityPerItem = 20
	// PickupCodeTTL is how long a handoff code stays valid.
	PickupCodeTTL = 24 * time.Hour
	// MaxHandoffAttempts locks a code after repeated wrong guesses.
	MaxHandoffAttempts = 5
)

// Service serves the public storefront for a single merchant.
type Service struct {
	store *db.Store
}

func NewService(store *db.Store) *Service { return &Service{store: store} }

// Settings is the public merchant configuration.
type Settings struct {
	ID                  uuid.UUID
	Name                string
	Slug                string
	Currency            string
	Timezone            *time.Location
	VatRateBp           int32
	Description         string
	Phone               string
	Email               string
	Street              string
	Line2               string
	Postcode            string
	City                string
	Country             string
	Cuisine             string
	PickupEnabled       bool
	SelfDeliveryEnabled bool
	Tagline             string
	PrimaryColor        string
	SecondaryColor      string
	AccentColor         string
	BackgroundColor     string
	TextColor           string
	Template            string
	Font                string
	LogoKey             string
	CoverKey            string
	CardEnabled         bool
}

// Hour is one opening window for a weekday (0 = Sunday).
type Hour struct {
	Weekday int
	Opens   string
	Closes  string
}

// Zone is a delivery zone with its fee rules.
type Zone struct {
	ID             uuid.UUID
	Name           string
	Prefixes       []string
	MinOrderMinor  int32
	DeliveryFeeMin int32
	Active         bool
}

// Item is a menu line on the public page.
type Item struct {
	ID           uuid.UUID
	CategoryID   uuid.UUID
	Name         string
	Description  string
	PriceMinor   int32
	Available    bool
	SoldOutUntil time.Time
	ImageKey     string
}

// Category groups items for display.
type Category struct {
	ID          uuid.UUID
	Name        string
	Description string
	Items       []Item
}

// Storefront is everything the public page needs.
type Storefront struct {
	Settings Settings
	Hours    []Hour
	Zones    []Zone
	Menu     []Category
	Open     bool
}

// Load returns the public page data for a merchant.
func (s *Service) Load(ctx context.Context, merchantID uuid.UUID) (Storefront, error) {
	var out Storefront
	err := s.store.WithTenant(ctx, merchantID, func(q *sqlcgen.Queries) error {
		settings, err := loadSettings(ctx, q, merchantID)
		if err != nil {
			return err
		}
		hours, err := loadHours(ctx, q)
		if err != nil {
			return err
		}
		zones, err := loadZones(ctx, q, false)
		if err != nil {
			return err
		}
		cats, err := q.ListStorefrontCategories(ctx)
		if err != nil {
			return err
		}
		items, err := q.ListStorefrontItems(ctx)
		if err != nil {
			return err
		}
		byCategory := make(map[uuid.UUID][]Item)
		for _, it := range items {
			byCategory[it.CategoryID] = append(byCategory[it.CategoryID], Item{
				ID:          it.ID,
				CategoryID:  it.CategoryID,
				Name:        it.Name,
				Description: it.Description,
				PriceMinor:  it.PriceMinor,
				Available:   it.Available != nil && *it.Available,
				ImageKey:    it.ImageKey,
			})
		}
		menu := make([]Category, 0, len(cats))
		for _, c := range cats {
			menu = append(menu, Category{
				ID:          c.ID,
				Name:        c.Name,
				Description: c.Description,
				Items:       byCategory[c.ID],
			})
		}
		out = Storefront{
			Settings: settings,
			Hours:    hours,
			Zones:    zones,
			Menu:     menu,
			Open:     IsOpen(hours, time.Now().In(settings.Timezone)),
		}
		return nil
	})
	return out, err
}

// CartLine is a request to buy a quantity of one menu item.
type CartLine struct {
	ItemID   uuid.UUID
	Quantity int
}

// DeliveryAddress is the address snapshot stored on an order.
type DeliveryAddress struct {
	Recipient string `json:"recipient"`
	Phone     string `json:"phone"`
	Line1     string `json:"line1"`
	Line2     string `json:"line2"`
	Postcode  string `json:"postcode"`
	City      string `json:"city"`
	Country   string `json:"country"`
	Notes     string `json:"notes"`
}

// OrderInput is a validated checkout request from a signed-in customer.
type OrderInput struct {
	CustomerID   uuid.UUID
	Name         string
	Email        string
	Phone        string
	Fulfilment   sqlcgen.FulfilmentModeEnum
	Payment      string
	Address      DeliveryAddress
	Notes        string
	ScheduledFor time.Time
	ShareProfile bool
	Lines        []CartLine
	Now          time.Time
}

// PlacedOrder is returned once, together with the plaintext pickup code.
type PlacedOrder struct {
	ID           uuid.UUID
	PublicRef    string
	PickupCode   string
	Total        int32
	Currency     string
	Pickup       bool
	Card         bool
	ScheduledFor time.Time
}

// PlaceOrder recomputes every price server-side and creates the order in the
// state appropriate for its payment method.
func (s *Service) PlaceOrder(ctx context.Context, merchantID uuid.UUID, in OrderInput) (PlacedOrder, error) {
	in.Name = strings.TrimSpace(in.Name)
	in.Email = strings.TrimSpace(in.Email)
	in.Phone = strings.TrimSpace(in.Phone)
	in.Notes = strings.TrimSpace(in.Notes)
	in.Address = DeliveryAddress{
		Recipient: strings.TrimSpace(in.Address.Recipient),
		Phone:     strings.TrimSpace(in.Address.Phone),
		Line1:     strings.TrimSpace(in.Address.Line1),
		Line2:     strings.TrimSpace(in.Address.Line2),
		Postcode:  strings.ToUpper(strings.TrimSpace(in.Address.Postcode)),
		City:      strings.TrimSpace(in.Address.City),
		Country:   strings.ToUpper(strings.TrimSpace(in.Address.Country)),
		Notes:     strings.TrimSpace(in.Address.Notes),
	}
	if in.CustomerID == uuid.Nil {
		return PlacedOrder{}, ErrInvalidInput
	}
	if in.Payment != "card" {
		in.Payment = "cash"
	}
	if in.Name == "" || len(in.Name) > 120 || len(in.Email) > 254 ||
		len(in.Phone) > 40 || len(in.Notes) > 500 {
		return PlacedOrder{}, ErrInvalidInput
	}
	if len(in.Lines) == 0 || len(in.Lines) > MaxItemsPerOrder {
		return PlacedOrder{}, ErrEmptyCart
	}
	switch in.Fulfilment {
	case sqlcgen.FulfilmentModePickup, sqlcgen.FulfilmentModeSelfDelivery:
	default:
		return PlacedOrder{}, ErrInvalidInput
	}
	if in.Fulfilment == sqlcgen.FulfilmentModeSelfDelivery {
		a := in.Address
		if a.Recipient == "" || a.Phone == "" || a.Line1 == "" || a.Postcode == "" || a.City == "" {
			return PlacedOrder{}, ErrInvalidInput
		}
	}

	var result PlacedOrder
	err := s.store.WithTenantTx(ctx, merchantID, func(_ pgx.Tx, q *sqlcgen.Queries) error {
		settings, err := loadSettings(ctx, q, merchantID)
		if err != nil {
			return err
		}
		hours, err := loadHours(ctx, q)
		if err != nil {
			return err
		}
		now := in.Now
		if now.IsZero() {
			now = time.Now()
		}
		now = now.In(settings.Timezone)
		if !IsOpen(hours, now) {
			return ErrClosed
		}
		if in.Fulfilment == sqlcgen.FulfilmentModePickup && !settings.PickupEnabled {
			return ErrDeliveryUnavailable
		}
		if in.Fulfilment == sqlcgen.FulfilmentModeSelfDelivery && !settings.SelfDeliveryEnabled {
			return ErrDeliveryUnavailable
		}
		scheduled := in.ScheduledFor
		if !scheduled.IsZero() {
			scheduled = scheduled.In(settings.Timezone)
			if !ValidSlot(hours, now, scheduled) {
				return ErrInvalidSlot
			}
		} else {
			scheduled = now
		}
		if in.Payment == "card" && !settings.CardEnabled {
			return ErrCardUnavailable
		}
		items, err := q.ListStorefrontItems(ctx)
		if err != nil {
			return err
		}
		byID := make(map[uuid.UUID]Item, len(items))
		for _, it := range items {
			byID[it.ID] = Item{ID: it.ID, Name: it.Name, PriceMinor: it.PriceMinor, Available: it.Available != nil && *it.Available}
		}

		var subtotal int32
		merged := mergeLines(in.Lines)
		type line struct {
			item Item
			qty  int
		}
		lines := make([]line, 0, len(merged))
		for _, req := range merged {
			it, ok := byID[req.ItemID]
			if !ok || !it.Available {
				return ErrItemUnavailable
			}
			if req.Quantity < 1 || req.Quantity > MaxQuantityPerItem {
				return ErrInvalidInput
			}
			subtotal += it.PriceMinor * int32(req.Quantity)
			lines = append(lines, line{item: it, qty: req.Quantity})
		}
		if subtotal <= 0 {
			return ErrEmptyCart
		}

		var fee int32
		if in.Fulfilment == sqlcgen.FulfilmentModeSelfDelivery {
			zones, err := loadZones(ctx, q, true)
			if err != nil {
				return err
			}
			zone, ok := MatchZone(zones, in.Address.Postcode)
			if !ok {
				return ErrDeliveryUnavailable
			}
			if subtotal < zone.MinOrderMinor {
				return ErrMinOrder
			}
			fee = zone.DeliveryFeeMin
		}

		total := subtotal + fee
		tax := IncludedTax(total, settings.VatRateBp)

		ref, err := randomRef()
		if err != nil {
			return err
		}
		code, err := randomCode()
		if err != nil {
			return err
		}
		var address []byte
		if in.Fulfilment == sqlcgen.FulfilmentModeSelfDelivery {
			in.Address.Country = defaultCountry(in.Address.Country)
			address, _ = json.Marshal(in.Address)
		}
		status := sqlcgen.OrderStatusSubmitted
		paymentStatus := sqlcgen.PaymentStatusUnpaid
		if in.Payment == "card" {
			status = sqlcgen.OrderStatusPendingPayment
			paymentStatus = sqlcgen.PaymentStatusPending
		}
		email := in.Email
		phone := in.Phone
		row, err := q.CreateStorefrontOrder(ctx, sqlcgen.CreateStorefrontOrderParams{
			Column1:          merchantID,
			Column2:          ref,
			Column3:          status,
			Column4:          in.Fulfilment,
			Column5:          in.Payment,
			Column6:          paymentStatus,
			Column7:          settings.Currency,
			SubtotalMinor:    subtotal,
			TaxMinor:         tax,
			DeliveryFeeMinor: fee,
			TotalMinor:       total,
			ContactName:      in.Name,
			ContactEmail:     &email,
			ContactPhone:     &phone,
			DeliveryAddress:  address,
			Notes:            in.Notes,
			Column17:         pgtype.Timestamptz{Time: scheduled, Valid: true},
			Column18:         in.CustomerID,
		})
		if err != nil {
			return err
		}
		for _, l := range lines {
			if l.qty < 1 || l.qty > MaxQuantityPerItem {
				return ErrInvalidInput
			}
			if err := q.InsertGuestOrderItem(ctx, sqlcgen.InsertGuestOrderItemParams{
				Column1: merchantID,
				Column2: row.ID,
				Column3: l.item.ID,
				Column4: l.item.Name,
				Column5: l.item.PriceMinor,
				Column6: int32(l.qty),                     // #nosec G115 -- bounded by MaxQuantityPerItem
				Column7: l.item.PriceMinor * int32(l.qty), // #nosec G115 -- bounded by MaxQuantityPerItem
			}); err != nil {
				return err
			}
		}
		if err := q.InsertStorefrontOrderEvent(ctx, sqlcgen.InsertStorefrontOrderEventParams{
			Column1: merchantID,
			Column2: row.ID,
			Column3: status,
			Column4: "customer",
		}); err != nil {
			return err
		}
		if in.Fulfilment == sqlcgen.FulfilmentModeSelfDelivery {
			if err := q.InsertDelivery(ctx, sqlcgen.InsertDeliveryParams{
				Column1: merchantID, Column2: row.ID,
			}); err != nil {
				return err
			}
		}
		kind := sqlcgen.HandoffKindPickup
		if in.Fulfilment == sqlcgen.FulfilmentModeSelfDelivery {
			kind = sqlcgen.HandoffKindDelivery
		}
		salt := make([]byte, 16)
		if _, err := rand.Read(salt); err != nil {
			return err
		}
		if err := q.InsertOrderHandoffCode(ctx, sqlcgen.InsertOrderHandoffCodeParams{
			Column1: merchantID,
			Column2: row.ID,
			Column3: kind,
			Column4: hashCode(salt, code),
			Column5: salt,
			Column6: pgtype.Timestamptz{Time: scheduled.Add(PickupCodeTTL + time.Until(now)), Valid: true},
		}); err != nil {
			return err
		}
		if in.ShareProfile {
			if err := q.UpsertCustomerShare(ctx, sqlcgen.UpsertCustomerShareParams{
				Column1: in.CustomerID, Column2: merchantID,
			}); err != nil {
				return err
			}
		} else if err := q.DeleteCustomerShare(ctx, sqlcgen.DeleteCustomerShareParams{
			Column1: in.CustomerID, Column2: merchantID,
		}); err != nil {
			return err
		}
		result = PlacedOrder{
			ID:           row.ID,
			PublicRef:    row.PublicRef,
			PickupCode:   code,
			Total:        total,
			Currency:     settings.Currency,
			Pickup:       in.Fulfilment == sqlcgen.FulfilmentModePickup,
			Card:         in.Payment == "card",
			ScheduledFor: scheduled,
		}
		return nil
	})
	return result, err
}

// PublicOrder is a guest-visible order.
type PublicOrder struct {
	Order sqlcgen.Order
	Items []sqlcgen.OrderItem
}

// OrderByID loads one order for a staff-initiated handoff.
func (s *Service) OrderByID(ctx context.Context, merchantID, orderID uuid.UUID) (sqlcgen.Order, error) {
	var out sqlcgen.Order
	err := s.store.WithTenant(ctx, merchantID, func(q *sqlcgen.Queries) error {
		order, err := q.GetOrderByID(ctx, orderID)
		out = order
		return err
	})
	if err != nil {
		return sqlcgen.Order{}, ErrNotFound
	}
	return out, nil
}

// OrderByRef loads one order by its unguessable public reference.
func (s *Service) OrderByRef(ctx context.Context, merchantID uuid.UUID, ref string) (PublicOrder, error) {
	var out PublicOrder
	err := s.store.WithTenant(ctx, merchantID, func(q *sqlcgen.Queries) error {
		order, err := q.GetOrderByPublicRef(ctx, ref)
		if err != nil {
			return ErrNotFound
		}
		items, err := q.ListOrderItems(ctx, order.ID)
		if err != nil {
			return err
		}
		out = PublicOrder{Order: order, Items: items}
		return nil
	})
	return out, err
}

// DeliveryJob is a driver-facing delivery row.
type DeliveryJob struct {
	OrderID      uuid.UUID
	Ref          string
	Status       sqlcgen.OrderStatusEnum
	Payment      sqlcgen.PaymentStatusEnum
	PaymentMode  string
	Currency     string
	Total        int32
	ContactName  string
	Phone        string
	Address      DeliveryAddress
	Notes        string
	ScheduledFor time.Time
	PlacedAt     time.Time
	AssigneeID   *uuid.UUID
	AssigneeName string
}

func (s *Service) DeliveryJobs(ctx context.Context, merchantID uuid.UUID) ([]DeliveryJob, error) {
	var out []DeliveryJob
	err := s.store.WithTenant(ctx, merchantID, func(q *sqlcgen.Queries) error {
		rows, err := q.ListDeliveryOrders(ctx, merchantID)
		if err != nil {
			return err
		}
		for _, row := range rows {
			job := DeliveryJob{
				OrderID: row.ID, Ref: row.PublicRef, Status: row.Status,
				Payment: row.PaymentStatusEnum, PaymentMode: row.PaymentMethod,
				Currency: row.Currency, Total: row.TotalMinor, ContactName: row.ContactName,
				Notes: row.Notes, AssigneeID: row.AssigneeStaffID, AssigneeName: row.AssigneeName,
			}
			if row.ContactPhone != nil {
				job.Phone = *row.ContactPhone
			}
			if len(row.DeliveryAddress) > 0 {
				_ = json.Unmarshal(row.DeliveryAddress, &job.Address)
			}
			if row.ScheduledFor.Valid {
				job.ScheduledFor = row.ScheduledFor.Time
			}
			if row.PlacedAt.Valid {
				job.PlacedAt = row.PlacedAt.Time
			}
			out = append(out, job)
		}
		return nil
	})
	return out, err
}

func (s *Service) ClaimDelivery(ctx context.Context, merchantID, staffID, orderID uuid.UUID) error {
	return s.store.WithTenant(ctx, merchantID, func(q *sqlcgen.Queries) error {
		_, err := q.ClaimDelivery(ctx, sqlcgen.ClaimDeliveryParams{
			AssigneeStaffID: &staffID, Column2: merchantID, Column3: orderID,
		})
		if err != nil {
			return ErrNotFound
		}
		return nil
	})
}

func (s *Service) AssignDelivery(ctx context.Context, merchantID, staffID, orderID uuid.UUID) error {
	return s.store.WithTenant(ctx, merchantID, func(q *sqlcgen.Queries) error {
		staff, err := q.GetStaffByID(ctx, staffID)
		if err != nil || staff.Role != sqlcgen.StaffRoleDriver || staff.DisabledAt.Valid {
			return ErrInvalidInput
		}
		return q.AssignDelivery(ctx, sqlcgen.AssignDeliveryParams{
			StaffID: &staffID, MerchantID: merchantID, OrderID: orderID,
		})
	})
}

func (s *Service) ReleaseDelivery(ctx context.Context, merchantID, orderID uuid.UUID) error {
	return s.store.WithTenant(ctx, merchantID, func(q *sqlcgen.Queries) error {
		return q.ReleaseDelivery(ctx, sqlcgen.ReleaseDeliveryParams{
			Column1: merchantID, Column2: orderID,
		})
	})
}

func (s *Service) DeliveryFor(ctx context.Context, merchantID, orderID uuid.UUID) (sqlcgen.Delivery, error) {
	var out sqlcgen.Delivery
	err := s.store.WithTenant(ctx, merchantID, func(q *sqlcgen.Queries) error {
		d, err := q.GetDeliveryByOrder(ctx, sqlcgen.GetDeliveryByOrderParams{
			Column1: merchantID, Column2: orderID,
		})
		out = d
		return err
	})
	return out, err
}

// VerifyDelivery checks the drop-off code, completes the order and closes the
// delivery job.
func (s *Service) VerifyDelivery(ctx context.Context, merchantID, staffID, orderID uuid.UUID, code string) error {
	code = strings.ToUpper(strings.TrimSpace(code))
	if len(code) != 6 {
		return ErrInvalidCode
	}
	return s.store.WithTenantTx(ctx, merchantID, func(_ pgx.Tx, q *sqlcgen.Queries) error {
		order, err := q.GetOrderByID(ctx, orderID)
		if err != nil {
			return ErrNotFound
		}
		if order.Status != sqlcgen.OrderStatusReady && order.Status != sqlcgen.OrderStatusOutForDelivery {
			return ErrNotReady
		}
		hc, err := q.GetHandoffCode(ctx, sqlcgen.GetHandoffCodeParams{
			Column1: orderID, Column2: sqlcgen.HandoffKindDelivery,
		})
		if err != nil {
			return ErrInvalidCode
		}
		if hc.UsedAt.Valid || (hc.ExpiresAt.Valid && time.Now().After(hc.ExpiresAt.Time)) || hc.Attempts >= MaxHandoffAttempts {
			return ErrCodeLocked
		}
		if !hashMatches(hc.Salt, hc.CodeHash, code) {
			_ = q.IncrementHandoffAttempts(ctx, hc.ID)
			return ErrInvalidCode
		}
		if err := q.UseHandoffCode(ctx, hc.ID); err != nil {
			return err
		}
		if _, err := q.MarkOrderCollected(ctx, orderID); err != nil {
			return err
		}
		if err := q.CompleteDelivery(ctx, sqlcgen.CompleteDeliveryParams{
			Column1: merchantID, Column2: orderID,
		}); err != nil {
			return err
		}
		return q.InsertStaffOrderEvent(ctx, sqlcgen.InsertStaffOrderEventParams{
			Column1:  merchantID,
			Column2:  orderID,
			Column3:  "delivery_verified",
			ToStatus: ptr(sqlcgen.OrderStatusCompleted),
			Column5:  staffID,
			Column6:  json.RawMessage(`{}`),
		})
	})
}

// VerifyPickup checks the guest's pickup code and completes a ready order.
func (s *Service) VerifyPickup(ctx context.Context, merchantID, staffID, orderID uuid.UUID, code string) error {
	code = strings.ToUpper(strings.TrimSpace(code))
	if len(code) != 6 {
		return ErrInvalidCode
	}
	return s.store.WithTenantTx(ctx, merchantID, func(_ pgx.Tx, q *sqlcgen.Queries) error {
		order, err := q.GetOrderByID(ctx, orderID)
		if err != nil {
			return ErrNotFound
		}
		if order.Status != sqlcgen.OrderStatusReady {
			return ErrNotReady
		}
		hc, err := q.GetHandoffCode(ctx, sqlcgen.GetHandoffCodeParams{
			Column1: orderID, Column2: sqlcgen.HandoffKindPickup,
		})
		if err != nil {
			return ErrInvalidCode
		}
		if hc.UsedAt.Valid || (hc.ExpiresAt.Valid && time.Now().After(hc.ExpiresAt.Time)) || hc.Attempts >= MaxHandoffAttempts {
			return ErrCodeLocked
		}
		if !hashMatches(hc.Salt, hc.CodeHash, code) {
			_ = q.IncrementHandoffAttempts(ctx, hc.ID)
			return ErrInvalidCode
		}
		if err := q.UseHandoffCode(ctx, hc.ID); err != nil {
			return err
		}
		if _, err := q.MarkOrderCollected(ctx, orderID); err != nil {
			return err
		}
		return q.InsertStaffOrderEvent(ctx, sqlcgen.InsertStaffOrderEventParams{
			Column1:  merchantID,
			Column2:  orderID,
			Column3:  "pickup_verified",
			ToStatus: ptr(sqlcgen.OrderStatusCompleted),
			Column5:  staffID,
			Column6:  json.RawMessage(`{}`),
		})
	})
}

// ValidSlot reports whether scheduled is a valid 30-minute slot within the
// next seven days and inside opening hours.
func ValidSlot(hours []Hour, now, scheduled time.Time) bool {
	if scheduled.Before(now.Add(5*time.Minute)) || scheduled.After(now.Add(7*24*time.Hour)) {
		return false
	}
	if scheduled.Minute()%30 != 0 || scheduled.Second() != 0 || scheduled.Nanosecond() != 0 {
		return false
	}
	return IsOpen(hours, scheduled)
}

// Slots returns bookable 30-minute slots for the next seven days.
func Slots(hours []Hour, now time.Time, limit int) []time.Time {
	if limit <= 0 {
		return nil
	}
	start := now.Truncate(30 * time.Minute).Add(30 * time.Minute)
	out := make([]time.Time, 0, limit)
	for i := 0; i < 7*24*2 && len(out) < limit; i++ {
		t := start.Add(time.Duration(i) * 30 * time.Minute)
		if t.Before(now.Add(5 * time.Minute)) {
			continue
		}
		if IsOpen(hours, t) {
			out = append(out, t)
		}
	}
	return out
}

func defaultCountry(v string) string {
	if len(v) == 2 {
		return v
	}
	return "DE"
}

func ptr[T any](v T) *T { return &v }

// --- settings -------------------------------------------------------------

// UpdateVatRate changes the merchant's single VAT rate.
func (s *Service) UpdateVatRate(ctx context.Context, merchantID uuid.UUID, vatRateBp int32) error {
	if vatRateBp < 0 || vatRateBp > 10000 {
		return ErrInvalidInput
	}
	return s.store.WithTenant(ctx, merchantID, func(q *sqlcgen.Queries) error {
		return q.UpdateVatRate(ctx, sqlcgen.UpdateVatRateParams{VatRateBp: vatRateBp, Column2: merchantID})
	})
}

// ReplaceHours replaces the weekly opening hours.
func (s *Service) ReplaceHours(ctx context.Context, merchantID uuid.UUID, hours []Hour) error {
	if len(hours) > 21 {
		return ErrInvalidInput
	}
	return s.store.WithTenantTx(ctx, merchantID, func(_ pgx.Tx, q *sqlcgen.Queries) error {
		if err := q.DeleteOpeningHours(ctx); err != nil {
			return err
		}
		for _, h := range hours {
			if h.Weekday < 0 || h.Weekday > 6 {
				return ErrInvalidInput
			}
			opens, ok := parseClock(h.Opens)
			if !ok {
				return ErrInvalidInput
			}
			closes, ok := parseClock(h.Closes)
			if !ok || opens == closes {
				return ErrInvalidInput
			}
			if err := q.InsertOpeningHour(ctx, sqlcgen.InsertOpeningHourParams{
				Column1: merchantID,
				Column2: int16(h.Weekday),
				Column3: opens,
				Column4: closes,
			}); err != nil {
				return err
			}
		}
		return nil
	})
}

// CreateZone adds a delivery zone.
func (s *Service) CreateZone(ctx context.Context, merchantID uuid.UUID, name string, prefixes []string, minOrder, fee int32) error {
	name = strings.TrimSpace(name)
	prefixes = normalizePrefixes(prefixes)
	if name == "" || len(name) > 120 || len(prefixes) == 0 || minOrder < 0 || fee < 0 {
		return ErrInvalidInput
	}
	return s.store.WithTenant(ctx, merchantID, func(q *sqlcgen.Queries) error {
		_, err := q.CreateDeliveryZone(ctx, sqlcgen.CreateDeliveryZoneParams{
			Column1: merchantID,
			Column2: name,
			Column3: prefixes,
			Column4: minOrder,
			Column5: fee,
		})
		return err
	})
}

// UpdateZone edits an existing delivery zone.
func (s *Service) UpdateZone(ctx context.Context, merchantID, zoneID uuid.UUID, name string, prefixes []string, minOrder, fee int32, active bool) error {
	name = strings.TrimSpace(name)
	prefixes = normalizePrefixes(prefixes)
	if name == "" || len(name) > 120 || len(prefixes) == 0 || minOrder < 0 || fee < 0 {
		return ErrInvalidInput
	}
	return s.store.WithTenant(ctx, merchantID, func(q *sqlcgen.Queries) error {
		return q.UpdateDeliveryZone(ctx, sqlcgen.UpdateDeliveryZoneParams{
			Name:             name,
			Column2:          prefixes,
			MinOrderMinor:    minOrder,
			DeliveryFeeMinor: fee,
			Active:           active,
			Column6:          zoneID,
		})
	})
}

// DeleteZone removes a delivery zone.
func (s *Service) DeleteZone(ctx context.Context, merchantID, zoneID uuid.UUID) error {
	return s.store.WithTenant(ctx, merchantID, func(q *sqlcgen.Queries) error {
		return q.DeleteDeliveryZone(ctx, zoneID)
	})
}

// LoadSettings returns the merchant configuration for the settings page.
func (s *Service) LoadSettings(ctx context.Context, merchantID uuid.UUID) (Settings, []Hour, []Zone, error) {
	var (
		settings Settings
		hours    []Hour
		zones    []Zone
	)
	err := s.store.WithTenant(ctx, merchantID, func(q *sqlcgen.Queries) error {
		st, err := loadSettings(ctx, q, merchantID)
		if err != nil {
			return err
		}
		settings = st
		hours, err = loadHours(ctx, q)
		if err != nil {
			return err
		}
		zones, err = loadZones(ctx, q, false)
		return err
	})
	return settings, hours, zones, err
}

// --- helpers --------------------------------------------------------------

func loadSettings(ctx context.Context, q *sqlcgen.Queries, merchantID uuid.UUID) (Settings, error) {
	row, err := q.GetStorefrontSettings(ctx, merchantID)
	if err != nil {
		return Settings{}, ErrNotFound
	}
	loc, err := time.LoadLocation(row.Timezone)
	if err != nil {
		loc = time.UTC
	}
	return Settings{
		ID:                  row.ID,
		Name:                row.Name,
		Slug:                row.Slug,
		Currency:            row.Currency,
		Timezone:            loc,
		VatRateBp:           row.VatRateBp,
		Description:         row.Description,
		Phone:               row.Phone,
		Email:               row.Email,
		Street:              row.Street,
		Line2:               row.Line2,
		Postcode:            row.Postcode,
		City:                row.City,
		Country:             row.Country,
		Cuisine:             row.Cuisine,
		PickupEnabled:       row.PickupEnabled,
		SelfDeliveryEnabled: row.SelfDeliveryEnabled,
		Tagline:             row.Tagline,
		PrimaryColor:        row.PrimaryColor,
		SecondaryColor:      row.SecondaryColor,
		AccentColor:         row.AccentColor,
		BackgroundColor:     row.BackgroundColor,
		TextColor:           row.TextColor,
		Template:            row.Template,
		Font:                row.Font,
		LogoKey:             row.LogoKey,
		CoverKey:            row.CoverKey,
		CardEnabled:         row.CardEnabled,
	}, nil
}

func loadHours(ctx context.Context, q *sqlcgen.Queries) ([]Hour, error) {
	rows, err := q.ListOpeningHours(ctx)
	if err != nil {
		return nil, err
	}
	hours := make([]Hour, 0, len(rows))
	for _, r := range rows {
		hours = append(hours, Hour{
			Weekday: int(r.Weekday),
			Opens:   clockString(r.Opens),
			Closes:  clockString(r.Closes),
		})
	}
	return hours, nil
}

func loadZones(ctx context.Context, q *sqlcgen.Queries, activeOnly bool) ([]Zone, error) {
	rows, err := q.ListDeliveryZones(ctx)
	if err != nil {
		return nil, err
	}
	zones := make([]Zone, 0, len(rows))
	for _, r := range rows {
		if activeOnly && !r.Active {
			continue
		}
		zones = append(zones, Zone{
			ID:             r.ID,
			Name:           r.Name,
			Prefixes:       r.PostcodePrefixes,
			MinOrderMinor:  r.MinOrderMinor,
			DeliveryFeeMin: r.DeliveryFeeMinor,
			Active:         r.Active,
		})
	}
	return zones, nil
}

// IsOpen reports whether t falls inside any opening window. A merchant with no
// configured hours is treated as always open.
func IsOpen(hours []Hour, t time.Time) bool {
	if len(hours) == 0 {
		return true
	}
	weekday := int(t.Weekday())
	minute := t.Hour()*60 + t.Minute()
	for _, h := range hours {
		opens, ok1 := clockMinutes(h.Opens)
		closes, ok2 := clockMinutes(h.Closes)
		if !ok1 || !ok2 {
			continue
		}
		if h.Weekday == weekday {
			if closes > opens && minute >= opens && minute < closes {
				return true
			}
			if closes < opens && minute >= opens {
				return true
			}
		}
		// A window that crosses midnight keeps the previous day open.
		if closes < opens && h.Weekday == (weekday+6)%7 && minute < closes {
			return true
		}
	}
	return false
}

// MatchZone finds the zone whose postcode prefix matches. The longest matching
// prefix wins, so specific zones can override broader ones.
func MatchZone(zones []Zone, postcode string) (Zone, bool) {
	postcode = strings.TrimSpace(strings.ToUpper(postcode))
	best := Zone{}
	bestLen := -1
	for _, z := range zones {
		if !z.Active {
			continue
		}
		for _, p := range z.Prefixes {
			p = strings.TrimSpace(strings.ToUpper(p))
			if p == "" {
				continue
			}
			if strings.HasPrefix(postcode, p) && len(p) > bestLen {
				best = z
				bestLen = len(p)
			}
		}
	}
	return best, bestLen >= 0
}

// IncludedTax returns the VAT contained in a gross amount.
func IncludedTax(grossMinor, rateBp int32) int32 {
	if grossMinor <= 0 || rateBp <= 0 {
		return 0
	}
	denom := int64(10000 + rateBp)
	return int32((int64(grossMinor)*int64(rateBp) + denom/2) / denom) // #nosec G115 -- gross is int32 and VAT rate is basis points
}

func mergeLines(lines []CartLine) []CartLine {
	merged := make(map[uuid.UUID]int)
	order := make([]uuid.UUID, 0, len(lines))
	for _, l := range lines {
		if l.ItemID == uuid.Nil || l.Quantity < 1 || l.Quantity > MaxQuantityPerItem {
			return nil
		}
		if _, ok := merged[l.ItemID]; !ok {
			order = append(order, l.ItemID)
		}
		merged[l.ItemID] += l.Quantity
	}
	out := make([]CartLine, 0, len(order))
	for _, id := range order {
		out = append(out, CartLine{ItemID: id, Quantity: merged[id]})
	}
	return out
}

func hashMatches(salt, expected []byte, code string) bool {
	got := hashCode(salt, code)
	return len(got) == len(expected) && subtle.ConstantTimeCompare(got, expected) == 1
}

func hashCode(salt []byte, code string) []byte {
	h := sha256.New()
	h.Write(salt)
	h.Write([]byte(code))
	return h.Sum(nil)
}

func randomRef() (string, error) {
	buf := make([]byte, 6)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return strings.ToUpper(hex.EncodeToString(buf)), nil
}

func randomCode() (string, error) {
	buf := make([]byte, 3)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return strings.ToUpper(hex.EncodeToString(buf)), nil
}

func normalizePrefixes(prefixes []string) []string {
	out := make([]string, 0, len(prefixes))
	for _, p := range prefixes {
		p = strings.ToUpper(strings.TrimSpace(p))
		if p != "" {
			out = append(out, p)
		}
	}
	sort.Strings(out)
	return out
}

func parseClock(v string) (pgtype.Time, bool) {
	minutes, ok := clockMinutes(v)
	if !ok {
		return pgtype.Time{}, false
	}
	return pgtype.Time{Microseconds: int64(minutes) * 60 * 1_000_000, Valid: true}, true
}

func clockMinutes(v string) (int, bool) {
	v = strings.TrimSpace(v)
	parts := strings.SplitN(v, ":", 2)
	if len(parts) != 2 {
		return 0, false
	}
	var h, m int
	if _, err := fmt.Sscanf(parts[0], "%d", &h); err != nil {
		return 0, false
	}
	if _, err := fmt.Sscanf(parts[1], "%d", &m); err != nil {
		return 0, false
	}
	if h < 0 || h > 23 || m < 0 || m > 59 {
		return 0, false
	}
	return h*60 + m, true
}

func clockString(t pgtype.Time) string {
	if !t.Valid {
		return "00:00"
	}
	total := int(t.Microseconds / (60 * 1_000_000))
	return fmt.Sprintf("%02d:%02d", total/60, total%60)
}

// MerchantProfile is the editable public restaurant profile.
type MerchantProfile struct {
	Name                string
	LegalName           string
	Timezone            string
	Locale              string
	VATRateBP           int32
	Description         string
	Phone               string
	Email               string
	Street              string
	Line2               string
	Postcode            string
	City                string
	Country             string
	Cuisine             string
	PickupEnabled       bool
	SelfDeliveryEnabled bool
}

func (s *Service) MerchantProfile(ctx context.Context, merchantID uuid.UUID) (MerchantProfile, error) {
	var out MerchantProfile
	err := s.store.WithTenant(ctx, merchantID, func(q *sqlcgen.Queries) error {
		m, err := q.GetMerchantFull(ctx, merchantID)
		if err != nil {
			return err
		}
		out = MerchantProfile{
			Name: m.Name, Timezone: m.Timezone, Locale: m.Locale, VATRateBP: m.VatRateBp,
			Description: m.Description, Phone: m.Phone, Email: m.Email, Street: m.Street,
			Line2: m.Line2, Postcode: m.Postcode, City: m.City, Country: m.Country,
			Cuisine: m.Cuisine, PickupEnabled: m.PickupEnabled, SelfDeliveryEnabled: m.SelfDeliveryEnabled,
		}
		if m.LegalName != nil {
			out.LegalName = *m.LegalName
		}
		return nil
	})
	return out, err
}

func (s *Service) UpdateMerchantProfile(ctx context.Context, merchantID uuid.UUID, p MerchantProfile) error {
	return s.store.WithTenant(ctx, merchantID, func(q *sqlcgen.Queries) error {
		return q.UpdateMerchantFull(ctx, sqlcgen.UpdateMerchantFullParams{
			Name: p.Name, LegalName: &p.LegalName, Timezone: p.Timezone, Locale: p.Locale,
			VatRateBp: p.VATRateBP, Description: p.Description, Phone: p.Phone, Email: p.Email,
			Street: p.Street, Line2: p.Line2, Postcode: p.Postcode, City: p.City,
			Country: p.Country, Cuisine: p.Cuisine, PickupEnabled: p.PickupEnabled,
			SelfDeliveryEnabled: p.SelfDeliveryEnabled, ID: merchantID,
		})
	})
}

// Brand is the editable storefront branding.
type Brand struct {
	LogoAssetID  *uuid.UUID
	CoverAssetID *uuid.UUID
	Primary      string
	Secondary    string
	Font         string
	Tagline      string
	Template     string
	Accent       string
	Background   string
	Text         string
}

func (s *Service) Brand(ctx context.Context, merchantID uuid.UUID) (Brand, error) {
	out := Brand{
		Primary: "#1c1917", Secondary: "#78716c", Font: "Inter",
		Template: "classic-bistro", Accent: "#f59e0b", Background: "#ffffff", Text: "#111111",
	}
	err := s.store.WithTenant(ctx, merchantID, func(q *sqlcgen.Queries) error {
		b, err := q.GetBrandSettings(ctx, merchantID)
		if errors.Is(err, pgx.ErrNoRows) {
			return nil
		}
		if err != nil {
			return err
		}
		out = Brand{
			LogoAssetID: b.LogoAssetID, CoverAssetID: b.CoverAssetID,
			Primary: b.PrimaryColor, Secondary: b.SecondaryColor, Font: b.Font,
			Tagline: b.Tagline, Template: b.Template, Accent: b.AccentColor,
			Background: b.BackgroundColor, Text: b.TextColor,
		}
		return nil
	})
	return out, err
}

func (s *Service) UpdateBrand(ctx context.Context, merchantID uuid.UUID, b Brand) error {
	return s.store.WithTenant(ctx, merchantID, func(q *sqlcgen.Queries) error {
		return q.UpdateBrandSettings(ctx, sqlcgen.UpdateBrandSettingsParams{
			MerchantID: merchantID, PrimaryColor: b.Primary, SecondaryColor: b.Secondary,
			Font: b.Font, Tagline: b.Tagline, Template: b.Template, AccentColor: b.Accent,
			BackgroundColor: b.Background, TextColor: b.Text,
		})
	})
}

func (s *Service) SetBrandAsset(ctx context.Context, merchantID uuid.UUID, kind string, assetID uuid.UUID) error {
	return s.store.WithTenant(ctx, merchantID, func(q *sqlcgen.Queries) error {
		if kind == "cover" {
			return q.SetBrandCover(ctx, sqlcgen.SetBrandCoverParams{CoverAssetID: &assetID, Column2: merchantID})
		}
		return q.SetBrandLogo(ctx, sqlcgen.SetBrandLogoParams{LogoAssetID: &assetID, Column2: merchantID})
	})
}

// PaymentConnection is the restaurant's own Stripe configuration.
type PaymentConnection struct {
	Status           string
	PublishableKey   string
	SecretKeyEnc     []byte
	WebhookSecretEnc []byte
	LiveMode         bool
}

func (s *Service) PaymentConnection(ctx context.Context, merchantID uuid.UUID) (PaymentConnection, error) {
	var out PaymentConnection
	err := s.store.WithTenant(ctx, merchantID, func(q *sqlcgen.Queries) error {
		pc, err := q.GetPaymentConnection(ctx, merchantID)
		if errors.Is(err, pgx.ErrNoRows) {
			return nil
		}
		if err != nil {
			return err
		}
		out.Status = pc.Status
		out.LiveMode = pc.Livemode
		out.SecretKeyEnc = pc.SecretKeyEnc
		out.WebhookSecretEnc = pc.WebhookSecretEnc
		if pc.PublishableKey != nil {
			out.PublishableKey = *pc.PublishableKey
		}
		return nil
	})
	return out, err
}

func (s *Service) SavePaymentConnection(ctx context.Context, merchantID uuid.UUID, publishable string, secretEnc, webhookEnc []byte, live bool) error {
	return s.store.WithTenant(ctx, merchantID, func(q *sqlcgen.Queries) error {
		return q.UpsertPaymentConnection(ctx, sqlcgen.UpsertPaymentConnectionParams{
			MerchantID: merchantID, Status: "active", PublishableKey: &publishable,
			SecretKeyEnc: secretEnc, WebhookSecretEnc: webhookEnc, Livemode: live,
		})
	})
}

func (s *Service) CreatePaymentRecord(ctx context.Context, merchantID, orderID uuid.UUID, ref string, amount int32, currency string) error {
	return s.store.WithTenant(ctx, merchantID, func(q *sqlcgen.Queries) error {
		return q.CreatePaymentRecord(ctx, sqlcgen.CreatePaymentRecordParams{
			Column1: merchantID, Column2: orderID, Column3: ref, AmountMinor: amount, Column5: currency,
		})
	})
}

func (s *Service) PaymentRecordByRef(ctx context.Context, merchantID uuid.UUID, ref string) (sqlcgen.GetPaymentRecordByRefRow, error) {
	var out sqlcgen.GetPaymentRecordByRefRow
	err := s.store.WithTenant(ctx, merchantID, func(q *sqlcgen.Queries) error {
		rec, err := q.GetPaymentRecordByRef(ctx, sqlcgen.GetPaymentRecordByRefParams{Column1: merchantID, Column2: ref})
		out = rec
		return err
	})
	return out, err
}

func (s *Service) SettlePayment(ctx context.Context, merchantID, recordID, orderID uuid.UUID) error {
	return s.store.WithTenantTx(ctx, merchantID, func(_ pgx.Tx, q *sqlcgen.Queries) error {
		if err := q.MarkPaymentRecordPaid(ctx, recordID); err != nil {
			return err
		}
		_, err := q.MarkOrderPaymentPaid(ctx, orderID)
		return err
	})
}

func (s *Service) FailPayment(ctx context.Context, merchantID, recordID, orderID uuid.UUID) error {
	return s.store.WithTenantTx(ctx, merchantID, func(_ pgx.Tx, q *sqlcgen.Queries) error {
		if err := q.MarkPaymentRecordFailed(ctx, recordID); err != nil {
			return err
		}
		return q.MarkOrderPaymentFailed(ctx, orderID)
	})
}

// BeginWebhookEvent records a provider event. The bool is false for duplicates.
func (s *Service) BeginWebhookEvent(ctx context.Context, merchantID uuid.UUID, eventID, eventType string, payload []byte) (int64, bool, error) {
	var id int64
	err := s.store.WithTenant(ctx, merchantID, func(q *sqlcgen.Queries) error {
		got, err := q.InsertWebhookEvent(ctx, sqlcgen.InsertWebhookEventParams{
			Column1: eventID, Column2: eventType, Column3: json.RawMessage(payload),
		})
		if errors.Is(err, pgx.ErrNoRows) {
			return nil
		}
		id = got
		return err
	})
	return id, id != 0, err
}

func (s *Service) FinishWebhookEvent(ctx context.Context, merchantID uuid.UUID, id int64, status string, errText *string) error {
	return s.store.WithTenant(ctx, merchantID, func(q *sqlcgen.Queries) error {
		return q.MarkWebhookEvent(ctx, sqlcgen.MarkWebhookEventParams{Status: status, Error: errText, Column3: id})
	})
}
