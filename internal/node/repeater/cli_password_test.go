package repeater

import (
	"strings"
	"testing"

	"github.com/OwlShack/OwlShack/internal/config"
)

// No CLI input may reach the mutation with a blank password: a blank one compares equal to the
// blank a login sends, granting admin to any node in range. Asserted on the reply, which is the
// only thing observable without a live config callback.
func TestCLIPassword_NeverAcceptsBlank(t *testing.T) {
	for _, pass := range []string{"", " ", "  ", "\t"} {
		r := &Repeater{cfg: config.RepeaterConfig{Name: "rp", AdminPassword: "keepme"}}
		if got := r.cliPassword(pass); got != "Error, password cannot be blank" {
			t.Errorf("cliPassword(%q) = %q, want a refusal", pass, got)
		}
	}
	// And no command string can route a blank through dispatch.
	for _, cmd := range []string{"password ", "password  ", "password \t", "  password   "} {
		r := &Repeater{cfg: config.RepeaterConfig{Name: "rp", AdminPassword: "keepme"}}
		if got := r.runCLI(cmd); strings.HasPrefix(got, "password now:") {
			t.Errorf("%q was accepted as a password change: %q", cmd, got)
		}
	}
}
