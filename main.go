package main

import (
	"context"
	"image/png"
	"os"

	"github.com/kbinani/screenshot"
	"github.com/viam-labs/screenshot-cam/models"
	"go.viam.com/rdk/components/camera"
	"go.viam.com/rdk/logging"
	"go.viam.com/rdk/module"
	"go.viam.com/utils"
)

func main() {
	logger := module.NewLoggerFromArgs("screenshot-cam")
	if os.Args[1] == "dump" {
		logger.Info("dumping a screenshot instead of starting module")
		img, err := screenshot.CaptureDisplay(0)
		if err != nil {
			panic(err)
		}
		path := "screenshot.png"
		f, err := os.Create(path)
		if err != nil {
			panic(err)
		}
		defer f.Close()
		if err := png.Encode(f, img); err != nil {
			panic(err)
		}
		logger.Infof("wrote to %s", path)
		return
	}
	utils.ContextualMain(mainWithArgs, logger)
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
