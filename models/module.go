package models

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
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
	"go.viam.com/rdk/utils"
	"golang.org/x/sys/windows"
)

var (
	Screenshot                      = resource.NewModel("viam", "screenshot-cam", "screenshot")
	errUnimplemented                = errors.New("unimplemented")
	_                 camera.Camera = (*screenshotCamScreenshot)(nil)
	shouldSpawnCached               = subproc.ShouldSpawn()
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
	// when true, use a persistent child process that continuously captures
	// screenshots into a ring buffer. When false (default), spawn a one-shot
	// child process per capture request.
	PersistentMode bool `json:"persistent_mode,omitempty"`
	// number of slots in the frame ring buffer (minimum 4, rounded up to a
	// power of 2; only used in persistent mode).
	BufferSize int `json:"buffer_size,omitempty"`
	// target resize dimensions for captured screenshots (0 = use default behavior)
	ResizeWidth  int `json:"resize_width,omitempty"`
	ResizeHeight int `json:"resize_height,omitempty"`
}

type screenshotCamScreenshot struct {
	resource.AlwaysRebuild
	name resource.Name

	logger       logging.Logger
	cfg          *Config
	hasWarnedTmp bool

	cancelCtx  context.Context
	cancelFunc func()

	// persistent child subprocess used in session 0 mode. nil until first use,
	// re-created after any I/O failure.
	child *subproc.PersistentChild
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
	if subproc.ShouldSpawn() && conf.PersistentMode {
		s.child, err = s.getOrStartPersistentChild()
		if err != nil {
			return nil, err
		}
	}
	return s, nil
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

// getOrStartPersistentChild returns the cached persistent child, starting one if needed.
func (s *screenshotCamScreenshot) getOrStartPersistentChild() (*subproc.PersistentChild, error) {
	if s.child != nil {
		return s.child, nil
	}
	bufSize := s.cfg.BufferSize
	if bufSize < 4 {
		bufSize = 4
	}
	cmdArgs := "-mode persistent"
	if s.cfg.ResizeWidth > 0 && s.cfg.ResizeHeight > 0 {
		cmdArgs += fmt.Sprintf(" -resize-width %d -resize-height %d", s.cfg.ResizeWidth, s.cfg.ResizeHeight)
	}
	child, err := subproc.StartPersistentChild(cmdArgs, uint32(s.cfg.DisplayIndex), bufSize)
	if err != nil {
		return nil, err
	}
	// Drain child stderr in the background. zap log lines from the subprocess
	// land here; surface them through our logger so they show up in module logs.
	go func(r *bufio.Reader) {
		for {
			line, err := r.ReadString('\n')
			if len(line) > 0 {
				s.logger.Debugf("[child] %s", line)
			}
			if err != nil {
				return
			}
		}
	}(bufio.NewReader(child.Stderr()))
	// Supervisor: if the child dies unexpectedly, exit the module process so
	// the viam-server/agent can restart us cleanly. Only fires on crashes —
	// clean shutdowns via Close() set Closed() and are ignored.
	go func(c *subproc.PersistentChild) {
		<-c.Done()
		if c.Closed() {
			return
		}
		s.logger.Errorf("screenshot-cam persistent child died unexpectedly; exiting module process")
		os.Exit(1)
	}(child)
	return child, nil
}

// invalidateChild closes and clears `c` if it's still the currently cached
// child. Called after an IPC error so the next request starts a fresh process.
func (s *screenshotCamScreenshot) invalidateChild(c *subproc.PersistentChild) {
	if s.child == c {
		s.child = nil
		go c.Close() // don't block the caller on process teardown
	}
}

func (s *screenshotCamScreenshot) Image(ctx context.Context, mimeType string, extra map[string]interface{}) ([]byte, camera.ImageMetadata, error) {
	if !shouldSpawnCached {
		// note: this code isn't reachable on windows in service mode; the NON ShouldSpawn
		// path hits the persistent child below.
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
	} else if s.cfg.PersistentMode {
		jpegBytes, err := s.child.LatestFrame()
		if err != nil {
			return nil, camera.ImageMetadata{}, err
		}
		return jpegBytes, camera.ImageMetadata{MimeType: "image/jpeg"}, nil
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
	raw, _, err := s.Image(ctx, utils.MimeTypeJPEG, nil)
	if err != nil {
		return nil, resource.ResponseMetadata{}, err
	}


	named, err := camera.NamedImageFromBytes(raw, "screen", utils.MimeTypeJPEG, data.Annotations{})
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
	s.cancelFunc()
	c := s.child
	s.child = nil
	if c != nil {
		c.Close()
	}
	return nil
}

func (s *screenshotCamScreenshot) Geometries(context.Context, map[string]interface{}) ([]spatialmath.Geometry, error) {
	return nil, errUnimplemented
}
