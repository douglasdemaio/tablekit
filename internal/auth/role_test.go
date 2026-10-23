package auth

import "testing"

func TestOwnerHasAllPermissions(t *testing.T) {
	for _, p := range []Permission{PermSecurity, PermMenuManage, PermOrdersManage, PermStaffManage, PermBilling} {
		if !Can(RoleOwner, p) {
			t.Fatalf("owner should have %s", p)
		}
	}
}

func TestKitchenCannotManageStaffOrSecurity(t *testing.T) {
	if Can(RoleKitchen, PermSecurity) {
		t.Fatal("kitchen must not manage security")
	}
	if Can(RoleKitchen, PermStaffManage) {
		t.Fatal("kitchen must not manage staff")
	}
	if !Can(RoleKitchen, PermOrdersManage) {
		t.Fatal("kitchen should manage orders")
	}
}

func TestRoleFromDBUnknownIsDenied(t *testing.T) {
	r := RoleFromDB("does-not-exist")
	if Can(r, PermOrdersManage) || Can(r, PermMenuManage) || Can(r, PermSecurity) {
		t.Fatal("unknown role must have no permissions")
	}
}
