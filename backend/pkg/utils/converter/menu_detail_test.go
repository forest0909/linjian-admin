package converter

import (
	"testing"

	permissionV1 "go-wind-admin/api/gen/go/permission/service/v1"
)

func TestConvertCodeDetailPageDoesNotCollideWithListPage(t *testing.T) {
	c := NewMenuPermissionConverter()
	for path, want := range map[string]string{
		"map/reservoir/:id":    "reservoir:detail:view",
		"config/reservoirs":    "reservoir:view",
		"opm/users/detail/:id": "user:detail:view",
		"opm/users":            "user:view",
		"ops/patrol/tasks":     "patrol:task:view",
	} {
		if got := c.ConvertCode(path, "x", permissionV1.Menu_MENU); got != want {
			t.Errorf("%s: got %q, want %q", path, got, want)
		}
	}
}
