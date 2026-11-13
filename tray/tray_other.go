//go:build !darwin

// Package tray is rota in the macOS menu bar; elsewhere there is none.
package tray

import (
	"context"
	"errors"
)

// Run says there is no menu bar here.
func Run(ctx context.Context, root, conf, exe string, inBundle bool) error {
	return errors.New("rota tray is available on macOS only")
}
