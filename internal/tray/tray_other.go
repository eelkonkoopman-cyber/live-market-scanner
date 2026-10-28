//go:build !windows

package tray

import "context"

func run(ctx context.Context, _ Options) error {
	<-ctx.Done()
	return nil
}
