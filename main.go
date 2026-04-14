package main

import (
	"bytes"
	"context"
	"errors"
	"flag"
	"fmt"
	"image"
	"image/jpeg"
	"io"
	"os"
	"time"

	"github.com/kbinani/screenshot"
	"github.com/nfnt/resize"
	"github.com/viam-labs/screenshot-cam/models"
	"github.com/viam-labs/screenshot-cam/subproc"
	"go.viam.com/rdk/components/camera"
	"go.viam.com/rdk/logging"
	"go.viam.com/rdk/module"
	"go.viam.com/utils"
	"golang.org/x/sys/windows"
)

// note: these flags are only relevant if the program is in parent/child mode.
// Otherwise, the viam module system will take care of argument parsing.
var programMode = flag.String("mode", "", "'parent', 'child', or 'persistent' for subprocess flow")
var capturePath = flag.String("path", "", "filename to save image (parent/child modes only)")
var nTries = flag.Uint("n", 1, "number of images to capture (parent mode only)")
var displayIndex = flag.Uint("display", 0, "display index to capture when there are multiple monitors")

// newStderrLogger builds a debug-level logger that writes to stderr only.
// Used by the `persistent` and `child` subprocess modes, where stdout is
// reserved for the binary capture protocol and any stray write would corrupt
// the JPEG stream sent back to the parent.
func newStderrLogger(name string) logging.Logger {
	l := logging.NewBlankLogger(name)
	l.AddAppender(logging.NewWriterAppender(os.Stderr))
	l.SetLevel(logging.DEBUG)
	return l
}

func main() {
	flag.Parse()
	var logger logging.Logger
	switch *programMode {
	case "persistent", "child":
		// Don't use module.NewLoggerFromArgs here: it appends to os.Stdout,
		// which is our binary protocol channel.
		logger = newStderrLogger("screenshot-cam-child")
	default:
		logger = module.NewLoggerFromArgs("screenshot-cam")
	}
	switch *programMode {
	case "parent", "child":
		if *capturePath == "" {
			logger.Error("the -path argument is required in the subprocess flow")
			return
		}
	}
	switch *programMode {
	case "parent":
		// parent is a manual test mode for driving a persistent child directly
		// from a session 0 CLI. see README.md for instructions.
		child, err := subproc.StartPersistentChild("-mode persistent")
		if err != nil {
			panic(err)
		}
		defer child.Close()
		// drain child stderr to ours so panics/log lines are visible
		go io.Copy(os.Stderr, child.Stderr())

		t0 := time.Now()
		for i := uint(0); i < *nTries; i++ {
			data, err := child.CaptureJPEG(uint32(*displayIndex))
			if err != nil {
				panic(err)
			}
			path := *capturePath
			if *nTries > 1 {
				path = fmt.Sprintf("%s.%d.jpg", *capturePath, i)
			}
			if err := os.WriteFile(path, data, 0644); err != nil {
				panic(err)
			}
		}
		delta := time.Since(t0)
		fmt.Printf("captured %d screenshots in %s, %f per second\n",
			*nTries, delta.String(), float64(*nTries)/delta.Seconds())
	case "child":
		// child is the legacy one-shot subprocess (still useful for debugging
		// SpawnSelf). It captures a single screenshot and writes it to disk.
		logger.Debugf("%d active displays, using index %d", screenshot.NumActiveDisplays(), *displayIndex)
		if int(*displayIndex) >= screenshot.NumActiveDisplays() {
			logger.Warnf("display index %d >= active displays %d, the subprocess may crash", *displayIndex, screenshot.NumActiveDisplays())
		}
		if err := captureToPath(logger, *capturePath, int(*displayIndex)); err != nil {
			panic(err)
		}
	case "persistent":
		// persistent is the long-running subprocess started by the module in
		// session 0. It serves capture requests from stdin and writes JPEGs to
		// stdout. See subproc.RunChildLoop for the wire protocol.
		if err := models.CheckSession(logger); err != nil {
			logger.Warnf("error checking session: %s", err)
		}
		logger.Debugf("%d active displays at startup", screenshot.NumActiveDisplays())
		if err := subproc.RunChildLoop(func(d uint32) ([]byte, error) {
			return captureJPEG(logger, int(d))
		}); err != nil {
			logger.Errorf("persistent child loop ended: %s", err)
			os.Exit(1)
		}
	default:
		utils.ContextualMain(mainWithArgs, logger)
	}
}

// captureJPEG grabs a screenshot of the given display, downsizes anything
// wider than 1920px, and returns the JPEG-encoded bytes.
func captureJPEG(logger logging.Logger, displayIndex int) ([]byte, error) {
	img, err := screenshot.CaptureDisplay(displayIndex)
	if err != nil {
		if lastErr := windows.GetLastError(); lastErr != nil {
			logger.Errorf("windows GetLastError %s", lastErr.Error())
		}
		return nil, err
	}
	if img.Rect.Size().X > 1920 {
		resized := resize.Resize(1920, 0, img, resize.Bilinear)
		var ok bool
		img, ok = resized.(*image.RGBA)
		if !ok {
			return nil, errors.New("resized image is not RGBA")
		}
	}
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, img, nil); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

func captureToPath(logger logging.Logger, path string, displayIndex int) error {
	if err := models.CheckSession(logger); err != nil {
		logger.Warnf("error checking session: %s", err)
	}
	data, err := captureJPEG(logger, displayIndex)
	if err != nil {
		return err
	}
	if err := os.WriteFile(path, data, 0644); err != nil {
		return err
	}
	logger.Debugf("wrote to %s", path)
	return nil
}

func mainWithArgs(ctx context.Context, args []string, logger logging.Logger) error {
	screenshotCam, err := module.NewModuleFromArgs(ctx)
	if err != nil {
		return err
	}

	if err = screenshotCam.AddModelFromRegistry(ctx, camera.API, models.Screenshot); err != nil {
		return err
	}

	err = screenshotCam.Start(ctx)
	defer screenshotCam.Close(ctx)
	if err != nil {
		return err
	}

	<-ctx.Done()
	return nil
}
