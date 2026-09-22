package d2

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// useEngine sets the rasteriser for one test and puts it back afterwards, since
// it is chosen once for a whole run.
func useEngine(t *testing.T, kind string) {
	t.Helper()

	previous := d2Engine
	t.Cleanup(func() { d2Engine = previous })

	require.NoError(t, UseEngine(kind))
}

// TestUseEngineRefusesWhatItCannotRasterise covers a name that is neither
// engine. Falling back to chrome would publish pictures drawn by something
// other than what was asked for, and say nothing about it.
func TestUseEngineRefusesWhatItCannotRasterise(t *testing.T) {
	t.Run("chrome", func(t *testing.T) {
		useEngine(t, EngineChrome)
		assert.Equal(t, EngineChrome, d2Engine)
	})

	t.Run("resvg", func(t *testing.T) {
		useEngine(t, EngineResvg)
		assert.Equal(t, EngineResvg, d2Engine)
	})

	// An unset engine is not an error: the flag has a default, and a run that
	// never set one draws the way mark always has.
	t.Run("empty means chrome", func(t *testing.T) {
		useEngine(t, EngineResvg)
		useEngine(t, "")
		assert.Equal(t, EngineChrome, d2Engine)
	})

	t.Run("anything else", func(t *testing.T) {
		previous := d2Engine
		assert.Error(t, UseEngine("inkscape"))
		assert.Equal(t, previous, d2Engine, "a refused engine must not change what is drawn with")
	})
}
