// Command reaper deletes DigitalOcean droplets and block volumes tagged
// "forge-e2e" after the crash-safety threshold. The three-hour default exceeds
// the longest 155-minute Forge workflow bound, so an active run cannot be reaped
// before its owner cleanup can finish. Run on a cron in CI.
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

const defaultMaxAge = 3 * time.Hour

type dropletClient interface {
	ListByTag(context.Context, string, *godo.ListOptions) ([]godo.Droplet, *godo.Response, error)
	Delete(context.Context, int) (*godo.Response, error)
}

type volumeClient interface {
	ListVolumes(context.Context, *godo.ListVolumeParams) ([]godo.Volume, *godo.Response, error)
	DeleteVolume(context.Context, string) (*godo.Response, error)
}

func main() {
	maxAge := flag.Duration("max-age", defaultMaxAge, "delete tagged droplets and block volumes older than this")
	flag.Parse()

	token := os.Getenv("DIGITALOCEAN_TOKEN")
	if token == "" {
		log.Fatal("DIGITALOCEAN_TOKEN not set")
	}
	client := godo.NewFromToken(token)
	now := time.Now()
	if err := errors.Join(
		reap(context.Background(), client.Droplets, now, *maxAge, os.Stdout),
		reapVolumes(context.Background(), client.Storage, now, *maxAge, os.Stdout),
	); err != nil {
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

// reapVolumes deletes tagged block volumes left by the dedicated-storage E2E.
// Attached volumes may need a later scheduled pass after droplet deletion
// finishes; such a refusal is reported rather than mistaken for success.
func reapVolumes(ctx context.Context, client volumeClient, now time.Time, maxAge time.Duration, output io.Writer) error {
	opts := &godo.ListVolumeParams{ListOptions: &godo.ListOptions{PerPage: 200}}
	var failures []error
	for {
		volumes, resp, err := client.ListVolumes(ctx, opts)
		if err != nil {
			return fmt.Errorf("list block volumes: %w", err)
		}
		for _, volume := range volumes {
			if !hasTag(volume.Tags, "forge-e2e") || now.Sub(volume.CreatedAt) < maxAge {
				continue
			}
			if _, err := client.DeleteVolume(ctx, volume.ID); err != nil {
				failures = append(failures, fmt.Errorf("delete block volume %s (%s): %w", volume.ID, volume.Name, err))
				continue
			}
			fmt.Fprintf(output, "deleted block volume %s (%s, age %s)\n", volume.ID, volume.Name, now.Sub(volume.CreatedAt).Truncate(time.Minute))
		}
		if resp == nil || resp.Links == nil || resp.Links.IsLastPage() {
			break
		}
		opts.ListOptions.Page, _ = resp.Links.CurrentPage()
		opts.ListOptions.Page++
	}
	return errors.Join(failures...)
}

func hasTag(tags []string, wanted string) bool {
	for _, tag := range tags {
		if tag == wanted {
			return true
		}
	}
	return false
}
