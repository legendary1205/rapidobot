// Command rapidobot is a Telegram shop that sells Rapido-Go accounts.
package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"syscall"
	"time"

	tgbot "github.com/go-telegram/bot"
	"github.com/go-telegram/bot/models"

	"github.com/legendary1205/rapidobot/internal/bot"
	"github.com/legendary1205/rapidobot/internal/config"
	"github.com/legendary1205/rapidobot/internal/panel"
	"github.com/legendary1205/rapidobot/internal/shop"
	"github.com/legendary1205/rapidobot/internal/store"
)

var version = "dev"

func main() {
	if len(os.Args) > 1 && (os.Args[1] == "version" || os.Args[1] == "--version") {
		fmt.Println("rapidobot", version)
		return
	}
	if len(os.Args) > 2 && os.Args[1] == "backup" {
		if err := backup(os.Args[2]); err != nil {
			fmt.Fprintln(os.Stderr, "rapidobot backup:", err)
			os.Exit(1)
		}
		return
	}
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "rapidobot:", err)
		os.Exit(1)
	}
}

func run() error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	log := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: levelOf(cfg.LogLevel)}))

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	// "check" validates the configuration end to end and exits - the
	// installer runs it so a wrong password is caught at install time, not
	// at the first customer's purchase.
	checkOnly := len(os.Args) > 1 && os.Args[1] == "check"

	pc := panel.New(cfg.PanelURL, cfg.PanelUsername, cfg.PanelPassword)
	pingCtx, cancel := context.WithTimeout(ctx, 20*time.Second)
	err = pc.Ping(pingCtx)
	cancel()
	if err != nil {
		if checkOnly {
			return fmt.Errorf("panel check failed: %w", err)
		}
		// Not fatal at runtime: the panel may simply be restarting. The bot
		// still answers, and every sale reports the outage clearly.
		log.Warn("panel is not reachable yet", "err", err)
	} else {
		log.Info("panel reachable", "url", cfg.PanelURL)
	}

	// The handler closes over bt, which is only assigned further down. That
	// is safe: updates are delivered only once b.Start runs, and by then bt
	// is set. It avoids building a second Telegram client just to break the
	// bot <-> handler construction cycle.
	var bt *bot.Bot
	b, err := tgbot.New(cfg.BotToken, tgbot.WithSkipGetMe(),
		tgbot.WithDefaultHandler(func(ctx context.Context, tb *tgbot.Bot, u *models.Update) { bt.Handle(ctx, tb, u) }))
	if err != nil {
		return fmt.Errorf("telegram: %w", err)
	}
	meCtx, cancel := context.WithTimeout(ctx, 20*time.Second)
	me, err := b.GetMe(meCtx)
	cancel()
	if err != nil {
		return fmt.Errorf("telegram rejected the bot token: %w", err)
	}
	if checkOnly {
		fmt.Printf("ok: panel %s, bot @%s\n", cfg.PanelURL, me.Username)
		return nil
	}

	st, err := store.Open(ctx, cfg.DBPath)
	if err != nil {
		return fmt.Errorf("database: %w", err)
	}
	defer st.Close()

	sh := shop.New(st, pc)
	bt = bot.New(b, sh, cfg.AdminIDs, me.Username, log)

	// Anything left mid-delivery by the last shutdown is failed and refunded
	// now, and the admins are told to check each one.
	if stuck, err := sh.RecoverInterrupted(ctx); err != nil {
		log.Error("could not check for interrupted orders", "err", err)
	} else if len(stuck) > 0 {
		bt.ReportInterrupted(ctx, stuck)
	}

	_, _ = b.SetMyCommands(ctx, &tgbot.SetMyCommandsParams{Commands: []models.BotCommand{
		{Command: "start", Description: "منوی اصلی"},
		{Command: "cancel", Description: "لغو عملیات"},
	}})

	log.Info("rapidobot started", "version", version, "bot", "@"+me.Username, "admins", len(cfg.AdminIDs))
	b.Start(ctx)
	log.Info("rapidobot stopped")
	return nil
}

// backup writes a consistent copy of the database to dest while the bot keeps
// running. Copying the file directly is not safe: in WAL mode recent writes
// may still sit in the -wal file, and a copy taken mid-write can be torn.
func backup(dest string) error {
	path := os.Getenv("DB_PATH")
	if path == "" {
		path = "/data/rapidobot.db"
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	st, err := store.Open(ctx, path)
	if err != nil {
		return err
	}
	defer st.Close()
	if err := st.BackupTo(ctx, dest); err != nil {
		return err
	}
	fmt.Println(dest)
	return nil
}

func levelOf(s string) slog.Level {
	switch s {
	case "debug":
		return slog.LevelDebug
	case "warn":
		return slog.LevelWarn
	case "error":
		return slog.LevelError
	}
	return slog.LevelInfo
}
