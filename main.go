package main

import (
	"context"
	"fmt"
	"image"
	"image/jpeg"
	"os"
	"strconv"
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

// var capturePath = flag.String("path", "", "filename to save image")
// var nTries = flag.Int("n", 1, "number of images to capture")
// var displayIndex = flag.Int("display", 0, "display index to capture when there are multiple monitors")

func main() {
	logger := module.NewLoggerFromArgs("screenshot-cam")
	// note: we're doing this manual
	var command string
	var capturePath string
	var nTries int = 1
	var displayIndex int
	var err error
	if len(os.Args) >= 2 {
		command = os.Args[1]
	}
	switch command {
	case "parent":
		fallthrough
	case "child":
		if len(os.Args) >= 3 {
			capturePath = os.Args[2]
		}
		if len(os.Args) >= 4 {
			nTries, err = strconv.Atoi(os.Args[3])
			if err != nil {
				nTries = 1
				logger.Warnf("ignoring nTries parse error %s %s", os.Args[3], err)
			}
		}
		if len(os.Args) >= 5 {
			displayIndex, err = strconv.Atoi(os.Args[4])
			if err != nil {
				logger.Warnf("ignoring displayIndex parse error %s %s", os.Args[4], err)
			}
		}
	}
	switch command {
	case "parent":
		// parent is a test mode for spawning a child proc directly from session 0 CLI. see README.md for instructions.
		t0 := time.Now()
		for range nTries {
			if err := subproc.SpawnSelf(fmt.Sprintf(" child %s %d %d", capturePath, 1, displayIndex)); err != nil {
				panic(err)
			}
		}
		delta := time.Now().Sub(t0)
		fmt.Printf("captured %d screenshots in %s seconds, %f per second", nTries, (delta / time.Second).String(), float64(nTries)/float64(delta/time.Second))
	case "child":
		// child is the subprocess started in session 1 by a session 0 parent. it does the work.
		logger.Debugf("dumping a screenshot instead of starting module")
		logger.Infof("%d active displays, using index %d", screenshot.NumActiveDisplays(), displayIndex)
		if err := captureToPath(logger, capturePath, displayIndex); err != nil {
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
