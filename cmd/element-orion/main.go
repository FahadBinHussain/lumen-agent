package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"element-orion/internal/agent"
	"element-orion/internal/auditlog"
	"element-orion/internal/bridge"
	"element-orion/internal/config"
	"element-orion/internal/dashboard"
	"element-orion/internal/discordbot"
	"element-orion/internal/eventwebhook"
	"element-orion/internal/httpaux"
	"element-orion/internal/llm"
	"element-orion/internal/notify"
	"element-orion/internal/persist"
	"element-orion/internal/sandbox"
	"element-orion/internal/tools"
)

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintf(os.Stderr, "%v\n", err)
		os.Exit(1)
	}
}

func run(args []string) error {
	command := "serve"
	if len(args) > 0 && !strings.HasPrefix(args[0], "-") {
		command = args[0]
		args = args[1:]
	}

	switch command {
	case "serve":
		return runServe(args)
	case "system-event":
		return runSystemEvent(args)
	case "help", "-h", "--help":
		printUsage()
		return nil
	default:
		return fmt.Errorf("unknown command %q\n\n%s", command, usageText())
	}
}

func runServe(args []string) error {
	flags := flag.NewFlagSet("serve", flag.ContinueOnError)
	flags.SetOutput(os.Stderr)
	configPath := flags.String("config", "config/element-orion.yaml", "Path to the Element Orion YAML config")
	workspaceDir := flags.String("workspace-dir", "", "Override the workspace directory available to tools and memory loading")
	if err := flags.Parse(args); err != nil {
		return err
	}

	cfg, err := config.Load(*configPath)
	if err != nil {
		return fmt.Errorf("load config: %w", err)
	}

	overrideWorkspaceDir := strings.TrimSpace(*workspaceDir)
	if overrideWorkspaceDir == "" {
		overrideWorkspaceDir = strings.TrimSpace(os.Getenv("ELEMENT_ORION_WORKSPACE_DIR"))
	}
	if err := cfg.OverrideWorkspaceRoot(overrideWorkspaceDir); err != nil {
		return fmt.Errorf("apply workspace override: %w", err)
	}

	if err := os.MkdirAll(cfg.App.SessionDir, 0o755); err != nil {
		return fmt.Errorf("create session dir: %w", err)
	}

	if err := os.MkdirAll(cfg.App.MemoryDir, 0o755); err != nil {
		return fmt.Errorf("create memory dir: %w", err)
	}

	alog, err := auditlog.New(cfg.LogDir())
	if err != nil {
		return fmt.Errorf("open audit log: %w", err)
	}
	defer alog.Close()

	var persistStore *persist.Store
	if cfg.Persistence.Enabled {
		dsn, err := cfg.Persistence.ResolvePersistenceDatabaseURL()
		if err != nil {
			return fmt.Errorf("resolve persistence database: %w", err)
		}
		persistStore, err = persist.Open(context.Background(), dsn, cfg.App.SessionDir, cfg.App.WorkspaceRoot, cfg.Persistence.Exclude)
		if err != nil {
			return fmt.Errorf("initialize persistence: %w", err)
		}
		defer func() {
			shutdownCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			persistStore.SyncNow(shutdownCtx)
			persistStore.Close()
		}()
		if err := persistStore.Restore(context.Background()); err != nil {
			log.Printf("persistence: restore failed (continuing with local state): %v", err)
		} else {
			log.Printf("persistence: restore pass complete")
		}
	}

	apiKey, err := cfg.ResolveAPIKey()
	if err != nil {
		return fmt.Errorf("resolve API key: %w", err)
	}

	registry, err := tools.NewRegistry(cfg)
	if err != nil {
		return fmt.Errorf("initialize tools: %w", err)
	}
	defer func() {
		if closeErr := registry.Close(); closeErr != nil {
			alog.Write("error", "", map[string]any{"op": "close_tools_registry", "error": closeErr.Error()})
		}
	}()

	client := llm.NewClient(
		cfg.LLM.BaseURL,
		apiKey,
		cfg.LLM.APIType,
		cfg.LLM.Headers,
		cfg.LLM.KimiNoThink,
		cfg.LLM.GLMNoThink,
		cfg.LLMTimeout(),
	)
	runner := agent.NewRunner(cfg, client, registry)
	var sandboxManager tools.SandboxManager
	if cfg.BackgroundTasks.Sandbox.Enabled {
		sandboxManager = sandbox.NewManager(cfg)
		runner.SetSandboxManager(sandboxManager)
	}
	service, err := discordbot.New(cfg, runner, alog, sandboxManager)
	if err != nil {
		return fmt.Errorf("initialize Discord service: %w", err)
	}
	if persistStore != nil {
		service.SetPersistenceToucher(persistStore.Touch)
	}

	var bridgeService *bridge.Service
	if cfg.Bridge.Enabled {
		bridgeService, err = bridge.New(cfg, runner)
		if err != nil {
			return fmt.Errorf("initialize bridge service: %w", err)
		}
		if persistStore != nil {
			bridgeService.SetPersistenceToucher(persistStore.Touch)
		}
		bridgeService.SetDiscord(service)
	}

	notifyService, err := buildNotify(&cfg)
	if err != nil {
		return fmt.Errorf("initialize notify service: %w", err)
	}
	if notifyService != nil {
		defer notifyService.Close()
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	workers := 1
	errCh := make(chan error, 4)

	if persistStore != nil {
		workers++
		go func() {
			persistStore.Run(ctx, cfg.Persistence.PersistenceInterval())
			errCh <- nil
		}()
	}

	log.Printf(
		"startup: config=%s dashboard.enabled=%t dashboard.listen_addr=%q dashboard.path=%q",
		cfg.SourcePath(),
		cfg.Dashboard.Enabled,
		cfg.Dashboard.ListenAddr,
		cfg.Dashboard.Path,
	)

	go func() {
		errCh <- service.Run(ctx)
	}()

	if bridgeService != nil {
		workers++
		go func() {
			errCh <- bridgeService.Run(ctx)
		}()
	}

	if notifyService != nil {
		workers++
		go func() {
			errCh <- notifyService.Run(ctx)
		}()
	}

	if httpaux.CanShareListener(cfg) {
		workers++
		go func() {
			errCh <- httpaux.Run(ctx, cfg, alog)
		}()
	} else {
		if cfg.EventWebhook.Enabled {
			workers++
			go func() {
				errCh <- eventwebhook.Run(ctx, cfg, alog)
			}()
		}

		if cfg.Dashboard.Enabled {
			workers++
			go func() {
				errCh <- dashboard.Run(ctx, cfg)
			}()
		}
	}

	var firstErr error
	for i := 0; i < workers; i++ {
		err := <-errCh
		if err != nil && firstErr == nil {
			firstErr = err
			stop()
		}
	}

	return firstErr
}

// buildNotify constructs the notify poller service (steam-updates, free-games,
// neon usage) only when at least one poller is enabled in config; returns nil
// when everything is off so the base binary behaves exactly as before.
func buildNotify(cfg *config.Config) (*notify.Service, error) {
	n := cfg.Notify
	anyEnabled := n.SteamUpdates.Enabled || n.FreeGames.Enabled || n.NeonUsage.Enabled || n.Supabase.Enabled || n.CrackWatch.Enabled
	if !anyEnabled {
		return nil, nil
	}
	nc := notify.Config{
		WebhookURL:     n.WebhookURL,
		WebhookToken:   n.WebhookToken,
		WebhookTokenEnv: n.WebhookTokenEnv,
		DatabaseURL:    n.DatabaseURL,
		DatabaseURLEnv: n.DatabaseURLEnv,
		SteamUpdates: notify.SteamUpdatesCfg{
			Enabled:    n.SteamUpdates.Enabled,
			Interval:   n.SteamUpdates.Interval,
			AppIDs:     n.SteamUpdates.AppIDs,
			ThreadIDs:  n.SteamUpdates.ThreadIDs,
			MaxAgeDays: n.SteamUpdates.MaxAgeDays,
			WebhookURL: n.SteamUpdates.WebhookURL,
		},
		FreeGames: notify.FreeGamesCfg{
			Enabled:    n.FreeGames.Enabled,
			Interval:   n.FreeGames.Interval,
			ThreadIDs:  n.FreeGames.ThreadIDs,
			WebhookURL: n.FreeGames.WebhookURL,
		},
		CrackWatch: notify.CrackWatchCfg{
			Enabled:    n.CrackWatch.Enabled,
			Interval:   n.CrackWatch.Interval,
			FeedURL:    n.CrackWatch.FeedURL,
			ThreadIDs:  n.CrackWatch.ThreadIDs,
			WebhookURL: n.CrackWatch.WebhookURL,
		},
		NeonUsage: notify.NeonUsageCfg{
			Enabled:      n.NeonUsage.Enabled,
			Interval:     n.NeonUsage.Interval,
			WarningHours: n.NeonUsage.WarningHours,
			ThreadID:     n.NeonUsage.ThreadID,
			APIKeyEnv:    n.NeonUsage.APIKeyEnv,
			StatePath:    n.NeonUsage.StatePath,
			Export: notify.NeonExportCfg{
				Enabled:        n.NeonUsage.Export.Enabled,
				Repo:           n.NeonUsage.Export.Repo,
				Branch:         n.NeonUsage.Export.Branch,
				Path:           n.NeonUsage.Export.Path,
				KeyEnv:         n.NeonUsage.Export.KeyEnv,
				GitHubTokenEnv: n.NeonUsage.Export.GitHubTokenEnv,
				ExportTimeout:  n.NeonUsage.Export.ExportTimeout,
				ExportInterval: n.NeonUsage.Export.ExportInterval,
			},
		},
		Supabase: notify.SupabaseCfg{
			Enabled:                 n.Supabase.Enabled,
			Interval:                n.Supabase.Interval,
			ThreadID:                n.Supabase.ThreadID,
			AppStateTable:           n.Supabase.AppStateTable,
			ProjectRefs:             n.Supabase.ProjectRefs,
			EgressThreshold:         n.Supabase.EgressThreshold,
			DBThreshold:             n.Supabase.DBThreshold,
			AppStateDatabaseURL:     n.Supabase.AppStateDatabaseURL,
			AppStateDatabaseURLEnv:  n.Supabase.AppStateDatabaseURLEnv,
			StatePath:               n.Supabase.StatePath,
		},
	}
	return notify.New(context.Background(), nc)
}

func runSystemEvent(args []string) error {
	flags := flag.NewFlagSet("system-event", flag.ContinueOnError)
	flags.SetOutput(os.Stderr)
	configPath := flags.String("config", "config/element-orion.yaml", "Path to the Element Orion YAML config")
	text := flags.String("text", "", "System event text to queue for heartbeat")
	mode := flags.String("mode", "now", "Delivery mode: now or next-heartbeat")
	if err := flags.Parse(args); err != nil {
		return err
	}

	cfg, err := config.Load(*configPath)
	if err != nil {
		return fmt.Errorf("load config: %w", err)
	}

	if err := discordbot.EnqueueSystemEvent(cfg, *text, *mode); err != nil {
		return fmt.Errorf("queue system event: %w", err)
	}

	return nil
}

func printUsage() {
	fmt.Fprint(os.Stdout, usageText())
}

func usageText() string {
	return "Element Orion\n\nUsage:\n  element-orion [serve] [-config path] [-workspace-dir path]\n  element-orion system-event -text \"Check urgent follow-ups\" [-mode now|next-heartbeat] [-config path]\n  element-orion help\n\nEnvironment:\n  ELEMENT_ORION_WORKSPACE_DIR   Override the workspace directory at runtime\n\nCommands:\n  serve         Run the agent service (Discord + optional Messenger/WhatsApp bridge)\n  system-event  Queue a heartbeat system event for the running service\n  help          Show this help text\n"
}
