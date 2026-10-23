package templates

import "tablekit.de/tablekit/internal/i18n"

// Page is the shared view model every template receives.
type Page struct {
	Title string
	Lang  string
	T     func(key string, args ...string) string
	Nav   string

	MerchantName string
	MerchantSlug string
	BaseDomain   string

	CSRFToken  string
	SessionTTL string

	StaffName string
	StaffRole string
	SignedIn  bool

	Error  string
	Notice string

	TOTPEnabled bool
	SetupURI    string
	SetupSecret string
	QRDataURI   string

	Passkeys []PasskeyView
	Sessions []SessionView

	Stats                   DashboardStats
	Orders                  []OrderView
	Menu                    []MenuCategoryView
	PasskeyRegistrationJSON string

	MediaEnabled bool
	CanStaff     bool
	CanSettings  bool

	Storefront   StorefrontView
	Checkout     CheckoutView
	Confirmation ConfirmationView
	Settings     SettingsView
	Staff        []StaffView
	TeamSessions []TeamSessionView
}

// StaffView is an employee row on the team page.
type StaffView struct {
	ID         string
	Name       string
	Email      string
	Role       string
	RoleLabel  string
	Disabled   bool
	MFAEnabled bool
	IsSelf     bool
}

// TeamSessionView is an active login shown to owners and managers.
type TeamSessionView struct {
	ID        string
	StaffName string
	Email     string
	RoleLabel string
	IP        string
	UserAgent string
	LastSeen  string
	Current   bool
}

// NewPage builds a Page with a translator bound to the locale.
func NewPage(loc i18n.Locale, titleKey string) Page {
	tr := i18n.For(loc)
	return Page{
		Title: tr.T(titleKey),
		Lang:  tr.Tag(),
		T:     tr.T,
	}
}

type DashboardStats struct {
	OpenOrders  int
	TodayOrders int
	Revenue     string
	MenuItems   int
}

type PasskeyView struct {
	ID       string
	Name     string
	Created  string
	LastUsed string
}

type SessionView struct {
	ID        string
	IP        string
	UserAgent string
	Created   string
	LastSeen  string
	Current   bool
}

type OrderItemView struct {
	Name     string
	Quantity int32
	Total    string
}

type OrderAction struct {
	Key   string
	Label string
	Path  string
	Style string
}

type OrderView struct {
	ID          string
	Ref         string
	Status      string
	StatusLabel string
	Payment     string
	Fulfilment  string
	Customer    string
	Total       string
	Placed      string
	Notes       string
	CanHandoff  bool
	Items       []OrderItemView
	Actions     []OrderAction
}

type MenuItemView struct {
	ID        string
	Name      string
	Price     string
	Available bool
	ImageURL  string
	ThumbURL  string
}

type MenuCategoryView struct {
	ID    string
	Name  string
	Items []MenuItemView
}

// HourView is one formatted opening window.
type HourView struct {
	Day   string
	Range string
}

// ZoneView is a delivery zone shown on the public page.
type ZoneView struct {
	Name      string
	Postcodes string
	Fee       string
	MinOrder  string
}

// StorefrontItemView is one purchasable menu item.
type StorefrontItemView struct {
	ID          string
	Name        string
	Description string
	Price       string
	ImageURL    string
	ThumbURL    string
	Available   bool
}

// StorefrontCategoryView groups items.
type StorefrontCategoryView struct {
	Name        string
	Description string
	Items       []StorefrontItemView
}

// StorefrontView is the public restaurant page.
type StorefrontView struct {
	Name         string
	Tagline      string
	Currency     string
	PrimaryColor string
	Open         bool
	Hours        []HourView
	Menu         []StorefrontCategoryView
	Zones        []ZoneView
}

// CheckoutView is the guest checkout form.
type CheckoutView struct {
	Name     string
	Open     bool
	Currency string
	Zones    []ZoneView
}

// ConfirmationLine is a placed line on the confirmation page.
type ConfirmationLine struct {
	Name     string
	Quantity int32
	Total    string
}

// ConfirmationView is shown after an order is placed.
type ConfirmationView struct {
	Ref        string
	Status     string
	Name       string
	Fulfilment string
	Pickup     bool
	Code       string
	Subtotal   string
	Fee        string
	Tax        string
	Total      string
	Items      []ConfirmationLine
}

// HourRow is an editable opening window.
type HourRow struct {
	Weekday int
	Label   string
	Enabled bool
	Opens   string
	Closes  string
}

// ZoneSettingView is an editable delivery zone.
type ZoneSettingView struct {
	ID       string
	Name     string
	Prefixes string
	MinOrder string
	Fee      string
	Active   bool
}

// SettingsView is the merchant settings page.
type SettingsView struct {
	VATPercent string
	Hours      []HourRow
	Zones      []ZoneSettingView
}
