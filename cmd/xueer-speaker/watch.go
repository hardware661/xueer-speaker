package main

import (
	"context"
	"flag"
	"fmt"
	"time"

	"github.com/hardware661/xueer-speaker/internal/listener"
	"github.com/hardware661/xueer-speaker/internal/xiaomi"
)

// watchInterrupt is a step-0 probe: poll the log every second, and as soon
// as the owner's trigger shows up, run pause→stop twice and report timings.
func watchInterrupt(ctx context.Context, sp *xiaomi.Speaker, args []string) error {
	fs := flag.NewFlagSet("watch-interrupt", flag.ContinueOnError)
	dur := fs.Duration("for", 120*time.Second, "最长盯多久")
	if err := fs.Parse(args); err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), *dur)
	defer cancel()
	hit := make(chan xiaomi.Record, 1)
	l := listener.New(sp, func(r xiaomi.Record) {
		fmt.Printf("%s 听到：%s\n", time.Now().Format("15:04:05.000"), r.Query)
		if listener.Match(r.Query).Kind != listener.IntentNone {
			select {
			case hit <- r:
			default:
			}
		}
	})
	fmt.Println("开始盯对话记录，请对音箱说话…")
	for {
		wait := l.Poll(ctx)
		select {
		case r := <-hit:
			seen := time.Now()
			for i := range 2 {
				sp.Control(ctx, xiaomi.OpPause)
				sp.Control(ctx, xiaomi.OpStop)
				if i == 0 {
					time.Sleep(600 * time.Millisecond)
				}
			}
			fmt.Printf("说话→记录出现 %.1fs，记录出现→打断完成 %.1fs\n",
				seen.Sub(time.UnixMilli(r.TimeMS)).Seconds(), time.Since(seen).Seconds())
			return nil
		case <-ctx.Done():
			return fmt.Errorf("没等到口令")
		case <-time.After(max(wait, time.Second)):
		}
	}
}
