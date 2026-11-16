//go:build windows

package main

import (
	"math"
	"runtime"

	"github.com/go-ole/go-ole"
	"github.com/moutend/go-wca/pkg/wca"
)

// The Windows master volume of the default output (the same slider as the
// taskbar's), via Core Audio. Each call opens COM on a locked thread: the
// calls are rare (a panel drag, a 2 s poll) and this keeps no COM state.
func withEndpoint(f func(*wca.IAudioEndpointVolume) error) error {
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	if err := ole.CoInitializeEx(0, ole.COINIT_APARTMENTTHREADED); err != nil {
		if oe, ok := err.(*ole.OleError); !ok || oe.Code() != 1 { // S_FALSE: already initialised
			return err
		}
	}
	defer ole.CoUninitialize()
	var de *wca.IMMDeviceEnumerator
	if err := wca.CoCreateInstance(wca.CLSID_MMDeviceEnumerator, 0, wca.CLSCTX_ALL, wca.IID_IMMDeviceEnumerator, &de); err != nil {
		return err
	}
	defer de.Release()
	var dev *wca.IMMDevice
	if err := de.GetDefaultAudioEndpoint(wca.ERender, wca.EConsole, &dev); err != nil {
		return err
	}
	defer dev.Release()
	var ev *wca.IAudioEndpointVolume
	if err := dev.Activate(wca.IID_IAudioEndpointVolume, wca.CLSCTX_ALL, nil, &ev); err != nil {
		return err
	}
	defer ev.Release()
	return f(ev)
}

func getVolume() (int, error) {
	var v float32
	err := withEndpoint(func(ev *wca.IAudioEndpointVolume) error { return ev.GetMasterVolumeLevelScalar(&v) })
	return int(math.Round(float64(v) * 100)), err
}

func setVolume(v int) error {
	v = max(0, min(100, v))
	return withEndpoint(func(ev *wca.IAudioEndpointVolume) error {
		return ev.SetMasterVolumeLevelScalar(float32(v)/100, nil)
	})
}
