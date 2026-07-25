//go:build windows

package tui

import (
	"unsafe"

	"golang.org/x/sys/windows"
)

const enumCurrentSettings = ^uint32(0)

const displayDevicePrimary = 0x4

var (
	user32               = windows.NewLazySystemDLL("user32.dll")
	enumDisplayDevicesW  = user32.NewProc("EnumDisplayDevicesW")
	enumDisplaySettingsW = user32.NewProc("EnumDisplaySettingsW")
)

type windowsDisplayDevice struct {
	Size         uint32
	DeviceName   [32]uint16
	DeviceString [128]uint16
	StateFlags   uint32
	DeviceID     [128]uint16
	DeviceKey    [128]uint16
}

type windowsDevMode struct {
	DeviceName         [32]uint16
	SpecVersion        uint16
	DriverVersion      uint16
	Size               uint16
	DriverExtra        uint16
	Fields             uint32
	PositionX          int32
	PositionY          int32
	DisplayOrientation uint32
	DisplayFixedOutput uint32
	Color              int16
	Duplex             int16
	YResolution        int16
	TTOption           int16
	Collate            int16
	FormName           [32]uint16
	LogPixels          uint16
	BitsPerPel         uint32
	PelsWidth          uint32
	PelsHeight         uint32
	DisplayFlags       uint32
	DisplayFrequency   uint32
	ICMMethod          uint32
	ICMIntent          uint32
	MediaType          uint32
	DitherType         uint32
	Reserved1          uint32
	Reserved2          uint32
	PanningWidth       uint32
	PanningHeight      uint32
}

func probeDisplayRefreshHz() (uint32, bool) {
	for index := uintptr(0); index < 32; index++ {
		device := windowsDisplayDevice{Size: uint32(unsafe.Sizeof(windowsDisplayDevice{}))}
		ok, _, _ := enumDisplayDevicesW.Call(0, index, uintptr(unsafe.Pointer(&device)), 0)
		if ok == 0 {
			break
		}
		if device.StateFlags&displayDevicePrimary == 0 {
			continue
		}
		mode := windowsDevMode{Size: uint16(unsafe.Sizeof(windowsDevMode{}))}
		ok, _, _ = enumDisplaySettingsW.Call(uintptr(unsafe.Pointer(&device.DeviceName[0])), uintptr(enumCurrentSettings), uintptr(unsafe.Pointer(&mode)))
		return mode.DisplayFrequency, ok != 0 && mode.DisplayFrequency >= 2
	}
	return 0, false
}
