// Command reaper deletes DigitalOcean droplets tagged "forge-e2e" older than a
// threshold. The two-hour default exceeds the longest 115-minute Forge workflow
// bound, so an active run cannot be reaped before its owner cleanup can finish.
// It is the safety net for runs that crash or are cancelled. Run on a cron in CI.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"os"
	"time"

	"github.com/digitalocean/godo"
)

const defaultMaxAge = 2 * time.Hour

type dropletClient interface {
	ListByTag(context.Context, string, *godo.ListOptions) ([]godo.Droplet, *godo.Response, error)
	Delete(context.Context, int) (*godo.Response, error)
}

func main() {
	maxAge := flag.Duration("max-age", defaultMaxAge, "delete droplets older than this")
	flag.Parse()

	token := os.Getenv("DIGITALOCEAN_TOKEN")
	if token == "" {
		log.Fatal("DIGITALOCEAN_TOKEN not set")
	}
	client := godo.NewFromToken(token)
	if err := reap(context.Background(), client.Droplets, time.Now(), *maxAge, os.Stdout); err != nil {
		log.Fatal(err)
	}
}

// reap deletes every expired resource returned by the forge-e2e tag query. It
// keeps processing after malformed resources or delete failures, then returns a
// joined error so the scheduled safety net cannot report a false success.
func reap(ctx context.Context, client dropletClient, now time.Time, maxAge time.Duration, output io.Writer) error {
	opts := &godo.ListOptions{PerPage: 200}
	var failures []error
	for {
		droplets, resp, err := client.ListByTag(ctx, "forge-e2e", opts)
		if err != nil {
			return fmt.Errorf("list droplets tagged forge-e2e: %w", err)
		}
		for _, droplet := range droplets {
			created, err := time.Parse(time.RFC3339, droplet.Created)
			if err != nil {
				failures = append(failures, fmt.Errorf("droplet %d bad created time %q: %w", droplet.ID, droplet.Created, err))
				continue
			}
			age := now.Sub(created)
			if age < maxAge {
				continue
			}
			if _, err := client.Delete(ctx, droplet.ID); err != nil {
				failures = append(failures, fmt.Errorf("delete droplet %d (%s): %w", droplet.ID, droplet.Name, err))
				continue
			}
			fmt.Fprintf(output, "deleted droplet %d (%s, age %s)\n", droplet.ID, droplet.Name, age.Truncate(time.Minute))
		}
		if resp == nil || resp.Links == nil || resp.Links.IsLastPage() {
			break
		}
		opts.Page, _ = resp.Links.CurrentPage()
		opts.Page++
	}
	return errors.Join(failures...)
}
