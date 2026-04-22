//go:build !testhooks

package testhooks

import (
	"context"
	"errors"
)

var ErrDisabled = errors.New("testhooks build tag is not enabled")

type Controller struct{}

func NewController() *Controller {
	return &Controller{}
}

func Install(_ *Controller) func() {
	return func() {}
}

func Point(_ string, _ ...Field) {}

func (c *Controller) AddBarrier(_ string, _ int) {}

func (c *Controller) WaitUntilHit(_ context.Context, _ string, _ int) (Hit, error) {
	return Hit{}, ErrDisabled
}

func (c *Controller) Release(_ string, _ int) bool {
	return false
}

func (c *Controller) Hits(_ string) []Hit {
	return nil
}
