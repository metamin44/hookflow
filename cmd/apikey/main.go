// Command apikey is the operator CLI for HookFlow API keys. It creates, lists
// and revokes machine credentials. A raw key is printed exactly once, by the
// create command, and never persisted by this program.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/metamin44/hookflow/internal/app/accesskeys"
	"github.com/metamin44/hookflow/internal/platform/config"
	"github.com/metamin44/hookflow/internal/platform/database"
	"github.com/metamin44/hookflow/internal/platform/secrets"
)

const usage = `usage: apikey <command> [flags]

commands:
  create  -organization <uuid> -name <name> [-scopes <csv>] [-expires <duration>]
  list    -organization <uuid>
  revoke  -organization <uuid> -id <uuid>

scopes: events:write, endpoints:read, endpoints:write, endpoints:secrets:rotate, billing:read, billing:write, deliveries:read
`

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	if err := run(ctx, os.Args[1:], os.Stdout, os.Stderr, defaultDependencies()); err != nil {
		fmt.Fprintf(os.Stderr, "apikey: %v\n", err)
		os.Exit(1)
	}
}

type dependencies struct {
	loadConfig func() (config.Config, error)
	newPool    func(context.Context, config.Database) (*database.Pool, error)
	random     io.Reader
	now        func() time.Time
}

func defaultDependencies() dependencies {
	return dependencies{
		loadConfig: config.Load,
		newPool:    database.NewPool,
		random:     secrets.DefaultReader(),
		now:        time.Now,
	}
}

func run(ctx context.Context, args []string, stdout, stderr io.Writer, deps dependencies) error {
	if len(args) == 0 {
		if _, err := fmt.Fprint(stderr, usage); err != nil {
			return err
		}
		return errors.New("a command is required")
	}
	command := args[0]
	switch command {
	case "create", "list", "revoke":
	default:
		if _, err := fmt.Fprint(stderr, usage); err != nil {
			return err
		}
		return fmt.Errorf("unknown command %q", command)
	}

	cfg, err := deps.loadConfig()
	if err != nil {
		return fmt.Errorf("load database configuration: %w", err)
	}
	pool, err := deps.newPool(ctx, cfg.Database)
	if err != nil {
		return fmt.Errorf("initialize PostgreSQL: %w", err)
	}
	defer pool.Close()

	service, err := accesskeys.NewService(pool.Raw(), deps.random)
	if err != nil {
		return err
	}
	switch command {
	case "create":
		return runCreate(ctx, args[1:], stdout, stderr, service, deps.now)
	case "list":
		return runList(ctx, args[1:], stdout, stderr, service)
	default:
		return runRevoke(ctx, args[1:], stdout, stderr, service)
	}
}

func runCreate(ctx context.Context, args []string, stdout, stderr io.Writer, service *accesskeys.Service, now func() time.Time) error {
	flags := flag.NewFlagSet("create", flag.ContinueOnError)
	flags.SetOutput(stderr)
	organization := flags.String("organization", "", "organization UUID")
	name := flags.String("name", "", "operator-visible key name")
	scopes := flags.String("scopes", "", "comma-separated scopes")
	expires := flags.Duration("expires", 0, "optional lifetime, for example 720h")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if *organization == "" || *name == "" {
		return errors.New("create requires -organization and -name")
	}
	if *expires < 0 {
		return errors.New("-expires must not be negative")
	}
	var expiresAt *time.Time
	if *expires > 0 {
		value := now().UTC().Add(*expires)
		expiresAt = &value
	}
	token, record, err := service.Create(ctx, *organization, *name, splitScopes(*scopes), expiresAt)
	if err != nil {
		return err
	}
	lines := []struct {
		format string
		value  string
	}{
		{"id: %s\n", record.ID},
		{"name: %s\n", record.Name},
		{"prefix: %s\n", record.KeyPrefix},
		{"scopes: %s\n", strings.Join(record.Scopes, ",")},
		{"expires_at: %s\n", formatTime(record.ExpiresAt)},
		{"api_key: %s\n", token},
	}
	for _, line := range lines {
		if _, err := fmt.Fprintf(stdout, line.format, line.value); err != nil {
			return err
		}
	}
	return nil
}

func runList(ctx context.Context, args []string, stdout, stderr io.Writer, service *accesskeys.Service) error {
	flags := flag.NewFlagSet("list", flag.ContinueOnError)
	flags.SetOutput(stderr)
	organization := flags.String("organization", "", "organization UUID")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if *organization == "" {
		return errors.New("list requires -organization")
	}
	records, err := service.List(ctx, *organization)
	if err != nil {
		return err
	}
	for _, record := range records {
		if _, err := fmt.Fprintf(stdout, "%s\t%s\t%s\t%s\t%s\t%s\t%s\n",
			record.ID, record.Name, record.KeyPrefix, strings.Join(record.Scopes, ","),
			record.CreatedAt.UTC().Format(time.RFC3339), formatTime(record.ExpiresAt), formatTime(record.RevokedAt)); err != nil {
			return err
		}
	}
	return nil
}

func runRevoke(ctx context.Context, args []string, stdout, stderr io.Writer, service *accesskeys.Service) error {
	flags := flag.NewFlagSet("revoke", flag.ContinueOnError)
	flags.SetOutput(stderr)
	organization := flags.String("organization", "", "organization UUID")
	keyID := flags.String("id", "", "api key UUID")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if *organization == "" || *keyID == "" {
		return errors.New("revoke requires -organization and -id")
	}
	_, changed, err := service.Revoke(ctx, *organization, *keyID)
	if err != nil {
		return err
	}
	if changed {
		if _, err := fmt.Fprintf(stdout, "revoked api key %s\n", *keyID); err != nil {
			return err
		}
		return nil
	}
	if _, err := fmt.Fprintf(stdout, "api key %s was already revoked\n", *keyID); err != nil {
		return err
	}
	return nil
}

func splitScopes(value string) []string {
	if strings.TrimSpace(value) == "" {
		return nil
	}
	parts := strings.Split(value, ",")
	scopes := make([]string, 0, len(parts))
	for _, part := range parts {
		if trimmed := strings.TrimSpace(part); trimmed != "" {
			scopes = append(scopes, trimmed)
		}
	}
	return scopes
}

func formatTime(value *time.Time) string {
	if value == nil {
		return "never"
	}
	return value.UTC().Format(time.RFC3339)
}
