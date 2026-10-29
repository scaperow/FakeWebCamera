//go:build !darwin

package menubar

import (
	"context"

	"fakewebcamera/internal/app"
)

// Run blocks until ctx is cancelled on non-macOS platforms.
func Run(ctx context.Context, a *app.App) {
	<-ctx.Done()
}
