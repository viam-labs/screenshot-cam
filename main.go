package main

import (
	"context"
	"image/png"
	"os"

	"github.com/kbinani/screenshot"
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
	if len(os.Args) >= 3 {
		arg = os.Args[1]
		capturePath = os.Args[2]
	}
	switch arg {
	case "parent":
		// parent is a test mode for spawning a child proc directly from session 0 CLI. see README.md for instructions.
		if err := subproc.SpawnSelf(" child " + capturePath); err != nil {
			panic(err)
		}
	case "child":
		// child is the subprocess started in session 1 by a session 0 parent. it does the work.
		logger.Info("dumping a screenshot instead of starting module")
		img, err := screenshot.CaptureDisplay(0)
		if err != nil {
			panic(err)
		}
		f, err := os.Create(capturePath)
		if err != nil {
			panic(err)
		}
		defer f.Close()
		if err := png.Encode(f, img); err != nil {
			panic(err)
		}
		logger.Infof("wrote to %s", capturePath)
	default:
		utils.ContextualMain(mainWithArgs, logger)
	}
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
