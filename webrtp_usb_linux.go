//go:build linux

package webrtp

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"unsafe"

	"golang.org/x/sys/unix"
)

const (
	v4l2BufTypeVideoCapture = 1

	v4l2PixFmtH264 = 0x34363248
	v4l2PixFmtHEVC = 0x43564548

	v4l2CidMpegVideoGopSize       = 0x009909cb
	v4l2CidMpegVideoH264IDRPeriod = 0x00990a6a
	v4l2CidMpegVideoForceKeyFrame = 0x009909e5

	vidiocGCtrl = 0xc008561b
	vidiocSCtrl = 0xc008561c
	vidiocGFmt  = 0xc0d05604
	vidiocSFmt  = 0xc0d05605
)

type usbConn struct {
	file   *os.File
	cancel context.CancelFunc
}

type v4l2Format struct {
	Type uint32
	Pix  v4l2PixFormat
	Raw  [152]byte
}

type v4l2PixFormat struct {
	Width        uint32
	Height       uint32
	Pixelformat  uint32
	Field        uint32
	Bytesperline uint32
	Sizeimage    uint32
	Colorspace   uint32
	Priv         uint32
	Flags        uint32
	YcbcrEnc     uint32
	Quantization uint32
	XferFunc     uint32
}

type v4l2Control struct {
	Id    uint32
	Value int32
}

func (r *usbConn) Close() {
	if r.cancel != nil {
		r.cancel()
	}
	if r.file != nil {
		_ = r.file.Close()
	}
}

func (r *usbConn) ForceNextKeyFrame() error {
	if r.file == nil {
		return fmt.Errorf("usb capture is not active")
	}
	ctrl := &v4l2Control{
		Id:    v4l2CidMpegVideoForceKeyFrame,
		Value: 1,
	}
	if err := usbIoctl(r.file.Fd(), vidiocSCtrl, unsafe.Pointer(ctrl)); err != nil {
		return fmt.Errorf("V4L2 force keyframe: %w", err)
	}
	return nil
}

func (r *Instance) connectUsb(ctx context.Context) (*usbConn, error) {
	device := strings.TrimSpace(r.cfg.Device)
	if device == "" {
		return nil, fmt.Errorf("usb source requires device")
	}
	codec := strings.ToLower(strings.TrimSpace(r.cfg.Codec))
	if codec != "h264" && codec != "h265" {
		return nil, fmt.Errorf("usb source requires codec to be h264 or h265")
	}

	file, err := os.OpenFile(device, os.O_RDWR, 0)
	if err != nil {
		return nil, fmt.Errorf("open usb device: %w", err)
	}

	if err := r.usbSetFormat(file.Fd(), codec); err != nil {
		_ = file.Close()
		return nil, err
	}

	fps := r.cfg.FrameRate
	if fps <= 0 {
		fps = 30
	}
	frameDur := uint32(90000 / fps)
	if frameDur == 0 {
		frameDur = 3000
	}

	usbCtx, cancel := context.WithCancel(ctx)
	conn := &usbConn{file: file, cancel: cancel}
	handler := &videoHandler{hub: r.hub, logger: r.logger, instance: r}

	r.logger.Printf("USB stream active (%s, codec=%s)", device, strings.ToUpper(codec))

	go func() {
		defer cancel()
		defer file.Close()

		ts := uint32(0)
		buf := make([]byte, 2*1024*1024)
		for {
			select {
			case <-usbCtx.Done():
				return
			default:
			}

			n, err := file.Read(buf)
			if err != nil {
				if errors.Is(err, os.ErrClosed) || errors.Is(err, context.Canceled) {
					return
				}
				if errors.Is(err, io.EOF) {
					r.logger.Printf("usb device returned EOF")
				} else {
					r.logger.Printf("usb read failed: %v", err)
				}
				return
			}
			if n == 0 {
				continue
			}

			au := AnnexbToNalus(buf[:n])
			if len(au) == 0 {
				continue
			}

			switch codec {
			case "h264":
				handler.processH264(au, ts, nil, nil)
			case "h265":
				handler.processH265(au, ts, nil, nil, nil)
			}
			ts += frameDur
		}
	}()

	return conn, nil
}

func (r *Instance) usbSetFormat(fd uintptr, codec string) error {
	format := &v4l2Format{Type: v4l2BufTypeVideoCapture}
	if err := usbIoctl(fd, vidiocGFmt, unsafe.Pointer(format)); err != nil {
		return fmt.Errorf("VIDIOC_G_FMT: %w", err)
	}

	switch codec {
	case "h264":
		format.Pix.Pixelformat = v4l2PixFmtH264
	case "h265":
		format.Pix.Pixelformat = v4l2PixFmtHEVC
	}

	if err := usbIoctl(fd, vidiocSFmt, unsafe.Pointer(format)); err != nil {
		return fmt.Errorf("VIDIOC_S_FMT: %w", err)
	}

	r.logger.Printf("USB format ready (%dx%d, codec=%s)", format.Pix.Width, format.Pix.Height, strings.ToUpper(codec))

	if gopSize, err := r.usbGetControl(fd, v4l2CidMpegVideoGopSize); err == nil {
		r.logger.Printf("USB GOP size: %d", gopSize)
	}
	if idrPeriod, err := r.usbGetControl(fd, v4l2CidMpegVideoH264IDRPeriod); err == nil {
		r.logger.Printf("USB IDR period: %d", idrPeriod)
	}

	return nil
}

func (r *Instance) usbGetControl(fd uintptr, id uint32) (int32, error) {
	ctrl := &v4l2Control{Id: id}
	if err := usbIoctl(fd, vidiocGCtrl, unsafe.Pointer(ctrl)); err != nil {
		return 0, err
	}
	return ctrl.Value, nil
}

func usbIoctl(fd uintptr, req uint, arg unsafe.Pointer) error {
	_, _, errno := unix.Syscall(unix.SYS_IOCTL, fd, uintptr(req), uintptr(arg))
	if errno != 0 {
		return errno
	}
	return nil
}
