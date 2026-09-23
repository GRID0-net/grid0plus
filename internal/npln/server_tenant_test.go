package npln

import "testing"

// TestSameTenantAcceptsBothFormsOfOneTenant: the console's npln-tenant-id is
// recorded as the full "t-dce9377b-lp1", and this project long used the short
// "dce9377b". Refusing either would refuse every call the game makes; another
// tenant must still be refused.
func TestSameTenantAcceptsBothFormsOfOneTenant(t *testing.T) {
	for _, c := range []struct {
		got, want string
		ok        bool
	}{
		{"t-dce9377b-lp1", "t-dce9377b-lp1", true},
		{"dce9377b", "t-dce9377b-lp1", true},
		{"t-dce9377b-lp1", "dce9377b", true},
		{"tenants/t-dce9377b-lp1", "t-dce9377b-lp1", true},
		{"t-adf89f68-lp1", "t-dce9377b-lp1", false},
		{"adf89f68", "t-dce9377b-lp1", false},
		{"", "t-dce9377b-lp1", false},
	} {
		if got := sameTenant(c.got, c.want); got != c.ok {
			t.Errorf("sameTenant(%q, %q) = %v, want %v", c.got, c.want, got, c.ok)
		}
	}
}
