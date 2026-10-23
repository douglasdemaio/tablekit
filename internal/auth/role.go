package auth

import (
	"errors"

	"tablekit.de/tablekit/internal/db/sqlcgen"
)

type Role string

const (
	RoleOwner   Role = "owner"
	RoleManager Role = "manager"
	RoleKitchen Role = "kitchen"
	RoleDriver  Role = "driver"
)

var ErrForbidden = errors.New("auth: forbidden")

func ParseRole(s string) (Role, error) {
	switch Role(s) {
	case RoleOwner, RoleManager, RoleKitchen, RoleDriver:
		return Role(s), nil
	}
	return "", ErrForbidden
}

type Permission string

const (
	PermDashboardView Permission = "dashboard.view"
	PermStaffManage   Permission = "staff.manage"
	PermMenuManage    Permission = "menu.manage"
	PermOrdersView    Permission = "orders.view"
	PermOrdersManage  Permission = "orders.manage"
	PermKitchen       Permission = "kitchen.use"
	PermDelivery      Permission = "delivery.use"
	PermBranding      Permission = "branding.manage"
	PermBilling       Permission = "billing.manage"
	PermSecurity      Permission = "security.manage"
	PermSettings      Permission = "settings.manage"
)

var permissions = map[Role]map[Permission]bool{
	RoleOwner: {
		PermDashboardView: true,
		PermStaffManage:   true,
		PermMenuManage:    true,
		PermOrdersView:    true,
		PermOrdersManage:  true,
		PermKitchen:       true,
		PermDelivery:      true,
		PermBranding:      true,
		PermBilling:       true,
		PermSecurity:      true,
		PermSettings:      true,
	},
	RoleManager: {
		PermDashboardView: true,
		PermStaffManage:   true,
		PermMenuManage:    true,
		PermOrdersView:    true,
		PermOrdersManage:  true,
		PermKitchen:       true,
		PermDelivery:      true,
		PermBranding:      true,
		PermSecurity:      true,
		PermSettings:      true,
	},
	RoleKitchen: {
		PermDashboardView: true,
		PermOrdersView:    true,
		PermOrdersManage:  true,
		PermKitchen:       true,
		PermMenuManage:    true,
	},
	RoleDriver: {
		PermDashboardView: true,
		PermOrdersView:    true,
		PermDelivery:      true,
		PermMenuManage:    true,
	},
}

func Can(role Role, perm Permission) bool {
	return permissions[role][perm]
}

func RoleFromDB(r sqlcgen.StaffRoleEnum) Role { return Role(r) }
