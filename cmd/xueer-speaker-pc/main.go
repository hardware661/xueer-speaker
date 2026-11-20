//go:build windows

// xueer-speaker-pc is the speaker's mouth on a Windows PC: it keeps the
// server's listen stream open, so while the PC is on every line plays here
// (out of whatever the PC plays through, e.g. the speaker over Bluetooth),
// and sets the PC's volume. While the owner dictates with xueer-ear on this
// PC, lines wait until the round ends (ear.go). The tray app (pc/, XueerSpeaker.exe) runs it
// with -engine and talks to it over stdio: it reads "pause" / "resume" lines
// and writes one JSON line per state change. Run by hand it works the same,
// headless.
//
// Files live in %LOCALAPPDATA%\xueer-speaker: server.txt (the server's
// address), pc.key (the PC key), pc.log.
package main

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"

	"golang.org/x/sys/windows"

	"github.com/hardware661/xueer-speaker/internal/pcagent"
)

var version = "dev"

func main() {
	dir := filepath.Join(os.Getenv("LOCALAPPDATA"), "xueer-speaker")
	if len(os.Args) > 1 && os.Args[1] == "-once" { // install check: play the file given and exit
		os.Exit(once(os.Args[2]))
	}
	if len(os.Args) > 1 && os.Args[1] == "-volume" { // install check: print the system volume
		v, err := getVolume()
		fmt.Println(v, err)
		return
	}
	// One mouth per PC: a second copy would keep kicking the first off the
	// server (the newest connection wins). The tray app starts this as its
	// engine, so a stray copy started by hand simply exits.
	name, _ := windows.UTF16PtrFromString(`Local\xueer-speaker-pc`)
	if _, err := windows.CreateMutex(nil, false, name); err == windows.ERROR_ALREADY_EXISTS {
		return
	}
	logger := log.New(openLog(filepath.Join(dir, "pc.log")), "", log.LstdFlags)
	key, err := os.ReadFile(filepath.Join(dir, "pc.key"))
	if err != nil {
		logger.Printf("no key: %v", err)
		os.Exit(1)
	}
	server, err := os.ReadFile(filepath.Join(dir, "server.txt"))
	if err != nil {
		logger.Printf("no server address: %v", err)
		os.Exit(1)
	}
	base := strings.TrimRight(strings.TrimSpace(string(server)), "/")
	logger.Printf("xueer-speaker-pc %s starting", version)
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	out := &output{}
	tr := http.DefaultTransport.(*http.Transport).Clone()
	tr.Proxy = systemProxy
	a := &pcagent.Agent{Base: base, Key: strings.TrimSpace(string(key)), Play: out.Play, GetVolume: getVolume, SetVolume: setVolume, Log: logger, Client: &http.Client{Transport: tr},
		Dictating: earDictating}
	if len(os.Args) > 1 && os.Args[1] == "-engine" {
		var cancel context.CancelFunc
		ctx, cancel = context.WithCancel(ctx)
		engine(a, cancel)
	}
	a.Run(ctx)
	a.Drain(30 * time.Second) // finish the sentence being said
	logger.Printf("xueer-speaker-pc quit")
}

// engine wires the agent to the tray app's pipes; the tray closing stdin
// (it quit or died) ends the program.
func engine(a *pcagent.Agent, quit context.CancelFunc) {
	var mu sync.Mutex
	say := func(connected bool) {
		b, _ := json.Marshal(map[string]bool{"connected": connected, "paused": a.Paused()})
		mu.Lock()
		os.Stdout.Write(append(b, '\n'))
		mu.Unlock()
	}
	a.OnState = say
	say(false)
	go func() {
		sc := bufio.NewScanner(os.Stdin)
		for sc.Scan() {
			switch strings.TrimSpace(sc.Text()) {
			case "pause":
				a.SetPaused(true)
			case "resume":
				a.SetPaused(false)
			}
		}
		quit()
	}()
}

func once(path string) int {
	b, err := os.ReadFile(path)
	if err == nil {
		err = (&output{}).Play(context.Background(), b)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	fmt.Println("played ok")
	return 0
}

// openLog keeps one old log next to the current one (1 MB each).
func openLog(path string) io.Writer {
	os.MkdirAll(filepath.Dir(path), 0o700)
	if st, err := os.Stat(path); err == nil && st.Size() > 1<<20 {
		os.Rename(path, path+".old")
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return io.Discard
	}
	return f
}
