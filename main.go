package main

import (
	"context"
	"flag"
	"fmt"
	"image"
	"image/jpeg"
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
)

// note: these flags are only relevant if the program is in parent/child mode.
// Otherwise, the viam module system will take care of argument parsing.
var programMode = flag.String("mode", "", "'parent' or 'child' for subprocess flow")
var capturePath = flag.String("path", "", "filename to save image")
var nTries = flag.Uint("n", 1, "number of images to capture")
var displayIndex = flag.Uint("display", 0, "display index to capture when there are multiple monitors")

func main() {
	logger := module.NewLoggerFromArgs("screenshot-cam")
	flag.Parse()
	switch *programMode {
	case "parent":
		fallthrough
	case "child":
		if *capturePath == "" {
			logger.Error("the -path argument is required in the subprocess flow")
			return
		}
	}
	switch *programMode {
	case "parent":
		// parent is a test mode for spawning a child proc directly from session 0 CLI. see README.md for instructions.
		t0 := time.Now()
		for range *nTries {
			if err := subproc.SpawnSelf(fmt.Sprintf(" -mode child -path %s -display %d", *capturePath, *displayIndex)); err != nil {
				panic(err)
			}
		}
		delta := time.Since(t0)
		fmt.Printf("captured %d screenshots in %s seconds, %f per second", *nTries, (delta / time.Second).String(), float64(*nTries)/float64(delta/time.Second))
	case "child":
		// child is the subprocess started in session 1 by a session 0 parent. it does the work.
		logger.Debugf("%d active displays, using index %d", screenshot.NumActiveDisplays(), *displayIndex)
		if int(*displayIndex) >= screenshot.NumActiveDisplays() {
			logger.Warnf("display index %d >= active displays %d, the subprocess may crash", *displayIndex, screenshot.NumActiveDisplays())
		}
		if err := captureToPath(logger, *capturePath, int(*displayIndex)); err != nil {
			panic(err)
		}
	default:
		utils.ContextualMain(mainWithArgs, logger)
	}
}

func captureToPath(logger logging.Logger, path string, displayIndex int) error {
	img, err := screenshot.CaptureDisplay(displayIndex)
	if err != nil {
		return err
	}
	if img.Rect.Size().X > 1920 {
		resized := resize.Resize(1920, 0, img, resize.Bilinear)
		var ok bool
		img, ok = resized.(*image.RGBA)
		if !ok {
			panic("resized image is not RGBA")
		}
	}
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	defer f.Close()
	if err := jpeg.Encode(f, img, nil); err != nil {
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
