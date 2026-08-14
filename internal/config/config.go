package config

import (
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

type Config struct {
	App             AppConfig             `yaml:"app"`
	LLM             LLMConfig             `yaml:"llm"`
	Tools           ToolsConfig           `yaml:"tools"`
	BackgroundTasks BackgroundTasksConfig `yaml:"background_tasks"`
	Dashboard       DashboardConfig       `yaml:"dashboard"`
	Discord         DiscordConfig         `yaml:"discord"`
	Messenger       MessengerConfig       `yaml:"messenger"`
	WhatsApp        WhatsAppConfig        `yaml:"whatsapp"`
	Bridge          BridgeConfig          `yaml:"bridge"`
	Notify          NotifyConfig          `yaml:"notify"`
	Persistence     PersistenceConfig     `yaml:"persistence"`
	GIFs            GIFConfig             `yaml:"gifs"`
	ImageGen        ImageGenConfig        `yaml:"image_gen"`
	Heartbeat       HeartbeatConfig       `yaml:"heartbeat"`
	DreamMode       DreamModeConfig       `yaml:"dream_mode"`
	EventWebhook    EventWebhookConfig    `yaml:"event_webhook"`
	Skills          SkillsConfig          `yaml:"skills"`
	MCP             MCPConfig             `yaml:"mcp"`

	sourcePath string `yaml:"-"`
}

func (c Config) SourcePath() string {
	return c.sourcePath
}

func (c *Config) SetSourcePath(path string) {
	c.sourcePath = strings.TrimSpace(path)
}

type AppConfig struct {
	Name                string                     `yaml:"name"`
	WorkspaceRoot       string                     `yaml:"workspace_root"`
	SessionDir          string                     `yaml:"session_dir"`
	MemoryDir           string                     `yaml:"memory_dir"`
	LoadAllMemoryShards bool                       `yaml:"load_all_memory_shards"`
	MaxAgentLoops       int                        `yaml:"max_agent_loops"`
	MaxToolCallsPerTurn int                        `yaml:"max_tool_calls_per_turn"`
	HistoryCompaction   AppHistoryCompactionConfig `yaml:"history_compaction"`
	SecretsPath         string                     `yaml:"secrets_path"`
}

type AppHistoryCompactionConfig struct {
	Enabled                bool `yaml:"enabled"`
	TriggerTokens          int  `yaml:"trigger_tokens"`
	TargetTokens           int  `yaml:"target_tokens"`
	PreserveRecentMessages int  `yaml:"preserve_recent_messages"`
}

type LLMConfig struct {
	APIType                 string            `yaml:"api_type"`
	BaseURL                 string            `yaml:"base_url"`
	APIKey                  string            `yaml:"api_key"`
	APIKeyEnv               string            `yaml:"api_key_env"`
	Model                   string            `yaml:"model"`
	Models                  []LLMModelEntry   `yaml:"models"`
	VisionEnabled           bool              `yaml:"vision_enabled"`
	ReasoningEffort         string            `yaml:"reasoning_effort"`
	MaxThinkingToken        string            `yaml:"max_thinking_token"`
	Temperature             float64           `yaml:"temperature"`
	MaxTokens               int               `yaml:"max_tokens"`
	ContextWindowTokens     int               `yaml:"context_window_tokens"`
	InjectMessageTimestamps bool              `yaml:"inject_message_timestamps"`
	Timeout                 string            `yaml:"timeout"`
	RequestMaxAttempts      int               `yaml:"request_max_attempts"`
	RetryInitialBackoff     string            `yaml:"retry_initial_backoff"`
	RetryMaxBackoff         string            `yaml:"retry_max_backoff"`
	Headers                 map[string]string `yaml:"headers"`
	KimiNoThink             bool              `yaml:"kimi-no-think"`
	GLMNoThink              bool              `yaml:"glm-no-think"`
}

type LLMModelEntry struct {
	Name      string `yaml:"name"`
	Model     string `yaml:"model"`
	Enabled   bool   `yaml:"enabled"`
	BaseURL   string `yaml:"base_url,omitempty"`
	APIKey    string `yaml:"api_key,omitempty"`
	APIKeyEnv string `yaml:"api_key_env,omitempty"`
}

type SkillsConfig struct {
	Enabled bool             `yaml:"enabled"`
	Load    SkillsLoadConfig `yaml:"load"`
}

type SkillsLoadConfig struct {
	ExtraDirs  []string `yaml:"extra_dirs"`
	UserDir    string   `yaml:"user_dir"`
	BundledDir string   `yaml:"bundled_dir"`
}

type ToolsConfig struct {
	Enabled               []string `yaml:"enabled"`
	ExecShell             string   `yaml:"exec_shell"`
	ExecTimeout           string   `yaml:"exec_timeout"`
	MaxFileBytes          int64    `yaml:"max_file_bytes"`
	MaxSearchResults      int      `yaml:"max_search_results"`
	MaxCommandOutputBytes int      `yaml:"max_command_output_bytes"`
	AllowedCommands       []string `yaml:"allowed_commands"`
}

type BackgroundTasksConfig struct {
	DefaultMinRuntime  string                      `yaml:"default_min_runtime"`
	InjectCurrentTime  bool                        `yaml:"inject_current_time"`
	MaxEventLogEntries int                         `yaml:"max_event_log_entries"`
	Sandbox            BackgroundTaskSandboxConfig `yaml:"sandbox"`
}

type BackgroundTaskSandboxConfig struct {
	Enabled      bool   `yaml:"enabled"`
	Force        bool   `yaml:"force"`
	UseSudo      bool   `yaml:"use_sudo"`
	Provider     string `yaml:"provider"`
	Release      string `yaml:"release"`
	Architecture string `yaml:"architecture"`
	Mirror       string `yaml:"mirror"`
	MachinesDir  string `yaml:"machines_dir"`
	SetupTimeout string `yaml:"setup_timeout"`
	AutoCleanup  bool   `yaml:"auto_cleanup"`
}

type DashboardConfig struct {
	Enabled    bool   `yaml:"enabled"`
	ListenAddr string `yaml:"listen_addr"`
	Path       string `yaml:"path"`
}

type DiscordConfig struct {
	TokenMode                   string   `yaml:"token_mode"`
	BotToken                    string   `yaml:"bot_token"`
	BotTokenEnv                 string   `yaml:"bot_token_env"`
	UserToken                   string   `yaml:"user_token"`
	AllowDirectMessages         bool     `yaml:"allow_direct_messages"`
	AllowGroupDirectMessages    bool     `yaml:"allow_group_direct_messages"`
	AllowedGuildIDs             []string `yaml:"allowed_guild_ids"`
	AllowedDMUserIDs            []string `yaml:"allowed_dm_user_ids"`
	AllowedOutboundChannelIDs   []string `yaml:"allowed_outbound_channel_ids"`
	GuildSessionScope           string   `yaml:"guild_session_scope"`
	ReplyToMessage              bool     `yaml:"reply_to_message"`
	DownloadIncomingAttachments bool     `yaml:"download_incoming_attachments"`
	IncomingAttachmentsDir      string   `yaml:"incoming_attachments_dir"`
}

type MessengerConfig struct {
	Enabled          bool     `yaml:"enabled"`
	CookiesPath      string   `yaml:"cookies_path"`
	AllowedThreadIDs []string `yaml:"allowed_thread_ids"`
}

type WhatsAppConfig struct {
	Enabled      bool   `yaml:"enabled"`
	StoreDir     string `yaml:"store_dir"`
	DatabaseURL  string `yaml:"database_url"`
	Proxy        string `yaml:"proxy"`
}

type BridgeConfig struct {
	Enabled              bool   `yaml:"enabled"`
	ListenAddr           string `yaml:"listen_addr"`
	NotificationsPath    string `yaml:"notifications_path"`
	NotificationsEnabled bool   `yaml:"notifications_enabled"`
	BNPEnabled           bool   `yaml:"bnp_enabled"`
	Secret               string `yaml:"secret"`
	SecretEnv            string `yaml:"secret_env"`
}

// NotifyConfig mirrors the murmur Vercel pollers' env surface. Copy-only for
// now: all pollers default to disabled; flip enabled flags at cutover.
type NotifyConfig struct {
	Enabled       bool             `yaml:"enabled"`
	WebhookURL    string           `yaml:"webhook_url"`
	WebhookToken  string           `yaml:"webhook_token"`
	WebhookTokenEnv string         `yaml:"webhook_token_env"`
	DatabaseURL   string           `yaml:"database_url"`
	DatabaseURLEnv string          `yaml:"database_url_env"`
	SteamUpdates  NotifySteamCfg   `yaml:"steam_updates"`
	FreeGames     NotifyFreeGames  `yaml:"free_games"`
	NeonUsage     NotifyNeonUsage  `yaml:"neon_usage"`
}

type NotifySteamCfg struct {
	Enabled    bool   `yaml:"enabled"`
	Interval   string `yaml:"interval"`
	AppIDs     string `yaml:"app_ids"`
	ThreadIDs  string `yaml:"thread_ids"`
	MaxAgeDays int    `yaml:"max_age_days"`
	WebhookURL string `yaml:"webhook_url"`
}

type NotifyFreeGames struct {
	Enabled    bool   `yaml:"enabled"`
	Interval   string `yaml:"interval"`
	ThreadIDs  string `yaml:"thread_ids"`
	WebhookURL string `yaml:"webhook_url"`
}

type NotifyNeonUsage struct {
	Enabled      bool     `yaml:"enabled"`
	Interval     string   `yaml:"interval"`
	WarningHours float64  `yaml:"warning_hours"`
	ThreadID     string   `yaml:"thread_id"`
	APIKeyEnv    []string `yaml:"api_key_env"`
	StatePath    string   `yaml:"state_path"`
}

// PersistenceConfig snapshots the session dir to an external Postgres
// (Neon) table so state survives Render's ephemeral container filesystem.
// The local dir stays authoritative; the DB is a restore-on-fresh-box +
// periodic/touch backup layer.
type PersistenceConfig struct {
	Enabled        bool     `yaml:"enabled"`
	DatabaseURL    string   `yaml:"database_url"`
	DatabaseURLEnv string   `yaml:"database_url_env"`
	Interval       string   `yaml:"interval"`
	Exclude        []string `yaml:"exclude"`
}

// ResolvePersistenceDatabaseURL returns the configured DSN: an explicit
// database_url wins, otherwise the env var named by database_url_env
// (defaulting to DATABASE_URL).
func (c PersistenceConfig) ResolvePersistenceDatabaseURL() (string, error) {
	if strings.TrimSpace(c.DatabaseURL) != "" {
		return strings.TrimSpace(c.DatabaseURL), nil
	}
	envName := strings.TrimSpace(c.DatabaseURLEnv)
	if envName == "" {
		envName = "DATABASE_URL"
	}
	value := strings.TrimSpace(os.Getenv(envName))
	if value == "" {
		return "", fmt.Errorf("persistence: environment variable %q is empty; set persistence.database_url or export the variable", envName)
	}
	return value, nil
}

// PersistenceInterval returns the parsed sync interval, defaulting to 1m.
func (c PersistenceConfig) PersistenceInterval() time.Duration {
	if strings.TrimSpace(c.Interval) == "" {
		return time.Minute
	}
	d, err := time.ParseDuration(strings.TrimSpace(c.Interval))
	if err != nil || d <= 0 {
		return time.Minute
	}
	return d
}

// DefaultPersistenceExclude are relative paths under the session dir that are
// never snapshotted: sandboxes are huge, whatsapp.db is already backed up via
// internal/neon, attachments are transient media, logs grow unbounded.
var DefaultPersistenceExclude = []string{"sandboxes", "whatsapp", "incoming-attachments", "logs"}

type GIFConfig struct {
	Enabled       bool   `yaml:"enabled"`
	Provider      string `yaml:"provider"`
	APIKey        string `yaml:"api_key"`
	APIKeyEnv     string `yaml:"api_key_env"`
	SearchLimit   int    `yaml:"search_limit"`
	ContentFilter string `yaml:"content_filter"`
}

type ImageGenConfig struct {
	Enabled   bool   `yaml:"enabled"`
	Model     string `yaml:"model"`
	OutputDir string `yaml:"output_dir"`
}

type HeartbeatConfig struct {
	Every             string                     `yaml:"every"`
	Model             string                     `yaml:"model"`
	LightContext      bool                       `yaml:"light_context"`
	IsolatedSession   bool                       `yaml:"isolated_session"`
	AckMaxChars       int                        `yaml:"ack_max_chars"`
	ShowOK            bool                       `yaml:"show_ok"`
	ShowAlerts        bool                       `yaml:"show_alerts"`
	UseIndicator      bool                       `yaml:"use_indicator"`
	EventPollInterval string                     `yaml:"event_poll_interval"`
	ActiveHours       HeartbeatActiveHoursConfig `yaml:"active_hours"`
	Target            HeartbeatTargetConfig      `yaml:"target"`
}

type HeartbeatActiveHoursConfig struct {
	Timezone string `yaml:"timezone"`
	Start    string `yaml:"start"`
	End      string `yaml:"end"`
}

type HeartbeatTargetConfig struct {
	GuildID   string `yaml:"guild_id"`
	ChannelID string `yaml:"channel_id"`
	UserID    string `yaml:"user_id"`
}

type DreamModeConfig struct {
	Enabled      bool                       `yaml:"enabled"`
	Every        string                     `yaml:"every"`
	Model        string                     `yaml:"model"`
	LightContext bool                       `yaml:"light_context"`
	UseIndicator bool                       `yaml:"use_indicator"`
	SleepHours   HeartbeatActiveHoursConfig `yaml:"sleep_hours"`
}

type EventWebhookConfig struct {
	Enabled     bool   `yaml:"enabled"`
	ListenAddr  string `yaml:"listen_addr"`
	Path        string `yaml:"path"`
	Secret      string `yaml:"secret"`
	SecretEnv   string `yaml:"secret_env"`
	DefaultMode string `yaml:"default_mode"`
}

type MCPConfig struct {
	Servers []MCPServerConfig `yaml:"servers"`
}

type MCPServerConfig struct {
	Name           string            `yaml:"name"`
	Enabled        bool              `yaml:"enabled"`
	Transport      string            `yaml:"transport"`
	Command        string            `yaml:"command"`
	Args           []string          `yaml:"args"`
	Endpoint       string            `yaml:"endpoint"`
	Env            map[string]string `yaml:"env"`
	WorkingDir     string            `yaml:"working_dir"`
	StartupTimeout string            `yaml:"startup_timeout"`
	ToolTimeout    string            `yaml:"tool_timeout"`
}

func Load(path string) (Config, error) {
	absPath, err := filepath.Abs(path)
	if err != nil {
		return Config{}, fmt.Errorf("resolve config path: %w", err)
	}

	data, err := os.ReadFile(absPath)
	if err != nil {
		return Config{}, fmt.Errorf("read config file: %w", err)
	}

	cfg := defaultConfig()
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return Config{}, fmt.Errorf("decode config YAML: %w", err)
	}

	cfg.sourcePath = absPath
	if err := cfg.resolvePaths(); err != nil {
		return Config{}, err
	}

	if err := cfg.validate(); err != nil {
		return Config{}, err
	}

	return cfg, nil
}

func defaultConfig() Config {
	return Config{
		App: AppConfig{
			Name:                "Element Orion",
			WorkspaceRoot:       ".",
			SessionDir:          ".element-orion",
			MemoryDir:           "",
			LoadAllMemoryShards: false,
			MaxAgentLoops:       12,
			MaxToolCallsPerTurn: 24,
			HistoryCompaction: AppHistoryCompactionConfig{
				Enabled:                true,
				PreserveRecentMessages: 12,
			},
		},
		LLM: LLMConfig{
			APIType:                 "openai",
			BaseURL:                 "https://api.openai.com/v1",
			APIKeyEnv:               "OPENAI_API_KEY",
			Model:                   "gpt-4.1-mini",
			ReasoningEffort:         "",
			Temperature:             0.2,
			MaxTokens:               4000,
			ContextWindowTokens:     24000,
			InjectMessageTimestamps: true,
			Timeout:                 "180s",
			RequestMaxAttempts:      3,
			RetryInitialBackoff:     "2s",
			RetryMaxBackoff:         "8s",
			Headers:                 map[string]string{},
		},
		Tools: ToolsConfig{
			Enabled: []string{
				"send_discord_message",
				"add_discord_reaction",
				"discord_api_request",
				"read_file",
				"write_file",
				"replace_in_file",
				"list_dir",
				"grep_search",
				"mkdir",
				"move_path",
				"delete_path",
				"exec_command",
				"compact_context",
				"send_discord_file",
				"start_background_task",
				"list_background_tasks",
				"get_background_task",
				"get_background_task_logs",
				"cancel_background_task",
				"schedule_heartbeat_wakeup",
				"list_scheduled_wakeups",
				"cancel_scheduled_wakeup",
				"read_previous_messages",
				"add_rss_feed",
				"list_rss_feeds",
				"read_rss_feed",
				"remove_rss_feed",
				"list_sandbox_containers",
				"inspect_sandbox_container",
				"create_sandbox_container",
				"start_sandbox_container",
				"stop_sandbox_container",
				"delete_sandbox_container",
			},
			ExecShell:             "/bin/zsh",
			ExecTimeout:           "120s",
			MaxFileBytes:          1 << 20,
			MaxSearchResults:      50,
			MaxCommandOutputBytes: 64 << 10,
			AllowedCommands:       []string{},
		},
		BackgroundTasks: BackgroundTasksConfig{
			DefaultMinRuntime:  "",
			InjectCurrentTime:  true,
			MaxEventLogEntries: 200,
			Sandbox: BackgroundTaskSandboxConfig{
				Enabled:      false,
				Force:        false,
				UseSudo:      false,
				Provider:     "nspawn",
				Release:      "stable",
				Architecture: "",
				Mirror:       "http://deb.debian.org/debian/",
				MachinesDir:  "",
				SetupTimeout: "20m",
				AutoCleanup:  true,
			},
		},
		Dashboard: DashboardConfig{
			Enabled:    false,
			ListenAddr: "127.0.0.1:8788",
			Path:       "/dashboard",
		},
		Discord: DiscordConfig{
			TokenMode:                   "bot",
			BotToken:                    "",
			UserToken:                   "",
			AllowDirectMessages:         false,
			AllowGroupDirectMessages:    false,
			AllowedGuildIDs:             []string{},
			AllowedDMUserIDs:            []string{},
			AllowedOutboundChannelIDs:   []string{},
			GuildSessionScope:           "channel",
			ReplyToMessage:              true,
			DownloadIncomingAttachments: true,
		},
		Messenger: MessengerConfig{
			AllowedThreadIDs: []string{},
		},
		WhatsApp: WhatsAppConfig{},
		Bridge: BridgeConfig{
			ListenAddr:           "127.0.0.1:8791",
			NotificationsPath:    "/api/automation/notifications",
			NotificationsEnabled: true,
			BNPEnabled:           true,
			SecretEnv:            "ELEMENT_ORION_BRIDGE_NOTIFICATIONS_SECRET",
		},
		Notify: NotifyConfig{
			WebhookTokenEnv:  "ELEMENT_ORION_BRIDGE_NOTIFICATIONS_SECRET",
			DatabaseURLEnv:   "DATABASE_URL",
			SteamUpdates:     NotifySteamCfg{Enabled: false, Interval: "1m", MaxAgeDays: 30},
			FreeGames:        NotifyFreeGames{Enabled: false, Interval: "1m"},
			NeonUsage:        NotifyNeonUsage{Enabled: false, Interval: "1h", WarningHours: 90},
		},
		GIFs: GIFConfig{
			Enabled:       false,
			Provider:      "giphy",
			APIKeyEnv:     "GIPHY_API_KEY",
			SearchLimit:   8,
			ContentFilter: "pg-13",
		},
		ImageGen: ImageGenConfig{
			Enabled:   false,
			Model:     "cloudflare/@cf/black-forest-labs/flux-1-schnell",
			OutputDir: ".element-orion/generated",
		},
		Heartbeat: HeartbeatConfig{
			Every:             "30m",
			Model:             "",
			LightContext:      false,
			IsolatedSession:   true,
			AckMaxChars:       300,
			ShowOK:            false,
			ShowAlerts:        true,
			UseIndicator:      true,
			EventPollInterval: "5s",
			ActiveHours:       HeartbeatActiveHoursConfig{},
			Target:            HeartbeatTargetConfig{},
		},
		DreamMode: DreamModeConfig{
			Enabled:      false,
			Every:        "6h",
			Model:        "",
			LightContext: false,
			UseIndicator: false,
			SleepHours:   HeartbeatActiveHoursConfig{},
		},
		EventWebhook: EventWebhookConfig{
			Enabled:     false,
			ListenAddr:  "127.0.0.1:8787",
			Path:        "/event",
			Secret:      "",
			SecretEnv:   "ELEMENT_ORION_EVENT_WEBHOOK_SECRET",
			DefaultMode: "now",
		},
		Skills: SkillsConfig{
			Enabled: true,
			Load: SkillsLoadConfig{
				ExtraDirs:  []string{},
				UserDir:    "~/.openclaw/skills",
				BundledDir: "../skills/bundled",
			},
		},
		MCP: MCPConfig{
			Servers: []MCPServerConfig{},
		},
	}
}

func (c *Config) resolvePaths() error {
	configDir := filepath.Dir(c.sourcePath)

	workspaceRoot, err := absFromBase(configDir, c.App.WorkspaceRoot)
	if err != nil {
		return fmt.Errorf("resolve app.workspace_root: %w", err)
	}
	c.App.WorkspaceRoot = workspaceRoot

	sessionDir, err := absFromBase(configDir, c.App.SessionDir)
	if err != nil {
		return fmt.Errorf("resolve app.session_dir: %w", err)
	}
	c.App.SessionDir = sessionDir

	c.App.MemoryDir = strings.TrimSpace(c.App.MemoryDir)
	if c.App.MemoryDir == "" {
		c.App.MemoryDir = filepath.Join(c.App.SessionDir, "memory")
	}
	memoryDir, err := absFromBase(configDir, c.App.MemoryDir)
	if err != nil {
		return fmt.Errorf("resolve app.memory_dir: %w", err)
	}
	c.App.MemoryDir = memoryDir

	c.App.SecretsPath = strings.TrimSpace(c.App.SecretsPath)
	if c.App.SecretsPath == "" {
		c.App.SecretsPath = filepath.Join(c.App.WorkspaceRoot, ".lumen", "secrets.json")
	}
	secretsPath, err := absFromBase(configDir, c.App.SecretsPath)
	if err != nil {
		return fmt.Errorf("resolve app.secrets_path: %w", err)
	}
	c.App.SecretsPath = secretsPath

	if strings.TrimSpace(c.App.Name) == "" {
		c.App.Name = "Element Orion"
	}

	if c.App.MaxToolCallsPerTurn <= 0 {
		c.App.MaxToolCallsPerTurn = 24
	}

	if strings.TrimSpace(c.Tools.ExecShell) == "" {
		c.Tools.ExecShell = "/bin/zsh"
	}
	c.BackgroundTasks.DefaultMinRuntime = strings.TrimSpace(c.BackgroundTasks.DefaultMinRuntime)
	if c.BackgroundTasks.MaxEventLogEntries <= 0 {
		c.BackgroundTasks.MaxEventLogEntries = 200
	}
	if c.BackgroundTasks.Sandbox.Force {
		c.BackgroundTasks.Sandbox.Enabled = true
	}
	c.BackgroundTasks.Sandbox.Provider = strings.TrimSpace(strings.ToLower(c.BackgroundTasks.Sandbox.Provider))
	if c.BackgroundTasks.Sandbox.Provider == "" {
		c.BackgroundTasks.Sandbox.Provider = "nspawn"
	}
	c.BackgroundTasks.Sandbox.Release = strings.TrimSpace(c.BackgroundTasks.Sandbox.Release)
	if c.BackgroundTasks.Sandbox.Release == "" {
		c.BackgroundTasks.Sandbox.Release = "stable"
	}
	c.BackgroundTasks.Sandbox.Architecture = strings.TrimSpace(strings.ToLower(c.BackgroundTasks.Sandbox.Architecture))
	c.BackgroundTasks.Sandbox.Mirror = strings.TrimSpace(c.BackgroundTasks.Sandbox.Mirror)
	if c.BackgroundTasks.Sandbox.Mirror == "" {
		c.BackgroundTasks.Sandbox.Mirror = "http://deb.debian.org/debian/"
	}
	c.BackgroundTasks.Sandbox.MachinesDir = strings.TrimSpace(c.BackgroundTasks.Sandbox.MachinesDir)
	if c.BackgroundTasks.Sandbox.MachinesDir == "" {
		c.BackgroundTasks.Sandbox.MachinesDir = filepath.Join(c.App.SessionDir, "sandboxes")
	}
	resolvedMachinesDir, err := absFromBase(configDir, c.BackgroundTasks.Sandbox.MachinesDir)
	if err != nil {
		return fmt.Errorf("resolve background_tasks.sandbox.machines_dir: %w", err)
	}
	c.BackgroundTasks.Sandbox.MachinesDir = resolvedMachinesDir
	if c.BackgroundTasks.Sandbox.Enabled {
		if err := os.MkdirAll(c.BackgroundTasks.Sandbox.MachinesDir, 0o755); err != nil {
			return fmt.Errorf("create background_tasks.sandbox.machines_dir: %w", err)
		}
	}
	c.BackgroundTasks.Sandbox.SetupTimeout = strings.TrimSpace(c.BackgroundTasks.Sandbox.SetupTimeout)
	if c.BackgroundTasks.Sandbox.SetupTimeout == "" {
		c.BackgroundTasks.Sandbox.SetupTimeout = "20m"
	}
	c.Discord.IncomingAttachmentsDir = strings.TrimSpace(c.Discord.IncomingAttachmentsDir)
	if c.Discord.IncomingAttachmentsDir == "" {
		c.Discord.IncomingAttachmentsDir = filepath.Join(c.App.SessionDir, "incoming-attachments")
	}
	resolvedAttachmentsDir, err := absFromBase(configDir, c.Discord.IncomingAttachmentsDir)
	if err != nil {
		return fmt.Errorf("resolve discord.incoming_attachments_dir: %w", err)
	}
	c.Discord.IncomingAttachmentsDir = resolvedAttachmentsDir

	c.Messenger.CookiesPath = strings.TrimSpace(c.Messenger.CookiesPath)
	if c.Messenger.CookiesPath != "" {
		resolvedCookiesPath, err := absFromBase(configDir, c.Messenger.CookiesPath)
		if err != nil {
			return fmt.Errorf("resolve messenger.cookies_path: %w", err)
		}
		c.Messenger.CookiesPath = resolvedCookiesPath
	}
	c.Messenger.AllowedThreadIDs = uniqueTrimmedStrings(c.Messenger.AllowedThreadIDs)

	c.WhatsApp.StoreDir = strings.TrimSpace(c.WhatsApp.StoreDir)
	if c.WhatsApp.StoreDir == "" {
		c.WhatsApp.StoreDir = filepath.Join(c.App.SessionDir, "whatsapp")
	}
	resolvedStoreDir, err := absFromBase(configDir, c.WhatsApp.StoreDir)
	if err != nil {
		return fmt.Errorf("resolve whatsapp.store_dir: %w", err)
	}
	c.WhatsApp.StoreDir = resolvedStoreDir
	c.WhatsApp.DatabaseURL = strings.TrimSpace(c.WhatsApp.DatabaseURL)
	c.WhatsApp.Proxy = strings.TrimSpace(c.WhatsApp.Proxy)

	c.Bridge.ListenAddr = strings.TrimSpace(c.Bridge.ListenAddr)
	c.Bridge.NotificationsPath = strings.TrimSpace(c.Bridge.NotificationsPath)
	c.Bridge.Secret = strings.TrimSpace(c.Bridge.Secret)
	c.Bridge.SecretEnv = strings.TrimSpace(c.Bridge.SecretEnv)
	if c.Bridge.ListenAddr == "" {
		c.Bridge.ListenAddr = "127.0.0.1:8791"
	}
	if c.Bridge.NotificationsPath == "" {
		c.Bridge.NotificationsPath = "/api/automation/notifications"
	}
	if c.Bridge.SecretEnv == "" {
		c.Bridge.SecretEnv = "ELEMENT_ORION_BRIDGE_NOTIFICATIONS_SECRET"
	}

	trimmedGuilds := make([]string, 0, len(c.Discord.AllowedGuildIDs))
	seenGuilds := make(map[string]struct{}, len(c.Discord.AllowedGuildIDs))
	for _, guildID := range c.Discord.AllowedGuildIDs {
		guildID = strings.TrimSpace(guildID)
		if guildID == "" {
			continue
		}
		if _, ok := seenGuilds[guildID]; ok {
			continue
		}
		seenGuilds[guildID] = struct{}{}
		trimmedGuilds = append(trimmedGuilds, guildID)
	}
	c.LLM.APIType = strings.TrimSpace(strings.ToLower(c.LLM.APIType))
	if c.LLM.APIType == "" {
		c.LLM.APIType = "openai"
	}
	c.LLM.APIKey = strings.TrimSpace(c.LLM.APIKey)
	c.LLM.Model = strings.TrimSpace(c.LLM.Model)
	for i := range c.LLM.Models {
		c.LLM.Models[i].Name = strings.TrimSpace(c.LLM.Models[i].Name)
		c.LLM.Models[i].Model = strings.TrimSpace(c.LLM.Models[i].Model)
		c.LLM.Models[i].BaseURL = strings.TrimSpace(c.LLM.Models[i].BaseURL)
		c.LLM.Models[i].APIKey = strings.TrimSpace(c.LLM.Models[i].APIKey)
		c.LLM.Models[i].APIKeyEnv = strings.TrimSpace(c.LLM.Models[i].APIKeyEnv)
	}
	c.LLM.Headers = normalizeStringMap(c.LLM.Headers)
	if c.LLM.ContextWindowTokens <= 0 {
		c.LLM.ContextWindowTokens = 24000
	}
	if c.LLM.RequestMaxAttempts <= 0 {
		c.LLM.RequestMaxAttempts = 3
	}
	c.LLM.RetryInitialBackoff = strings.TrimSpace(c.LLM.RetryInitialBackoff)
	if c.LLM.RetryInitialBackoff == "" {
		c.LLM.RetryInitialBackoff = "2s"
	}
	c.LLM.RetryMaxBackoff = strings.TrimSpace(c.LLM.RetryMaxBackoff)
	if c.LLM.RetryMaxBackoff == "" {
		c.LLM.RetryMaxBackoff = "8s"
	}
	c.Discord.BotToken = strings.TrimSpace(c.Discord.BotToken)
	c.Discord.BotTokenEnv = strings.TrimSpace(c.Discord.BotTokenEnv)
	c.Discord.UserToken = strings.TrimSpace(c.Discord.UserToken)
	c.Discord.TokenMode = strings.TrimSpace(strings.ToLower(c.Discord.TokenMode))
	if c.Discord.TokenMode == "" {
		c.Discord.TokenMode = "bot"
	}
	c.Discord.AllowedGuildIDs = trimmedGuilds
	c.Discord.AllowedDMUserIDs = uniqueTrimmedStrings(c.Discord.AllowedDMUserIDs)
	c.Discord.AllowedOutboundChannelIDs = uniqueTrimmedStrings(c.Discord.AllowedOutboundChannelIDs)
	c.Discord.GuildSessionScope = strings.TrimSpace(strings.ToLower(c.Discord.GuildSessionScope))
	if c.Discord.GuildSessionScope == "" {
		c.Discord.GuildSessionScope = "channel"
	}
	c.GIFs.Provider = strings.TrimSpace(strings.ToLower(c.GIFs.Provider))
	if c.GIFs.Provider == "" {
		c.GIFs.Provider = "giphy"
	}
	c.GIFs.APIKey = strings.TrimSpace(c.GIFs.APIKey)
	c.GIFs.APIKeyEnv = strings.TrimSpace(c.GIFs.APIKeyEnv)
	if c.GIFs.APIKeyEnv == "" {
		c.GIFs.APIKeyEnv = "GIPHY_API_KEY"
	}
	if c.GIFs.SearchLimit <= 0 {
		c.GIFs.SearchLimit = 8
	}
	c.GIFs.ContentFilter = strings.TrimSpace(strings.ToLower(c.GIFs.ContentFilter))
	if c.GIFs.ContentFilter == "" {
		c.GIFs.ContentFilter = "pg-13"
	}
	c.ImageGen.Model = strings.TrimSpace(c.ImageGen.Model)
	if c.ImageGen.Model == "" {
		c.ImageGen.Model = "cloudflare/@cf/black-forest-labs/flux-1-schnell"
	}
	c.ImageGen.OutputDir = strings.TrimSpace(c.ImageGen.OutputDir)
	if c.ImageGen.OutputDir == "" {
		c.ImageGen.OutputDir = ".element-orion/generated"
	}
	switch c.GIFs.ContentFilter {
	case "off":
		c.GIFs.ContentFilter = "r"
	case "low":
		c.GIFs.ContentFilter = "g"
	case "medium":
		c.GIFs.ContentFilter = "pg"
	case "high":
		c.GIFs.ContentFilter = "pg-13"
	}

	c.Heartbeat.Every = strings.TrimSpace(c.Heartbeat.Every)
	c.Heartbeat.Model = strings.TrimSpace(c.Heartbeat.Model)
	if c.Heartbeat.AckMaxChars <= 0 {
		c.Heartbeat.AckMaxChars = 300
	}
	if strings.TrimSpace(c.Heartbeat.EventPollInterval) == "" {
		c.Heartbeat.EventPollInterval = "5s"
	}
	c.Heartbeat.ActiveHours.Timezone = strings.TrimSpace(c.Heartbeat.ActiveHours.Timezone)
	c.Heartbeat.ActiveHours.Start = strings.TrimSpace(c.Heartbeat.ActiveHours.Start)
	c.Heartbeat.ActiveHours.End = strings.TrimSpace(c.Heartbeat.ActiveHours.End)
	c.DreamMode.Every = strings.TrimSpace(c.DreamMode.Every)
	if c.DreamMode.Every == "" {
		c.DreamMode.Every = "6h"
	}
	c.DreamMode.Model = strings.TrimSpace(c.DreamMode.Model)
	c.DreamMode.SleepHours.Timezone = strings.TrimSpace(c.DreamMode.SleepHours.Timezone)
	c.DreamMode.SleepHours.Start = strings.TrimSpace(c.DreamMode.SleepHours.Start)
	c.DreamMode.SleepHours.End = strings.TrimSpace(c.DreamMode.SleepHours.End)
	c.Heartbeat.Target.GuildID = strings.TrimSpace(c.Heartbeat.Target.GuildID)
	c.Heartbeat.Target.ChannelID = strings.TrimSpace(c.Heartbeat.Target.ChannelID)
	c.Heartbeat.Target.UserID = strings.TrimSpace(c.Heartbeat.Target.UserID)

	c.Persistence.DatabaseURL = strings.TrimSpace(c.Persistence.DatabaseURL)
	c.Persistence.DatabaseURLEnv = strings.TrimSpace(c.Persistence.DatabaseURLEnv)
	if c.Persistence.DatabaseURLEnv == "" {
		c.Persistence.DatabaseURLEnv = "DATABASE_URL"
	}
	c.Persistence.Interval = strings.TrimSpace(c.Persistence.Interval)
	c.Persistence.Exclude = uniqueTrimmedStrings(c.Persistence.Exclude)
	excludeSet := make(map[string]struct{}, len(c.Persistence.Exclude))
	for _, ex := range c.Persistence.Exclude {
		excludeSet[ex] = struct{}{}
	}
	for _, def := range DefaultPersistenceExclude {
		if _, ok := excludeSet[def]; !ok {
			c.Persistence.Exclude = append(c.Persistence.Exclude, def)
		}
	}

	c.EventWebhook.ListenAddr = strings.TrimSpace(c.EventWebhook.ListenAddr)
	c.EventWebhook.Path = strings.TrimSpace(c.EventWebhook.Path)
	c.EventWebhook.Secret = strings.TrimSpace(c.EventWebhook.Secret)
	c.EventWebhook.SecretEnv = strings.TrimSpace(c.EventWebhook.SecretEnv)
	c.EventWebhook.DefaultMode = strings.TrimSpace(strings.ToLower(c.EventWebhook.DefaultMode))
	if c.EventWebhook.ListenAddr == "" {
		c.EventWebhook.ListenAddr = "127.0.0.1:8787"
	}
	if c.EventWebhook.Path == "" {
		c.EventWebhook.Path = "/event"
	}
	if c.EventWebhook.SecretEnv == "" {
		c.EventWebhook.SecretEnv = "ELEMENT_ORION_EVENT_WEBHOOK_SECRET"
	}
	if c.EventWebhook.DefaultMode == "" {
		c.EventWebhook.DefaultMode = "now"
	}

	c.Dashboard.ListenAddr = strings.TrimSpace(c.Dashboard.ListenAddr)
	c.Dashboard.Path = strings.TrimSpace(c.Dashboard.Path)
	if c.Dashboard.ListenAddr == "" {
		c.Dashboard.ListenAddr = "127.0.0.1:8788"
	}
	if c.Dashboard.Path == "" {
		c.Dashboard.Path = "/dashboard"
	}

	if strings.TrimSpace(c.Skills.Load.UserDir) == "" {
		c.Skills.Load.UserDir = "~/.openclaw/skills"
	}
	resolvedSkillsUserDir, err := absOrHomeFromBase(configDir, c.Skills.Load.UserDir)
	if err != nil {
		return fmt.Errorf("resolve skills.load.user_dir: %w", err)
	}
	c.Skills.Load.UserDir = resolvedSkillsUserDir

	if strings.TrimSpace(c.Skills.Load.BundledDir) == "" {
		c.Skills.Load.BundledDir = "../skills/bundled"
	}
	resolvedBundledSkillsDir, err := absOrHomeFromBase(configDir, c.Skills.Load.BundledDir)
	if err != nil {
		return fmt.Errorf("resolve skills.load.bundled_dir: %w", err)
	}
	c.Skills.Load.BundledDir = resolvedBundledSkillsDir

	extraSkillDirs := make([]string, 0, len(c.Skills.Load.ExtraDirs))
	seenSkillDirs := make(map[string]struct{}, len(c.Skills.Load.ExtraDirs))
	for _, extraDir := range c.Skills.Load.ExtraDirs {
		resolvedExtraDir, err := absOrHomeFromBase(configDir, extraDir)
		if err != nil {
			return fmt.Errorf("resolve skills.load.extra_dirs entry %q: %w", extraDir, err)
		}
		if resolvedExtraDir == "" {
			continue
		}
		if _, seen := seenSkillDirs[resolvedExtraDir]; seen {
			continue
		}
		seenSkillDirs[resolvedExtraDir] = struct{}{}
		extraSkillDirs = append(extraSkillDirs, resolvedExtraDir)
	}
	c.Skills.Load.ExtraDirs = extraSkillDirs

	servers := make([]MCPServerConfig, 0, len(c.MCP.Servers))
	for _, server := range c.MCP.Servers {
		server.Name = strings.TrimSpace(server.Name)
		server.Transport = strings.TrimSpace(strings.ToLower(server.Transport))
		server.Command = strings.TrimSpace(server.Command)
		server.Endpoint = strings.TrimSpace(server.Endpoint)
		server.WorkingDir = strings.TrimSpace(server.WorkingDir)
		server.StartupTimeout = strings.TrimSpace(server.StartupTimeout)
		server.ToolTimeout = strings.TrimSpace(server.ToolTimeout)
		if server.Env == nil {
			server.Env = map[string]string{}
		}
		if server.Transport == "" {
			server.Transport = "stdio"
		}
		if server.StartupTimeout == "" {
			server.StartupTimeout = "30s"
		}
		if server.ToolTimeout == "" {
			server.ToolTimeout = "120s"
		}
		if server.WorkingDir != "" {
			resolved, err := absFromBase(configDir, server.WorkingDir)
			if err != nil {
				return fmt.Errorf("resolve mcp.servers[%s].working_dir: %w", server.Name, err)
			}
			server.WorkingDir = resolved
		}
		servers = append(servers, server)
	}
	c.MCP.Servers = servers
	c.LLM.ReasoningEffort = strings.TrimSpace(strings.ToLower(c.LLM.ReasoningEffort))
	c.LLM.MaxThinkingToken = strings.TrimSpace(strings.ToLower(c.LLM.MaxThinkingToken))

	return nil
}

func (c Config) validate() error {
	if !slices.Contains([]string{"openai", "codex", "deepseek"}, c.LLM.APIType) {
		return fmt.Errorf("llm.api_type must be one of openai, codex, or deepseek")
	}

	if strings.TrimSpace(c.LLM.BaseURL) == "" {
		return fmt.Errorf("llm.base_url must be set")
	}

	if strings.TrimSpace(c.LLM.Model) == "" {
		return fmt.Errorf("llm.model must be set")
	}
	if len(c.LLM.Models) > 0 {
		seenNames := map[string]bool{}
		anyEnabled := false
		for _, m := range c.LLM.Models {
			if strings.TrimSpace(m.Name) == "" {
				return fmt.Errorf("llm.models: every entry needs a name")
			}
			if strings.TrimSpace(m.Model) == "" {
				return fmt.Errorf("llm.models: entry %q needs a model", m.Name)
			}
			if seenNames[m.Name] {
				return fmt.Errorf("llm.models: duplicate name %q", m.Name)
			}
			seenNames[m.Name] = true
			if m.Enabled {
				anyEnabled = true
			}
		}
		if !anyEnabled {
			return fmt.Errorf("llm.models: at least one entry must be enabled")
		}
		if _, ok := c.LLM.ActiveModelEntry(); !ok {
			return fmt.Errorf("llm.model %q must match an enabled llm.models entry (by name or model id)", c.LLM.Model)
		}
	}
	if c.LLM.ReasoningEffort != "" && !slices.Contains([]string{"off", "none", "minimal", "low", "medium", "high", "xhigh"}, c.LLM.ReasoningEffort) {
		return fmt.Errorf("llm.reasoning_effort must be one of off, none, minimal, low, medium, high, or xhigh")
	}
	if c.LLM.MaxThinkingToken != "" && c.LLM.MaxThinkingToken != "off" {
		value, err := strconv.Atoi(c.LLM.MaxThinkingToken)
		if err != nil || value < 0 {
			return fmt.Errorf("llm.max_thinking_token must be off or a non-negative integer")
		}
	}

	if c.App.MaxAgentLoops <= 0 {
		return fmt.Errorf("app.max_agent_loops must be greater than zero")
	}

	if c.App.MaxToolCallsPerTurn <= 0 {
		return fmt.Errorf("app.max_tool_calls_per_turn must be greater than zero")
	}

	if c.Tools.MaxFileBytes <= 0 {
		return fmt.Errorf("tools.max_file_bytes must be greater than zero")
	}

	if c.Tools.MaxSearchResults <= 0 {
		return fmt.Errorf("tools.max_search_results must be greater than zero")
	}

	if c.Tools.MaxCommandOutputBytes <= 0 {
		return fmt.Errorf("tools.max_command_output_bytes must be greater than zero")
	}

	if c.App.HistoryCompaction.Enabled {
		if c.App.HistoryCompaction.TriggerTokens < 0 {
			return fmt.Errorf("app.history_compaction.trigger_tokens must not be negative")
		}
		if c.App.HistoryCompaction.TargetTokens < 0 {
			return fmt.Errorf("app.history_compaction.target_tokens must not be negative")
		}
		if c.App.HistoryCompaction.PreserveRecentMessages < 0 {
			return fmt.Errorf("app.history_compaction.preserve_recent_messages must not be negative")
		}
		if c.App.HistoryCompaction.TriggerTokens > 0 && c.App.HistoryCompaction.TargetTokens > 0 &&
			c.App.HistoryCompaction.TargetTokens >= c.App.HistoryCompaction.TriggerTokens {
			return fmt.Errorf("app.history_compaction.target_tokens must be smaller than app.history_compaction.trigger_tokens")
		}
	}

	if !slices.Contains([]string{"bot", "user"}, c.Discord.TokenMode) {
		return fmt.Errorf("discord.token_mode must be one of bot or user")
	}

	switch c.Discord.TokenMode {
	case "bot":
		if c.Discord.BotToken == "" && c.Discord.BotTokenEnv == "" {
			return fmt.Errorf("discord.bot_token must be set when discord.token_mode is bot")
		}
	case "user":
		if c.Discord.UserToken == "" {
			return fmt.Errorf("discord.user_token must be set when discord.token_mode is user")
		}
	}

	if !c.Discord.AllowDirectMessages && !c.Discord.AllowGroupDirectMessages && len(c.Discord.AllowedGuildIDs) == 0 {
		return fmt.Errorf("configure at least one discord.allowed_guild_ids entry or enable discord.allow_direct_messages or discord.allow_group_direct_messages")
	}

	if !slices.Contains([]string{"channel", "user"}, c.Discord.GuildSessionScope) {
		return fmt.Errorf("discord.guild_session_scope must be one of channel or user")
	}
	if err := validateOptionalDirectoryPath(c.Discord.IncomingAttachmentsDir, "discord.incoming_attachments_dir"); err != nil {
		return err
	}
	if !slices.Contains([]string{"giphy"}, c.GIFs.Provider) {
		return fmt.Errorf("gifs.provider must be one of giphy")
	}
	if c.GIFs.SearchLimit <= 0 {
		return fmt.Errorf("gifs.search_limit must be greater than zero")
	}
	if !slices.Contains([]string{"g", "pg", "pg-13", "r"}, c.GIFs.ContentFilter) {
		return fmt.Errorf("gifs.content_filter must be one of g, pg, pg-13, or r")
	}

	if err := validateDirectoryPath(c.App.WorkspaceRoot, "app.workspace_root"); err != nil {
		return err
	}

	if err := validateOptionalDirectoryPath(c.App.MemoryDir, "app.memory_dir"); err != nil {
		return err
	}

	if _, err := time.ParseDuration(c.LLM.Timeout); err != nil {
		return fmt.Errorf("parse llm.timeout: %w", err)
	}
	if c.LLM.RequestMaxAttempts <= 0 {
		return fmt.Errorf("llm.request_max_attempts must be greater than zero")
	}
	if _, err := time.ParseDuration(c.LLM.RetryInitialBackoff); err != nil {
		return fmt.Errorf("parse llm.retry_initial_backoff: %w", err)
	}
	if _, err := time.ParseDuration(c.LLM.RetryMaxBackoff); err != nil {
		return fmt.Errorf("parse llm.retry_max_backoff: %w", err)
	}

	if c.LLM.ContextWindowTokens <= 0 {
		return fmt.Errorf("llm.context_window_tokens must be greater than zero")
	}

	if c.LLM.MaxTokens <= 0 {
		return fmt.Errorf("llm.max_tokens must be greater than zero")
	}

	if c.LLM.ContextWindowTokens <= c.LLM.MaxTokens {
		return fmt.Errorf("llm.context_window_tokens must be greater than llm.max_tokens")
	}

	if _, err := time.ParseDuration(c.Tools.ExecTimeout); err != nil {
		return fmt.Errorf("parse tools.exec_timeout: %w", err)
	}
	if strings.TrimSpace(c.BackgroundTasks.DefaultMinRuntime) != "" {
		if _, err := time.ParseDuration(c.BackgroundTasks.DefaultMinRuntime); err != nil {
			return fmt.Errorf("parse background_tasks.default_min_runtime: %w", err)
		}
	}
	if c.BackgroundTasks.MaxEventLogEntries <= 0 {
		return fmt.Errorf("background_tasks.max_event_log_entries must be greater than zero")
	}
	if c.BackgroundTasks.Sandbox.Enabled {
		if !slices.Contains([]string{"nspawn"}, c.BackgroundTasks.Sandbox.Provider) {
			return fmt.Errorf("background_tasks.sandbox.provider must be one of nspawn")
		}
		if err := validateDirectoryPath(c.BackgroundTasks.Sandbox.MachinesDir, "background_tasks.sandbox.machines_dir"); err != nil {
			return err
		}
		if _, err := time.ParseDuration(c.BackgroundTasks.Sandbox.SetupTimeout); err != nil {
			return fmt.Errorf("parse background_tasks.sandbox.setup_timeout: %w", err)
		}
	}

	if strings.TrimSpace(c.Heartbeat.Every) != "" {
		if _, err := time.ParseDuration(c.Heartbeat.Every); err != nil {
			return fmt.Errorf("parse heartbeat.every: %w", err)
		}
	}

	if _, err := time.ParseDuration(c.Heartbeat.EventPollInterval); err != nil {
		return fmt.Errorf("parse heartbeat.event_poll_interval: %w", err)
	}

	if c.Heartbeat.AckMaxChars <= 0 {
		return fmt.Errorf("heartbeat.ack_max_chars must be greater than zero")
	}

	if (c.Heartbeat.ActiveHours.Start == "") != (c.Heartbeat.ActiveHours.End == "") {
		return fmt.Errorf("heartbeat.active_hours.start and heartbeat.active_hours.end must be set together")
	}
	if c.Heartbeat.ActiveHours.Start != "" {
		if _, err := parseClockHHMM(c.Heartbeat.ActiveHours.Start); err != nil {
			return fmt.Errorf("parse heartbeat.active_hours.start: %w", err)
		}
		if _, err := parseClockHHMM(c.Heartbeat.ActiveHours.End); err != nil {
			return fmt.Errorf("parse heartbeat.active_hours.end: %w", err)
		}
		if c.Heartbeat.ActiveHours.Timezone != "" {
			if _, err := time.LoadLocation(c.Heartbeat.ActiveHours.Timezone); err != nil {
				return fmt.Errorf("load heartbeat.active_hours.timezone: %w", err)
			}
		}
	}

	if c.DreamMode.Enabled {
		if _, err := time.ParseDuration(c.DreamMode.Every); err != nil {
			return fmt.Errorf("parse dream_mode.every: %w", err)
		}
		if (c.DreamMode.SleepHours.Start == "") != (c.DreamMode.SleepHours.End == "") {
			return fmt.Errorf("dream_mode.sleep_hours.start and dream_mode.sleep_hours.end must be set together")
		}
		if c.DreamMode.SleepHours.Start == "" {
			return fmt.Errorf("dream_mode.sleep_hours.start and dream_mode.sleep_hours.end must be set when dream_mode.enabled is true")
		}
		if _, err := parseClockHHMM(c.DreamMode.SleepHours.Start); err != nil {
			return fmt.Errorf("parse dream_mode.sleep_hours.start: %w", err)
		}
		if _, err := parseClockHHMM(c.DreamMode.SleepHours.End); err != nil {
			return fmt.Errorf("parse dream_mode.sleep_hours.end: %w", err)
		}
		if c.DreamMode.SleepHours.Timezone != "" {
			if _, err := time.LoadLocation(c.DreamMode.SleepHours.Timezone); err != nil {
				return fmt.Errorf("load dream_mode.sleep_hours.timezone: %w", err)
			}
		}
	}

	if c.EventWebhook.Enabled {
		if strings.TrimSpace(c.EventWebhook.ListenAddr) == "" {
			return fmt.Errorf("event_webhook.listen_addr must be set when event_webhook.enabled is true")
		}
		if !strings.HasPrefix(c.EventWebhook.Path, "/") {
			return fmt.Errorf("event_webhook.path must start with '/'")
		}
		if !isValidHeartbeatMode(c.EventWebhook.DefaultMode) {
			return fmt.Errorf("event_webhook.default_mode must be one of now or next-heartbeat")
		}
		if !c.HeartbeatEnabled() {
			return fmt.Errorf("event_webhook.enabled requires heartbeat target and schedule configuration")
		}
	}

	if c.Messenger.Enabled {
		if strings.TrimSpace(c.Messenger.CookiesPath) == "" {
			return fmt.Errorf("messenger.cookies_path must be set when messenger.enabled is true")
		}
	}

	if c.Bridge.Enabled {
		if strings.TrimSpace(c.Bridge.ListenAddr) == "" {
			return fmt.Errorf("bridge.listen_addr must be set when bridge.enabled is true")
		}
		if !strings.HasPrefix(c.Bridge.NotificationsPath, "/") {
			return fmt.Errorf("bridge.notifications_path must start with '/'")
		}
	}

	if c.Persistence.Enabled {
		if _, err := c.Persistence.ResolvePersistenceDatabaseURL(); err != nil {
			return err
		}
		if strings.TrimSpace(c.Persistence.Interval) != "" {
			if _, err := time.ParseDuration(strings.TrimSpace(c.Persistence.Interval)); err != nil {
				return fmt.Errorf("parse persistence.interval: %w", err)
			}
		}
	}

	if c.Dashboard.Enabled {
		if strings.TrimSpace(c.Dashboard.ListenAddr) == "" {
			return fmt.Errorf("dashboard.listen_addr must be set when dashboard.enabled is true")
		}
		if !strings.HasPrefix(c.Dashboard.Path, "/") {
			return fmt.Errorf("dashboard.path must start with '/'")
		}
	}

	for index, server := range c.MCP.Servers {
		if !server.Enabled {
			continue
		}
		label := fmt.Sprintf("mcp.servers[%d]", index)
		if server.Name == "" {
			return fmt.Errorf("%s.name must be set when the server is enabled", label)
		}
		switch server.Transport {
		case "stdio":
			if server.Command == "" {
				return fmt.Errorf("%s.command must be set for stdio transport", label)
			}
		case "http", "streamable_http":
			if server.Endpoint == "" {
				return fmt.Errorf("%s.endpoint must be set for HTTP transport", label)
			}
		default:
			return fmt.Errorf("%s.transport must be one of stdio, http, or streamable_http", label)
		}
		if server.WorkingDir != "" {
			if err := validateDirectoryPath(server.WorkingDir, label+".working_dir"); err != nil {
				return err
			}
		}
		if _, err := time.ParseDuration(server.StartupTimeout); err != nil {
			return fmt.Errorf("parse %s.startup_timeout: %w", label, err)
		}
		if _, err := time.ParseDuration(server.ToolTimeout); err != nil {
			return fmt.Errorf("parse %s.tool_timeout: %w", label, err)
		}
	}

	if err := validateOptionalDirectoryPath(c.Skills.Load.UserDir, "skills.load.user_dir"); err != nil {
		return err
	}

	if err := validateOptionalDirectoryPath(c.Skills.Load.BundledDir, "skills.load.bundled_dir"); err != nil {
		return err
	}

	for index, extraDir := range c.Skills.Load.ExtraDirs {
		field := fmt.Sprintf("skills.load.extra_dirs[%d]", index)
		if err := validateOptionalDirectoryPath(extraDir, field); err != nil {
			return err
		}
	}

	return nil
}

func (c Config) ResolveAPIKey() (string, error) {
	if c.LLM.APIKey != "" {
		return c.LLM.APIKey, nil
	}

	envName := strings.TrimSpace(c.LLM.APIKeyEnv)
	if envName == "" {
		return "", fmt.Errorf("set llm.api_key in config or llm.api_key_env pointing to an environment variable")
	}

	value := strings.TrimSpace(os.Getenv(envName))
	if value == "" {
		return "", fmt.Errorf("environment variable %q is empty; set llm.api_key or export the variable", envName)
	}

	return value, nil
}

func (c Config) ResolveEventWebhookSecret() (string, error) {
	if strings.TrimSpace(c.EventWebhook.Secret) != "" {
		return c.EventWebhook.Secret, nil
	}

	envName := strings.TrimSpace(c.EventWebhook.SecretEnv)
	if envName == "" {
		return "", nil
	}

	return strings.TrimSpace(os.Getenv(envName)), nil
}

func (c Config) ResolveBridgeNotificationsSecret() (string, error) {
	if strings.TrimSpace(c.Bridge.Secret) != "" {
		return c.Bridge.Secret, nil
	}

	envName := strings.TrimSpace(c.Bridge.SecretEnv)
	if envName == "" {
		return "", nil
	}

	return strings.TrimSpace(os.Getenv(envName)), nil
}

func (c Config) WhatsAppDatabaseURL() string {
	if strings.TrimSpace(c.WhatsApp.DatabaseURL) != "" {
		return strings.TrimSpace(c.WhatsApp.DatabaseURL)
	}
	return strings.TrimSpace(os.Getenv("DATABASE_URL"))
}

func (c Config) MessengerThreadAllowed(threadID string) bool {
	if len(c.Messenger.AllowedThreadIDs) == 0 {
		return true
	}
	for _, id := range c.Messenger.AllowedThreadIDs {
		if id == threadID {
			return true
		}
	}
	return false
}

func (c Config) ResolveGIFAPIKey() (string, error) {
	if strings.TrimSpace(c.GIFs.APIKey) != "" {
		return strings.TrimSpace(c.GIFs.APIKey), nil
	}

	envName := strings.TrimSpace(c.GIFs.APIKeyEnv)
	if envName == "" {
		return "", fmt.Errorf("set gifs.api_key in config or gifs.api_key_env pointing to an environment variable")
	}

	value := strings.TrimSpace(os.Getenv(envName))
	if value == "" {
		return "", fmt.Errorf("environment variable %q is empty; set gifs.api_key or export the variable", envName)
	}

	return value, nil
}

func (c Config) DiscordUsesBotToken() bool {
	return strings.TrimSpace(strings.ToLower(c.Discord.TokenMode)) != "user"
}

func (c Config) ResolveDiscordGatewayToken() (string, error) {
	if c.DiscordUsesBotToken() {
		token := strings.TrimSpace(c.Discord.BotToken)
		if token == "" && c.Discord.BotTokenEnv != "" {
			token = strings.TrimSpace(os.Getenv(c.Discord.BotTokenEnv))
		}
		if token == "" {
			return "", fmt.Errorf("discord.bot_token is not configured")
		}
		return "Bot " + token, nil
	}

	if strings.TrimSpace(c.Discord.UserToken) == "" {
		return "", fmt.Errorf("discord.user_token is not configured")
	}
	return strings.TrimSpace(c.Discord.UserToken), nil
}

func (c Config) ResolveDiscordAuthorizationHeader() (string, error) {
	return c.ResolveDiscordGatewayToken()
}

func (c Config) SupportsDiscordApplicationCommands() bool {
	return c.DiscordUsesBotToken()
}

func (c Config) LLMTimeout() time.Duration {
	timeout, err := time.ParseDuration(c.LLM.Timeout)
	if err != nil || timeout <= 0 {
		return 180 * time.Second
	}
	return timeout
}

func (c Config) LLMRetryInitialBackoff() time.Duration {
	backoff, err := time.ParseDuration(c.LLM.RetryInitialBackoff)
	if err != nil || backoff <= 0 {
		return 2 * time.Second
	}
	return backoff
}

func (c Config) LLMRetryMaxBackoff() time.Duration {
	backoff, err := time.ParseDuration(c.LLM.RetryMaxBackoff)
	if err != nil || backoff <= 0 {
		return 8 * time.Second
	}
	return backoff
}

func (c Config) LLMInputTokenBudget() int {
	if c.LLM.ContextWindowTokens <= 0 {
		return 0
	}

	budget := c.LLM.ContextWindowTokens - c.LLM.MaxTokens
	if budget <= 0 {
		return c.LLM.ContextWindowTokens
	}

	return budget
}

func (c Config) HistoryCompactionTriggerTokens() int {
	if !c.App.HistoryCompaction.Enabled {
		return 0
	}
	if c.App.HistoryCompaction.TriggerTokens > 0 {
		return c.App.HistoryCompaction.TriggerTokens
	}
	budget := c.LLMInputTokenBudget()
	if budget <= 0 {
		return 0
	}
	trigger := (budget * 3) / 4
	if trigger <= 0 {
		return budget
	}
	return trigger
}

func (c Config) HistoryCompactionTargetTokens() int {
	if !c.App.HistoryCompaction.Enabled {
		return 0
	}
	if c.App.HistoryCompaction.TargetTokens > 0 {
		return c.App.HistoryCompaction.TargetTokens
	}
	trigger := c.HistoryCompactionTriggerTokens()
	if trigger <= 0 {
		return 0
	}
	target := (trigger * 2) / 3
	if target <= 0 {
		return trigger / 2
	}
	return target
}

func (c Config) HistoryCompactionPreserveRecentMessages() int {
	if !c.App.HistoryCompaction.Enabled {
		return 0
	}
	if c.App.HistoryCompaction.PreserveRecentMessages > 0 {
		return c.App.HistoryCompaction.PreserveRecentMessages
	}
	return 12
}

func (c Config) ExecTimeout() time.Duration {
	timeout, err := time.ParseDuration(c.Tools.ExecTimeout)
	if err != nil || timeout <= 0 {
		return 120 * time.Second
	}
	return timeout
}

func (c Config) BackgroundTaskDefaultMinRuntime() time.Duration {
	value := strings.TrimSpace(c.BackgroundTasks.DefaultMinRuntime)
	if value == "" {
		return 0
	}
	runtime, err := time.ParseDuration(value)
	if err != nil || runtime <= 0 {
		return 0
	}
	return runtime
}

func (c Config) BackgroundTaskMaxEventLogEntries() int {
	if c.BackgroundTasks.MaxEventLogEntries > 0 {
		return c.BackgroundTasks.MaxEventLogEntries
	}
	return 200
}

func (c Config) BackgroundTaskSandboxSetupTimeout() time.Duration {
	timeout, err := time.ParseDuration(c.BackgroundTasks.Sandbox.SetupTimeout)
	if err != nil || timeout <= 0 {
		return 20 * time.Minute
	}
	return timeout
}

func (c Config) HeartbeatInterval() time.Duration {
	interval, err := time.ParseDuration(c.Heartbeat.Every)
	if err != nil || interval <= 0 {
		return 30 * time.Minute
	}
	return interval
}

func (c Config) HeartbeatEventPollInterval() time.Duration {
	interval, err := time.ParseDuration(c.Heartbeat.EventPollInterval)
	if err != nil || interval <= 0 {
		return 5 * time.Second
	}
	return interval
}

func (c Config) HeartbeatModel() string {
	if strings.TrimSpace(c.Heartbeat.Model) != "" {
		return c.Heartbeat.Model
	}
	return c.ResolveLLMModel()
}

// ActiveModelEntry returns the enabled llm.models entry that llm.model refers
// to (by name or by model id). Only meaningful when the models catalog is
// configured; returns ok=false when llm.model doesn't match an enabled entry.
func (l LLMConfig) ActiveModelEntry() (LLMModelEntry, bool) {
	if len(l.Models) == 0 {
		return LLMModelEntry{Name: l.Model, Model: l.Model, Enabled: true}, true
	}
	target := strings.TrimSpace(l.Model)
	for i := range l.Models {
		m := l.Models[i]
		if !m.Enabled {
			continue
		}
		if m.Name == target || m.Model == target {
			return m, true
		}
	}
	return LLMModelEntry{}, false
}

// ActiveModelProvider returns the base URL + resolved API key for the active
// catalog entry, falling back to the top-level llm settings when the entry
// doesn't override them.
func (l LLMConfig) ActiveModelProvider() (baseURL string, apiKey string, err error) {
	entry, ok := l.ActiveModelEntry()
	if !ok {
		return l.BaseURL, l.APIKey, nil
	}
	baseURL = entry.BaseURL
	if strings.TrimSpace(baseURL) == "" {
		baseURL = l.BaseURL
	}
	apiKey = entry.APIKey
	envName := strings.TrimSpace(entry.APIKeyEnv)
	if envName == "" {
		envName = strings.TrimSpace(l.APIKeyEnv)
	}
	if apiKey != "" {
		return baseURL, apiKey, nil
	}
	if envName != "" && strings.TrimSpace(os.Getenv(envName)) != "" {
		return baseURL, strings.TrimSpace(os.Getenv(envName)), nil
	}
	if strings.TrimSpace(l.APIKey) != "" {
		return baseURL, l.APIKey, nil
	}
	if l.APIKeyEnv == "" {
		return baseURL, "", nil
	}
	return "", "", fmt.Errorf("environment variable %q is empty; set llm.api_key or export the variable", envName)
}

// ResolveLLMModel returns the active model id: the single configured
// llm.model when no catalog is present, or the catalog entry's full model id
// (falling back to llm.model itself if it's not in the catalog, which
// validation normally prevents). Users cannot select models; admins pick via
// llm.model and toggle entries in llm.models.
func (c Config) ResolveLLMModel() string {
	if entry, ok := c.LLM.ActiveModelEntry(); ok && entry.Model != "" {
		return entry.Model
	}
	return c.LLM.Model
}

func (c Config) HeartbeatEventsDir() string {
	return filepath.Join(c.App.SessionDir, "heartbeat-events")
}

func (c Config) HeartbeatStatePath() string {
	return filepath.Join(c.App.SessionDir, "heartbeat-state.json")
}

func (c Config) LogDir() string {
	return filepath.Join(c.App.SessionDir, "logs")
}

func (c Config) MCPServerStartupTimeout(server MCPServerConfig) time.Duration {
	timeout, err := time.ParseDuration(server.StartupTimeout)
	if err != nil || timeout <= 0 {
		return 30 * time.Second
	}
	return timeout
}

func (c Config) MCPServerToolTimeout(server MCPServerConfig) time.Duration {
	timeout, err := time.ParseDuration(server.ToolTimeout)
	if err != nil || timeout <= 0 {
		return 120 * time.Second
	}
	return timeout
}

func (c Config) HeartbeatLocation() (*time.Location, error) {
	if strings.TrimSpace(c.Heartbeat.ActiveHours.Timezone) == "" {
		return time.Local, nil
	}
	return time.LoadLocation(c.Heartbeat.ActiveHours.Timezone)
}

func (c Config) HeartbeatEnabled() bool {
	return strings.TrimSpace(c.Heartbeat.Every) != "" &&
		strings.TrimSpace(c.Heartbeat.Target.ChannelID) != "" &&
		strings.TrimSpace(c.Heartbeat.Target.UserID) != ""
}

func (c Config) DreamModeEnabled() bool {
	return c.DreamMode.Enabled && strings.TrimSpace(c.DreamMode.Every) != ""
}

func (c Config) DreamModeInterval() time.Duration {
	interval, err := time.ParseDuration(c.DreamMode.Every)
	if err != nil || interval <= 0 {
		return 6 * time.Hour
	}
	return interval
}

func (c Config) DreamModeModel() string {
	if strings.TrimSpace(c.DreamMode.Model) != "" {
		return c.DreamMode.Model
	}
	return c.ResolveLLMModel()
}

func (c Config) DreamModeLocation() (*time.Location, error) {
	if strings.TrimSpace(c.DreamMode.SleepHours.Timezone) == "" {
		return time.Local, nil
	}
	return time.LoadLocation(c.DreamMode.SleepHours.Timezone)
}

func (c Config) HeartbeatHasAnyDelivery() bool {
	return c.Heartbeat.ShowOK || c.Heartbeat.ShowAlerts || c.Heartbeat.UseIndicator
}

func (c Config) ToolEnabled(name string) bool {
	if len(c.Tools.Enabled) == 0 {
		return true
	}
	return slices.Contains(c.Tools.Enabled, name)
}

func (c Config) SharedGuildSessions() bool {
	return strings.EqualFold(strings.TrimSpace(c.Discord.GuildSessionScope), "channel")
}

func (c Config) DMAllowedForUser(userID string) bool {
	if !c.Discord.AllowDirectMessages {
		return false
	}

	userID = strings.TrimSpace(userID)
	if userID == "" {
		return false
	}

	if len(c.Discord.AllowedDMUserIDs) == 0 {
		return true
	}

	return slices.Contains(c.Discord.AllowedDMUserIDs, userID)
}

func (c Config) DiscordChannelAllowed(targetChannelID string, activeChannelID string) bool {
	targetChannelID = strings.TrimSpace(targetChannelID)
	activeChannelID = strings.TrimSpace(activeChannelID)
	if targetChannelID == "" {
		return false
	}
	if activeChannelID != "" && targetChannelID == activeChannelID {
		return true
	}
	return slices.Contains(c.Discord.AllowedOutboundChannelIDs, targetChannelID)
}

func (c *Config) OverrideWorkspaceRoot(path string) error {
	if strings.TrimSpace(path) == "" {
		return nil
	}

	previousWorkspaceRoot := strings.TrimSpace(c.App.WorkspaceRoot)
	previousSessionDir := strings.TrimSpace(c.App.SessionDir)
	previousMemoryDir := strings.TrimSpace(c.App.MemoryDir)

	workingDir, err := os.Getwd()
	if err != nil {
		return fmt.Errorf("get working directory: %w", err)
	}

	resolved, err := absFromBase(workingDir, path)
	if err != nil {
		return fmt.Errorf("resolve workspace override: %w", err)
	}

	if err := validateDirectoryPath(resolved, "workspace override"); err != nil {
		return err
	}

	c.App.WorkspaceRoot = resolved
	if previousWorkspaceRoot != "" && sameCleanPath(previousSessionDir, filepath.Join(previousWorkspaceRoot, ".element-orion")) {
		c.App.SessionDir = filepath.Join(resolved, ".element-orion")
	}
	if previousMemoryDir == "" || sameCleanPath(previousMemoryDir, filepath.Join(previousSessionDir, "memory")) {
		c.App.MemoryDir = filepath.Join(c.App.SessionDir, "memory")
	}
	return nil
}

func sameCleanPath(left string, right string) bool {
	trimmedLeft := strings.TrimSpace(left)
	trimmedRight := strings.TrimSpace(right)
	if trimmedLeft == "" || trimmedRight == "" {
		return trimmedLeft == trimmedRight
	}
	return filepath.Clean(trimmedLeft) == filepath.Clean(trimmedRight)
}

func validateDirectoryPath(path string, fieldName string) error {
	info, err := os.Stat(path)
	if err != nil {
		return fmt.Errorf("stat %s: %w", fieldName, err)
	}

	if !info.IsDir() {
		return fmt.Errorf("%s must point to a directory", fieldName)
	}

	return nil
}

func validateOptionalDirectoryPath(path string, fieldName string) error {
	trimmed := strings.TrimSpace(path)
	if trimmed == "" {
		return nil
	}

	info, err := os.Stat(trimmed)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return fmt.Errorf("stat %s: %w", fieldName, err)
	}

	if !info.IsDir() {
		return fmt.Errorf("%s must point to a directory when it exists", fieldName)
	}

	return nil
}

func parseClockHHMM(value string) (int, error) {
	parsed, err := time.Parse("15:04", value)
	if err != nil {
		return 0, err
	}
	return parsed.Hour()*60 + parsed.Minute(), nil
}

func isValidHeartbeatMode(mode string) bool {
	trimmed := strings.TrimSpace(strings.ToLower(mode))
	return trimmed == "now" || trimmed == "next-heartbeat"
}

func absFromBase(base string, candidate string) (string, error) {
	if strings.TrimSpace(candidate) == "" {
		candidate = "."
	}

	if filepath.IsAbs(candidate) {
		return filepath.Clean(candidate), nil
	}

	return filepath.Abs(filepath.Join(base, candidate))
}

func absOrHomeFromBase(base string, candidate string) (string, error) {
	trimmed := strings.TrimSpace(candidate)
	if trimmed == "" {
		return "", nil
	}

	expanded, err := expandHome(trimmed)
	if err != nil {
		return "", err
	}

	if filepath.IsAbs(expanded) {
		return filepath.Clean(expanded), nil
	}

	return filepath.Abs(filepath.Join(base, expanded))
}

func expandHome(path string) (string, error) {
	if path == "~" {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", fmt.Errorf("resolve home directory: %w", err)
		}
		return home, nil
	}

	if !strings.HasPrefix(path, "~/") {
		return path, nil
	}

	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("resolve home directory: %w", err)
	}

	return filepath.Join(home, strings.TrimPrefix(path, "~/")), nil
}

func uniqueTrimmedStrings(values []string) []string {
	result := make([]string, 0, len(values))
	seen := make(map[string]struct{}, len(values))
	for _, value := range values {
		trimmed := strings.TrimSpace(value)
		if trimmed == "" {
			continue
		}
		if _, ok := seen[trimmed]; ok {
			continue
		}
		seen[trimmed] = struct{}{}
		result = append(result, trimmed)
	}
	return result
}

func normalizeStringMap(values map[string]string) map[string]string {
	if len(values) == 0 {
		return map[string]string{}
	}

	result := make(map[string]string, len(values))
	for key, value := range values {
		trimmedKey := strings.TrimSpace(key)
		if trimmedKey == "" {
			continue
		}
		result[trimmedKey] = strings.TrimSpace(value)
	}
	return result
}
