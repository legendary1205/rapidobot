package config

import "testing"

func setEnv(t *testing.T, kv map[string]string) {
	t.Helper()
	for k, v := range kv {
		t.Setenv(k, v)
	}
}

func TestDashboardURLIsAcceptedAsThePanelURL(t *testing.T) {
	setEnv(t, map[string]string{
		"BOT_TOKEN": "1:x", "ADMIN_IDS": "10", "PANEL_USERNAME": "admin", "PANEL_PASSWORD": "p",
		"PANEL_URL": "https://panel.example.com/dashboard/",
	})
	c, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if c.PanelURL != "https://panel.example.com" {
		t.Errorf("PanelURL = %q, want the panel root", c.PanelURL)
	}
}

func TestAdminIDsAcceptCommasAndSpaces(t *testing.T) {
	setEnv(t, map[string]string{
		"BOT_TOKEN": "1:x", "PANEL_URL": "https://p", "PANEL_USERNAME": "a", "PANEL_PASSWORD": "p",
		"ADMIN_IDS": "45312485, 99 ;7",
	})
	c, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if len(c.AdminIDs) != 3 || c.AdminIDs[0] != 45312485 || c.AdminIDs[2] != 7 {
		t.Errorf("AdminIDs = %v", c.AdminIDs)
	}
}

func TestMissingSettingsAreNamed(t *testing.T) {
	setEnv(t, map[string]string{"BOT_TOKEN": "", "PANEL_URL": "", "PANEL_USERNAME": "", "PANEL_PASSWORD": "", "ADMIN_IDS": ""})
	_, err := Load()
	if err == nil {
		t.Fatal("want an error")
	}
	for _, name := range []string{"BOT_TOKEN", "PANEL_URL", "ADMIN_IDS"} {
		if !contains(err.Error(), name) {
			t.Errorf("error %q does not name %s", err, name)
		}
	}
}

func TestBadAdminIDIsRejected(t *testing.T) {
	setEnv(t, map[string]string{"BOT_TOKEN": "1:x", "PANEL_URL": "https://p", "PANEL_USERNAME": "a", "PANEL_PASSWORD": "p", "ADMIN_IDS": "@myname"})
	if _, err := Load(); err == nil {
		t.Error("a username instead of a numeric id must be refused")
	}
}

func contains(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
