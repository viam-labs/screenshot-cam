package main

import (
	"screenshot-cam/models"
	"context"
	"go.viam.com/rdk/logging"
	"go.viam.com/rdk/module"
	"go.viam.com/utils"
	"go.viam.com/rdk/components/camera"

)

func main() {
	utils.ContextualMain(mainWithArgs, module.NewLoggerFromArgs("screenshot-cam"))
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
