package util

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/urfave/cli/v3"
)

// TestD2EngineFlagValidation covers the value a run is told to rasterise with.
// A name mark does not know would otherwise reach the renderer and fall back to
// chrome, publishing pictures drawn by something other than what was asked for.
func TestD2EngineFlagValidation(t *testing.T) {
	run := func(args ...string) error {
		cmd := &cli.Command{
			Flags: []cli.Flag{
				&cli.StringFlag{Name: "d2-engine", Value: "chrome"},
			},
			Before: CheckFlags,
			Action: func(context.Context, *cli.Command) error { return nil },
		}

		return cmd.Run(context.Background(), append([]string{"cmd"}, args...))
	}

	t.Run("chrome is accepted", func(t *testing.T) {
		assert.NoError(t, run("--d2-engine", "chrome"))
	})

	t.Run("resvg is accepted", func(t *testing.T) {
		assert.NoError(t, run("--d2-engine", "resvg"))
	})

	t.Run("anything else is rejected", func(t *testing.T) {
		assert.Error(t, run("--d2-engine", "inkscape"))
	})

	t.Run("emptied on purpose is rejected", func(t *testing.T) {
		assert.Error(t, run("--d2-engine", ""))
	})
}
