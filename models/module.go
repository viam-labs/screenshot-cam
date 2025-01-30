package models

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"image/png"

	"github.com/kbinani/screenshot"
	"go.viam.com/rdk/components/camera"
	"go.viam.com/rdk/components/camera/rtppassthrough"
	"go.viam.com/rdk/gostream"
	"go.viam.com/rdk/logging"
	"go.viam.com/rdk/pointcloud"
	"go.viam.com/rdk/resource"
	"go.viam.com/utils/rpc"
)

var (
	Screenshot       = resource.NewModel("viam", "screenshot-cam", "screenshot")
	errUnimplemented = errors.New("unimplemented")
)

func init() {
	resource.RegisterComponent(camera.API, Screenshot,
		resource.Registration[camera.Camera, *Config]{
			Constructor: newScreenshotCamScreenshot,
		},
	)
}

type Config struct {
	resource.TriviallyValidateConfig
	DisplayIndex int // index of display to capture (relevant when there are multiple monitors)
}

// Validate ensures all parts of the config are valid and important fields exist.
// Returns implicit dependencies based on the config.
// The path is the JSON path in your robot's config (not the `Config` struct) to the
// resource being validated; e.g. "components.0".
func (cfg *Config) Validate(path string) ([]string, error) {
	// Add config validation code here
	return nil, nil
}

type screenshotCamScreenshot struct {
	name resource.Name

	logger logging.Logger
	cfg    *Config

	cancelCtx  context.Context
	cancelFunc func()

	/* Uncomment this if your model does not need to reconfigure. */
	// resource.TriviallyReconfigurable

	// Uncomment this if the model does not have any goroutines that
	// need to be shut down while closing.
	// resource.TriviallyCloseable

}

func newScreenshotCamScreenshot(ctx context.Context, deps resource.Dependencies, rawConf resource.Config, logger logging.Logger) (camera.Camera, error) {
	conf, err := resource.NativeConfig[*Config](rawConf)
	if err != nil {
		return nil, err
	}

	cancelCtx, cancelFunc := context.WithCancel(context.Background())

	s := &screenshotCamScreenshot{
		name:       rawConf.ResourceName(),
		logger:     logger,
		cfg:        conf,
		cancelCtx:  cancelCtx,
		cancelFunc: cancelFunc,
	}
	return s, nil
}

func (s *screenshotCamScreenshot) Name() resource.Name {
	return s.name
}

func (s *screenshotCamScreenshot) Reconfigure(ctx context.Context, deps resource.Dependencies, conf resource.Config) error {
	myConf, err := resource.NativeConfig[*Config](conf)
	if err != nil {
		return err
	}
	s.cfg = myConf
	return nil
}

func (s *screenshotCamScreenshot) NewClientFromConn(ctx context.Context, conn rpc.ClientConn, remoteName string, name resource.Name, logger logging.Logger) (camera.Camera, error) {
	panic("not implemented")
}

func (s *screenshotCamScreenshot) Stream(ctx context.Context, errHandlers ...gostream.ErrorHandler) (gostream.VideoStream, error) {
	panic("not implemented")
}

func (s *screenshotCamScreenshot) Image(ctx context.Context, mimeType string, extra map[string]interface{}) ([]byte, camera.ImageMetadata, error) {
	img, err := screenshot.CaptureDisplay(s.cfg.DisplayIndex)
	if err != nil {
		return nil, camera.ImageMetadata{}, err
	}
	var buf bytes.Buffer
	if err := png.Encode(bufio.NewWriter(&buf), img); err != nil {
		return nil, camera.ImageMetadata{}, err
	}
	return buf.Bytes(), camera.ImageMetadata{MimeType: "image/png"}, nil
}

func (s *screenshotCamScreenshot) Images(ctx context.Context) ([]camera.NamedImage, resource.ResponseMetadata, error) {
	panic("not implemented")
}

func (s *screenshotCamScreenshot) NextPointCloud(ctx context.Context) (pointcloud.PointCloud, error) {
	panic("not implemented")
}

func (s *screenshotCamScreenshot) Properties(ctx context.Context) (camera.Properties, error) {
	panic("not implemented")
}

func (s *screenshotCamScreenshot) DoCommand(ctx context.Context, cmd map[string]interface{}) (map[string]interface{}, error) {
	panic("not implemented")
}

func (s *screenshotCamScreenshot) SubscribeRTP(ctx context.Context, bufferSize int, packetsCB rtppassthrough.PacketCallback) (rtppassthrough.Subscription, error) {
	panic("not implemented")
}

func (s *screenshotCamScreenshot) Unsubscribe(ctx context.Context, id rtppassthrough.SubscriptionID) error {
	panic("not implemented")
}

func (s *screenshotCamScreenshot) Close(context.Context) error {
	// Put close code here
	s.cancelFunc()
	return nil
}
