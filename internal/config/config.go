// Package config reads the bot's settings from the environment. Only what
// is needed to start lives here; everything an admin changes day to day
// (plans, prices, card number, trial) is edited from inside the bot.
package config

import (
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
)

type Config struct {
	BotToken      string
	AdminIDs      []int64
	PanelURL      string
	PanelUsername string
	PanelPassword string
	DBPath        string
	LogLevel      string
}

func Load() (Config, error) {
	c := Config{
		BotToken:      strings.TrimSpace(os.Getenv("BOT_TOKEN")),
		PanelURL:      strings.TrimRight(strings.TrimSpace(os.Getenv("PANEL_URL")), "/"),
		PanelUsername: strings.TrimSpace(os.Getenv("PANEL_USERNAME")),
		PanelPassword: os.Getenv("PANEL_PASSWORD"),
		DBPath:        envOr("DB_PATH", "/data/rapidobot.db"),
		LogLevel:      envOr("LOG_LEVEL", "info"),
	}

	var missing []string
	for name, v := range map[string]string{
		"BOT_TOKEN": c.BotToken, "PANEL_URL": c.PanelURL,
		"PANEL_USERNAME": c.PanelUsername, "PANEL_PASSWORD": c.PanelPassword,
	} {
		if v == "" {
			missing = append(missing, name)
		}
	}

	ids, err := parseIDs(os.Getenv("ADMIN_IDS"))
	if err != nil {
		return c, err
	}
	if len(ids) == 0 {
		missing = append(missing, "ADMIN_IDS")
	}
	c.AdminIDs = ids

	if len(missing) > 0 {
		return c, fmt.Errorf("missing required settings: %s", strings.Join(missing, ", "))
	}
	if !strings.HasPrefix(c.PanelURL, "http://") && !strings.HasPrefix(c.PanelURL, "https://") {
		return c, errors.New("PANEL_URL must start with http:// or https://")
	}
	// The address people copy from the browser is the dashboard's, but the
	// API lives at the panel root. Accept either instead of failing every
	// request with a 404 that points nowhere near the real mistake.
	c.PanelURL = strings.TrimRight(strings.TrimSuffix(c.PanelURL, "/dashboard"), "/")
	return c, nil
}

// parseIDs accepts "123,456" or "123 456".
func parseIDs(raw string) ([]int64, error) {
	var out []int64
	for _, f := range strings.FieldsFunc(raw, func(r rune) bool { return r == ',' || r == ' ' || r == ';' }) {
		id, err := strconv.ParseInt(f, 10, 64)
		if err != nil || id <= 0 {
			return nil, fmt.Errorf("ADMIN_IDS: %q is not a Telegram numeric id", f)
		}
		out = append(out, id)
	}
	return out, nil
}

func envOr(key, def string) string {
	if v := strings.TrimSpace(os.Getenv(key)); v != "" {
		return v
	}
	return def
}
