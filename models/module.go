package models

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"image"
	"image/jpeg"
	"math/rand/v2"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/kbinani/screenshot"
	"github.com/viam-labs/screenshot-cam/subproc"
	"go.viam.com/rdk/components/camera"
	"go.viam.com/rdk/gostream"
	"go.viam.com/rdk/logging"
	"go.viam.com/rdk/pointcloud"
	"go.viam.com/rdk/resource"
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
	// index of display to capture (relevant when there are multiple monitors)
	DisplayIndex int `json:"display_index"`
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

	logger       logging.Logger
	cfg          *Config
	hasWarnedTmp bool

	cancelCtx  context.Context
	cancelFunc func()

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
	if !subproc.ShouldSpawn() {
		// note: this will be wrong in the ShouldSpawn case so we don't log it
		logger.Infof("Displays for screenshotting: %d", screenshot.NumActiveDisplays())
	}

	s := &screenshotCamScreenshot{
		name:       rawConf.ResourceName(),
		logger:     logger,
		cfg:        conf,
		cancelCtx:  cancelCtx,
		cancelFunc: cancelFunc,
	}
	return s, nil
}
func (s *screenshotCamScreenshot) Reconfigure(ctx context.Context, deps resource.Dependencies, rawConf resource.Config) error {
	conf, err := resource.NativeConfig[*Config](rawConf)
	if err == nil {
		s.cfg = conf
	}
	return err
}

func (s *screenshotCamScreenshot) Name() resource.Name {
	return s.name
}

func (s *screenshotCamScreenshot) Stream(ctx context.Context, errHandlers ...gostream.ErrorHandler) (gostream.VideoStream, error) {
	return nil, errUnimplemented
}

func (s *screenshotCamScreenshot) Image(ctx context.Context, mimeType string, extra map[string]interface{}) ([]byte, camera.ImageMetadata, error) {
	if !subproc.ShouldSpawn() {
		img, err := screenshot.CaptureDisplay(s.cfg.DisplayIndex)
		if err != nil {
			return nil, camera.ImageMetadata{}, err
		}
		var buf bytes.Buffer
		if err := jpeg.Encode(bufio.NewWriter(&buf), img, nil); err != nil {
			return nil, camera.ImageMetadata{}, err
		}
		return buf.Bytes(), camera.ImageMetadata{MimeType: "image/jpeg"}, nil
	}
	td := os.TempDir()
	if strings.ToLower(td) == "c:\\windows\\systemtemp" {
		// this is an experimental workaround for an access denied bug we've seen
		newTd := "C:\\windows\\TEMP"
		if !s.hasWarnedTmp {
			s.hasWarnedTmp = true
			s.logger.Warnf("applying workaround to rewrite tempdir from %s to %s", td, newTd)
		}
		td = newTd
	}
	capturePath := filepath.Join(td, fmt.Sprintf("screenshot-cam-%d.jpg", rand.IntN(1000000)))
	if err := subproc.SpawnSelf(fmt.Sprintf(" -mode child -path %s -display %d", capturePath, s.cfg.DisplayIndex)); err != nil {
		return nil, camera.ImageMetadata{}, err
	}
	defer os.Remove(capturePath)
	buf, err := os.ReadFile(capturePath)
	if err != nil {
		return nil, camera.ImageMetadata{}, err
	}
	return buf, camera.ImageMetadata{MimeType: "image/jpeg"}, nil
}

func (s *screenshotCamScreenshot) Images(ctx context.Context) ([]camera.NamedImage, resource.ResponseMetadata, error) {
	raw, _, err := s.Image(ctx, "image/jpg", nil)
	if err != nil {
		return nil, resource.ResponseMetadata{}, err
	}

	reader := bytes.NewReader(raw)
	img, _, err := image.Decode(reader)
	if err != nil {
		return nil, resource.ResponseMetadata{}, err
	}

	return []camera.NamedImage{
		{img, "screen"},
	}, resource.ResponseMetadata{time.Now()}, nil
}

func (s *screenshotCamScreenshot) NextPointCloud(ctx context.Context) (pointcloud.PointCloud, error) {
	return nil, errUnimplemented
}

func (s *screenshotCamScreenshot) Properties(ctx context.Context) (camera.Properties, error) {
	return camera.Properties{
		MimeTypes: []string{"image/jpeg"},
	}, nil
}

func (s *screenshotCamScreenshot) DoCommand(ctx context.Context, cmd map[string]interface{}) (map[string]interface{}, error) {
	return nil, errUnimplemented
}

func (s *screenshotCamScreenshot) Close(context.Context) error {
	// Put close code here
	s.cancelFunc()
	return nil
}
