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
	CanOrders    bool
	CanMenu      bool
	CanDelivery  bool
	CanSecurity  bool

	CustomerSignedIn bool
	CustomerName     string

	Storefront    StorefrontView
	Checkout      CheckoutView
	Confirmation  ConfirmationView
	Settings      SettingsView
	Staff         []StaffView
	TeamSessions  []TeamSessionView
	Account       AccountView
	Login         CustomerLoginView
	AccountOrder  AccountOrderView
	AccountOrders []AccountOrderView
	LoginNotice   string
	Deliveries    []DeliveryView
	Drivers       []DriverOption
	CanManageMenu bool
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
	ID           string
	Name         string
	Price        string
	Available    bool
	SoldOutUntil string
	SoldOut      bool
	ImageURL     string
	ThumbURL     string
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
	SoldOutText string
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
	Description  string
	Cuisine      string
	Address      string
	Phone        string
	Email        string
	Currency     string
	PrimaryColor string
	AccentColor  string
	LogoURL      string
	CoverURL     string
	Template     string
	Open         bool
	Hours        []HourView
	Menu         []StorefrontCategoryView
	Zones        []ZoneView
}

// CheckoutView is the guest checkout form.
type CheckoutView struct {
	Name        string
	Template    string
	Open        bool
	Currency    string
	Zones       []ZoneView
	SignedIn    bool
	Customer    string
	Email       string
	Phone       string
	Addresses   []AddressView
	Slots       []SlotView
	CardEnabled bool
	SharedHere  bool
}

type AddressView struct {
	ID        string
	Label     string
	Recipient string
	Phone     string
	Line1     string
	Line2     string
	Postcode  string
	City      string
	Country   string
	Notes     string
	IsDefault bool
	Summary   string
}

type SlotView struct {
	Value string
	Label string
}

type AccountView struct {
	Name       string
	Email      string
	Phone      string
	Addresses  []AddressView
	Shares     []ShareView
	SharedHere bool
	Restaurant string
	Customer   bool
}

type ShareView struct {
	MerchantID string
	Name       string
	URL        string
	Created    string
}

type CustomerLoginView struct {
	Next      string
	DevNotice bool
	Email     string
}

type AccountOrderView struct {
	Ref         string
	Status      string
	StatusLabel string
	Fulfilment  string
	Payment     string
	Total       string
	Placed      string
	Scheduled   string
	Items       []ConfirmationLine
	Subtotal    string
	Fee         string
	Tax         string
	Code        string
	Pickup      bool
	Address     string
	ContactName string
}

// BrandSettingsView is editable branding on the settings page.
type BrandSettingsView struct {
	Tagline        string
	PrimaryColor   string
	SecondaryColor string
	AccentColor    string
	Background     string
	TextColor      string
	Font           string
	Template       string
	LogoURL        string
	CoverURL       string
}

// ProfileSettingsView is the public restaurant profile on the settings page.
type ProfileSettingsView struct {
	Name                string
	LegalName           string
	Description         string
	Phone               string
	Email               string
	Street              string
	Line2               string
	Postcode            string
	City                string
	Country             string
	Cuisine             string
	Timezone            string
	Locale              string
	PickupEnabled       bool
	SelfDeliveryEnabled bool
}

// PaymentSettingsView shows the restaurant's Stripe configuration state.
type PaymentSettingsView struct {
	Configured     bool
	PublishableKey string
	LiveMode       bool
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
	Profile    ProfileSettingsView
	Brand      BrandSettingsView
	Payment    PaymentSettingsView
	CanBrand   bool
}

// DeliveryView is one job in the driver queue.
type DeliveryView struct {
	ID          string
	Ref         string
	Status      string
	StatusLabel string
	Customer    string
	Phone       string
	Address     string
	Notes       string
	Scheduled   string
	Total       string
	Payment     string
	Assignee    string
	Claimed     bool
	Mine        bool
	CanClaim    bool
	CanRelease  bool
	CanHandoff  bool
	CanAssign   bool
}

type DriverOption struct {
	ID   string
	Name string
}
