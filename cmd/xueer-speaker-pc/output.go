//go:build windows

package main

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"sync"
	"time"

	"github.com/ebitengine/oto/v3"
	"github.com/hajimehoshi/go-mp3"
)

// output plays mp3 clips on the default Windows output device. The device
// is opened on the first clip and suspended between clips, so an idle PC is
// not holding the Bluetooth link open with silence.
type output struct {
	mu   sync.Mutex
	ctx  *oto.Context
	rate int
}

// leadIn: a Bluetooth link that was idle eats the first few hundred ms.
const leadIn = 300 * time.Millisecond

func (o *output) Play(ctx context.Context, clip []byte) error {
	o.mu.Lock()
	defer o.mu.Unlock()
	d, err := mp3.NewDecoder(bytes.NewReader(clip))
	if err != nil {
		return err
	}
	if o.ctx == nil {
		c, ready, err := oto.NewContext(&oto.NewContextOptions{SampleRate: d.SampleRate(), ChannelCount: 2, Format: oto.FormatSignedInt16LE})
		if err != nil {
			return fmt.Errorf("open output: %w", err)
		}
		<-ready
		o.ctx, o.rate = c, d.SampleRate()
	} else if err := o.ctx.Resume(); err != nil {
		return fmt.Errorf("resume output: %w", err)
	}
	defer o.ctx.Suspend()
	if d.SampleRate() != o.rate {
		return fmt.Errorf("clip is %d Hz, output opened at %d Hz", d.SampleRate(), o.rate)
	}
	silence := make([]byte, int(leadIn.Seconds()*float64(o.rate))*4)
	p := o.ctx.NewPlayer(io.MultiReader(bytes.NewReader(silence), d))
	defer p.Close()
	p.Play()
	for p.IsPlaying() {
		select {
		case <-ctx.Done():
			p.Pause()
			return ctx.Err()
		case <-time.After(50 * time.Millisecond):
		}
	}
	return o.ctx.Err()
}
