package main

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/hardware661/xueer-speaker/internal/xiaomi"
)

// watchBT samples the Bluetooth link and the music player on one session
// (no re-login per sample) and prints each change with its time. Real-device
// diagnostics: who takes the sound back while a clip plays.
func watchBT(ctx context.Context, sp *xiaomi.Speaker, d time.Duration) error {
	t0 := time.Now()
	last := ""
	read := func(path, method, key string) string {
		info, err := sp.Raw(ctx, path, method, json.RawMessage("{}"))
		if err != nil {
			return "err"
		}
		var m map[string]any
		if json.Unmarshal([]byte(info), &m) != nil {
			return "?"
		}
		return fmt.Sprint(m[key])
	}
	for time.Since(t0) < d {
		cur := "bt=" + read("mibt", "status", "playing") + " player=" + read("mediaplayer", "player_get_play_status", "status")
		if cur != last {
			fmt.Printf("%6.2fs %s %s\n", time.Since(t0).Seconds(), time.Now().Format("15:04:05.000"), cur)
			last = cur
		}
	}
	return nil
}
