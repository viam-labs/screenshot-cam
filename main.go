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

func main() {
	logger := module.NewLoggerFromArgs("screenshot-cam")
	var arg string
	var capturePath string
	if len(os.Args) >= 2 {
		arg = os.Args[1]
	}
	if len(os.Args) >= 3 {
		capturePath = os.Args[2]
	}
	switch arg {
	case "parent":
		// parent is a test mode for spawning a child proc directly from session 0 CLI. see README.md for instructions.
		n := 1
		if len(os.Args) >= 4 {
			if nTries, err := strconv.Atoi(os.Args[3]); err == nil {
				n = nTries
			}
		}
		t0 := time.Now()
		for range n {
			if err := subproc.SpawnSelf(" child " + capturePath); err != nil {
				panic(err)
			}
		}
		delta := time.Now().Sub(t0)
		fmt.Printf("captured %d screenshots in %s seconds, %f per second", n, (delta / time.Second).String(), float64(n)/float64(delta/time.Second))
	case "child":
		// child is the subprocess started in session 1 by a session 0 parent. it does the work.
		logger.Debugf("dumping a screenshot instead of starting module")
		if err := captureToPath(logger, capturePath); err != nil {
			panic(err)
		}
	default:
		utils.ContextualMain(mainWithArgs, logger)
	}
}

func captureToPath(logger logging.Logger, path string) error {
	img, err := screenshot.CaptureDisplay(0)
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
