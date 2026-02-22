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
	"go.viam.com/rdk/data"
	"go.viam.com/rdk/gostream"
	"go.viam.com/rdk/logging"
	"go.viam.com/rdk/pointcloud"
	"go.viam.com/rdk/resource"
	"go.viam.com/rdk/spatialmath"
	"golang.org/x/sys/windows"
)

var (
	Screenshot                     = resource.NewModel("viam", "screenshot-cam", "screenshot")
	errUnimplemented               = errors.New("unimplemented")
	_                camera.Camera = (*screenshotCamScreenshot)(nil)
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

// CheckSession warns if you're not in the active console session.
func CheckSession(logger logging.Logger) error {
	var sid uint32
	if err := windows.ProcessIdToSessionId(windows.GetCurrentProcessId(), &sid); err != nil {
		return err
	}
	active := windows.WTSGetActiveConsoleSessionId()
	logger.Debugf("current session %d, active session %d", sid, active)
	if sid != active {
		logger.Warnw("current session is not the active session", "current", sid, "active", active)
	}
	return nil
}

func (s *screenshotCamScreenshot) Image(ctx context.Context, mimeType string, extra map[string]interface{}) ([]byte, camera.ImageMetadata, error) {
	if !subproc.ShouldSpawn() {
		// note: this code isn't reachable on windows in service mode; the NON ShouldSpawn
		// path hits `-mode child` in main.go.
		if err := CheckSession(s.logger); err != nil {
			s.logger.Errorf("error in CheckSession: %s", err)
		}
		img, err := screenshot.CaptureDisplay(s.cfg.DisplayIndex)
		if err != nil {
			if err := windows.GetLastError(); err != nil {
				s.logger.Warnf("windows last error %s", err)
			}
			return nil, camera.ImageMetadata{}, err
		}
		var buf bytes.Buffer
		if err := jpeg.Encode(bufio.NewWriter(&buf), img, nil); err != nil {
			return nil, camera.ImageMetadata{}, err
		}
		return buf.Bytes(), camera.ImageMetadata{MimeType: "image/jpeg"}, nil
	}
	if !subproc.HasDesktopSession() {
		return nil, camera.ImageMetadata{}, subproc.ErrNoDesktopSession
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
	// careful: if you modify the SpawnSelf call, think through the recursion risk. The spawned subprocess
	// should not itself check ShouldSpawn and spawn again. (Because if ShouldSpawn breaks, you'll create
	// infinite subprocs).
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

func (s *screenshotCamScreenshot) Images(ctx context.Context, filterSourceNames []string, extra map[string]interface{}) ([]camera.NamedImage, resource.ResponseMetadata, error) {
	mimetype := "image/jpeg"
	raw, _, err := s.Image(ctx, mimetype, nil)
	if err != nil {
		return nil, resource.ResponseMetadata{}, err
	}

	reader := bytes.NewReader(raw)
	img, _, err := image.Decode(reader)
	if err != nil {
		return nil, resource.ResponseMetadata{}, err
	}

	named, err := camera.NamedImageFromImage(img, "screen", mimetype, data.Annotations{})
	if err != nil {
		return nil, resource.ResponseMetadata{}, err
	}
	return []camera.NamedImage{
		named,
	}, resource.ResponseMetadata{CapturedAt: time.Now()}, nil
}

func (s *screenshotCamScreenshot) NextPointCloud(ctx context.Context, extra map[string]any) (pointcloud.PointCloud, error) {
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

func (s *screenshotCamScreenshot) Geometries(context.Context, map[string]interface{}) ([]spatialmath.Geometry, error) {
	return nil, errUnimplemented
}
