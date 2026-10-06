package app

import (
	"strings"
	"testing"

	"github.com/OwlShack/OwlShack/internal/api"
)

// A blank admin password compares equal to the blank a login sends, so it must be impossible to
// create a repeater without one. The store column defaults to ”, so omitting the field used to
// mean "no auth" rather than "not set".
func TestCreateRepeater_RejectsBlankPassword(t *testing.T) {
	t.Parallel()
	b := &backend{}
	for _, pw := range []string{"", " ", "\t", "\n  "} {
		err := b.CreateRepeater(t.Context(), api.RepeaterCreateInput{Name: "rp", AdminPassword: pw})
		if err == nil {
			t.Errorf("password %q was accepted", pw)
			continue
		}
		if !strings.Contains(err.Error(), "admin password is required") {
			t.Errorf("password %q: error should say what is wrong, got %v", pw, err)
		}
	}
}

// Omitting the field keeps the stored password; sending "" would clear it and reopen the hole.
// Guest stays clearable: a blank guest password grants PERM_ACL_GUEST (0), as the firmware does.
func TestUpdateRepeaterAdmin_RefusesToBlankThePassword(t *testing.T) {
	t.Parallel()
	b := &backend{}
	blank := ""
	spaces := "   "
	for _, pw := range []*string{&blank, &spaces} {
		err := b.UpdateRepeaterAdmin(t.Context(), api.RepeaterAdminInput{AdminPassword: pw})
		if err == nil {
			t.Errorf("clearing the admin password with %q was accepted", *pw)
			continue
		}
		if !strings.Contains(err.Error(), "cannot be blank") {
			t.Errorf("%q: got %v", *pw, err)
		}
	}
}
