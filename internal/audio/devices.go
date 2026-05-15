package audio

import (
	"fmt"
	"sort"
	"strings"

	"github.com/gen2brain/malgo"
)

// InputDevice is a microphone the user can select via config.input_device.
// UID is the stable string identifier passed to NewRecorder; Name is the
// human-readable label shown by `flowstate devices`.
type InputDevice struct {
	UID  string
	Name string
}

// ListInputDevices returns the available input devices as (UID, Name)
// pairs. Devices with empty UIDs or names are skipped, the slice is
// de-duplicated by UID, and the result is sorted by Name (case-insensitive)
// to match FreeFlow's `AudioDevice.availableInputDevices`.
//
// Each call spins up a temporary malgo context — enumeration is rare
// enough (only on `flowstate devices` and at startup) that holding a
// shared context isn't worth the lifecycle complexity.
func ListInputDevices() ([]InputDevice, error) {
	ctx, err := malgo.InitContext(nil, malgo.ContextConfig{}, nil)
	if err != nil {
		return nil, fmt.Errorf("audio: init malgo context: %w", err)
	}
	defer func() {
		_ = ctx.Uninit()
		ctx.Free()
	}()

	infos, err := ctx.Devices(malgo.Capture)
	if err != nil {
		return nil, fmt.Errorf("audio: enumerate capture devices: %w", err)
	}

	seen := make(map[string]struct{}, len(infos))
	out := make([]InputDevice, 0, len(infos))
	for _, info := range infos {
		uid := strings.TrimSpace(info.ID.String())
		name := strings.TrimSpace(info.Name())
		if uid == "" || name == "" {
			continue
		}
		if _, ok := seen[uid]; ok {
			continue
		}
		seen[uid] = struct{}{}
		out = append(out, InputDevice{UID: uid, Name: name})
	}

	sort.Slice(out, func(i, j int) bool {
		return strings.ToLower(out[i].Name) < strings.ToLower(out[j].Name)
	})

	return out, nil
}

// findCaptureDeviceID looks up a malgo capture device by its UID string
// (the hex form of the underlying DeviceID bytes). The returned DeviceID
// is suitable for storing in DeviceConfig.Capture.DeviceID via its
// address. The found bool distinguishes "no such device" from real errors.
func findCaptureDeviceID(ctx *malgo.AllocatedContext, uid string) (malgo.DeviceID, bool, error) {
	infos, err := ctx.Devices(malgo.Capture)
	if err != nil {
		return malgo.DeviceID{}, false, err
	}
	for _, info := range infos {
		if info.ID.String() == uid {
			return info.ID, true, nil
		}
	}
	return malgo.DeviceID{}, false, nil
}
