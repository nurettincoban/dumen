package secrets

import "testing"

func TestScan(t *testing.T) {
	bad := []string{
		"key AKIAIOSFODNN7EXAMPLE here",
		"token ghp_abcdefghijklmnopqrstuvwxyz0123456789",
		"glpat-abcdefghij0123456789",
		"-----BEGIN RSA PRIVATE KEY-----",
		"password = hunter2hunter2",
		"API_KEY: 'abcd1234efgh'",
		"postgres://admin:s3cretpw@db.internal/app",
		"sk-ant-api03-abcdefghijklmnopqrstuvwx",
	}
	for _, s := range bad {
		if Scan(s) == nil {
			t.Errorf("expected a match for %q", s)
		}
	}
	good := []string{
		"",
		"All new endpoints use requireAuth",
		"Release week: do not touch src/auth/**",
		"We store the password hash with argon2id",
		"token rotation is handled by the gateway",
		"https://gitlab.example.com/team/app/-/merge_requests/12",
	}
	for _, s := range good {
		if err := Scan(s); err != nil {
			t.Errorf("unexpected match for %q: %v", s, err)
		}
	}
}
