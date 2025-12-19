package models

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"image"
	"math/rand/v2"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"
	"unsafe"

	errw "github.com/pkg/errors"

	"github.com/kbinani/screenshot"
	"github.com/rodrigocfd/windigo/co"
	"github.com/rodrigocfd/windigo/win"
	"github.com/viam-labs/screenshot-cam/subproc"
	"go.viam.com/rdk/components/camera"
	"go.viam.com/rdk/data"
	"go.viam.com/rdk/gostream"
	"go.viam.com/rdk/logging"
	"go.viam.com/rdk/pointcloud"
	"go.viam.com/rdk/resource"
	"go.viam.com/rdk/spatialmath"
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

func Capture(logger logging.Logger, index int) (image.Image, error) {
	win.SetProcessDPIAware()
	for _, dev := range win.EnumDisplayDevices("", 0) {
		logger.Infof("dev %s %s", dev.DeviceName(), dev.DeviceString())
	}
	cx := win.GetSystemMetrics(co.SM_CXSCREEN)
	cy := win.GetSystemMetrics(co.SM_CYSCREEN)
	logger.Infof("dims %d %d", cx, cy)
	mon := win.GetDesktopWindow().MonitorFromWindow(0)
	info, _ := mon.GetMonitorInfo()
	logger.Infof("info %d %d %d %d", info.RcMonitor.Top, info.RcMonitor.Right, info.RcMonitor.Bottom, info.RcMonitor.Left)
	hdcScreen, err := win.HWND(0).GetDC()
	if err != nil {
		return nil, errw.Wrap(err, "GetDC")
	}
	defer win.HWND(0).ReleaseDC(hdcScreen)

	monitors, err := hdcScreen.EnumDisplayMonitors(nil)
	if err != nil {
		return nil, errw.Wrap(err, "EnumDisplayMonitors")
	}
	for i, mon := range monitors {
		logger.Infof("MON %d: %d %d %d %d", i, mon.Rc.Top, mon.Rc.Right, mon.Rc.Bottom, mon.Rc.Left)
	}

	bmp, err := hdcScreen.CreateCompatibleBitmap(int(cx), int(cy))
	if err != nil {
		return nil, errw.Wrap(err, "CreateCompatibleBitmap")
	}
	defer bmp.DeleteObject()
	hdcMem, err := hdcScreen.CreateCompatibleDC()
	defer hdcMem.DeleteDC()

	bmpOld, err := hdcMem.SelectObjectBmp(bmp)
	if err != nil {
		return nil, errw.Wrap(err, "SelectObjectBmp")
	}
	defer hdcMem.SelectObjectBmp(bmpOld)

	zero := win.POINT{X: 0, Y: 0}
	err = hdcMem.BitBlt(
		zero, win.SIZE{Cx: cx, Cy: cy},
		hdcScreen, zero,
		co.ROP_SRCCOPY,
	)
	if err != nil {
		return nil, errw.Wrap(err, "BitBlt")
	}
	bi := win.BITMAPINFO{BmiHeader: win.BITMAPINFOHEADER{
		Width: cx, Height: cy, Planes: 1, BitCount: 32, Compression: co.BI_RGB,
	}}
	bi.BmiHeader.SetSize()

	bmpObj, err := bmp.GetObject()
	if err != nil {
		return nil, errw.Wrap(err, "GetObject")
	}
	bmpSize := bmpObj.CalcBitmapSize(bi.BmiHeader.BitCount)
	rawMem, err := win.GlobalAlloc(co.GMEM_FIXED|co.GMEM_ZEROINIT, bmpSize)
	if err != nil {
		return nil, errw.Wrap(err, "GlobalAlloc")
	}
	defer rawMem.GlobalFree()

	bmpSlice, err := rawMem.GlobalLockSlice()
	if err != nil {
		return nil, errw.Wrap(err, "GlobalLockSlice")
	}
	defer rawMem.GlobalUnlock()

	_, err = hdcScreen.GetDIBits(bmp, 0, int(cy), bmpSlice, &bi, co.DIB_COLORS_RGB)
	if err != nil {
		return nil, errw.Wrap(err, "GetDIBits")
	}
	var bfh win.BITMAPFILEHEADER
	bfh.SetBfType()
	bfh.SetBfOffBits(uint32(unsafe.Sizeof(bfh) + unsafe.Sizeof(bi.BmiHeader)))
	bfh.SetBfSize(bfh.BfOffBits() + uint32(bmpSize))

	buf := bytes.NewBuffer(nil)
	_, err = buf.Write(bfh.Serialize())
	if err != nil {
		return nil, errw.Wrap(err, "Write")
	}
	_, err = buf.Write(bi.BmiHeader.Serialize())
	if err != nil {
		return nil, errw.Wrap(err, "Write")
	}
	_, err = buf.Write(bmpSlice)
	if err != nil {
		return nil, errw.Wrap(err, "Write")
	}
	img, _, err := image.Decode(buf)
	if err != nil {
		return nil, errw.Wrap(err, "Decode")
	}
	return img, nil
}

func (s *screenshotCamScreenshot) Image(ctx context.Context, mimeType string, extra map[string]interface{}) ([]byte, camera.ImageMetadata, error) {
	if !subproc.ShouldSpawn() {
		runtime.LockOSThread()
		defer runtime.UnlockOSThread()
		_, err := Capture(s.logger, s.cfg.DisplayIndex)
		// img, err := screenshot.CaptureDisplay(s.cfg.DisplayIndex)
		if err != nil {
			return nil, camera.ImageMetadata{}, err
		}
		panic("todo")
		// var buf bytes.Buffer
		// if err := jpeg.Encode(bufio.NewWriter(&buf), img, nil); err != nil {
		// 	return nil, camera.ImageMetadata{}, err
		// }
		// return buf.Bytes(), camera.ImageMetadata{MimeType: "image/jpeg"}, nil
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

func (s *screenshotCamScreenshot) Images(ctx context.Context, filterSourceNames []string, extra map[string]interface{}) ([]camera.NamedImage, resource.ResponseMetadata, error) {
	mimetype := "image/jpg"
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
