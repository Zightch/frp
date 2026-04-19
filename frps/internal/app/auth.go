package app

import (
	"fmt"

	"github.com/zightch/frp/frps/internal/auth"
)

func (a *App) initAuth() error {
	manager, err := auth.NewManager(auth.Options{})
	if err != nil {
		return fmt.Errorf("init management auth: %w", err)
	}

	a.auth = manager
	a.logger.Info(
		"management auth ready",
		"initialized", manager.Initialized(),
		"path", manager.Path(),
	)
	return nil
}
