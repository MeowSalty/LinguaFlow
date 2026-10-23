package config

import "testing"

func TestBootstrapAdministratorIdentity(t *testing.T) {
	for _, test := range []struct {
		name, username, email, password, wantUsername, wantEmail string
		invalid                                                  bool
	}{
		{name: "optional email", username: " ADMIN ", password: "password-123", wantUsername: "admin"},
		{name: "normalized email", username: "Root", email: " ROOT@Example.COM ", password: "password-123", wantUsername: "root", wantEmail: "root@example.com"},
		{name: "ordinary administrator", username: "Alice-Admin", password: "password-123", wantUsername: "alice-admin"},
		{name: "local reserved", username: "local", password: "password-123", invalid: true},
		{name: "system reserved", username: "system", password: "password-123", invalid: true},
		{name: "unicode", username: "Ａdmin", password: "password-123", invalid: true},
		{name: "control", username: "admin\t", password: "password-123", invalid: true},
		{name: "invalid separator", username: "admin--user", password: "password-123", invalid: true},
		{name: "missing password", username: "admin", invalid: true},
		{name: "bad email", username: "admin", email: "Name <user@example.com>", password: "password-123", invalid: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			in := deploymentInputs(t)
			in.Environment["LINGUAFLOW_BOOTSTRAP_ADMIN_USERNAME"] = test.username
			in.Environment["LINGUAFLOW_BOOTSTRAP_ADMIN_PASSWORD"] = test.password
			if test.email != "" {
				in.Environment["LINGUAFLOW_BOOTSTRAP_ADMIN_EMAIL"] = test.email
			}
			resolved, err := ResolveServerConfig(in)
			if test.invalid {
				if err == nil {
					t.Fatal("invalid bootstrap administrator accepted")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			admin := resolved.Bootstrap.Admin
			if admin == nil || admin.Username != test.wantUsername || admin.Email != test.wantEmail || admin.Password != test.password {
				t.Fatal("bootstrap identity was not normalized as expected")
			}
		})
	}
}
