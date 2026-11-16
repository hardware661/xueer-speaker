package xiaomi

import (
	"context"
	"encoding/json"
	"testing"
)

func newSpeaker(t *testing.T) (*fakeCloud, *Speaker) {
	t.Helper()
	f := newFakeCloud(t)
	s := NewSession(f.endpoints(), Credentials{UserID: "2225000001", PassToken: "V1:good", DeviceID: "an_fixed"})
	sp, err := s.Speaker(context.Background(), "Xiaomi 智能音箱 Pro")
	if err != nil {
		t.Fatalf("Speaker: %v", err)
	}
	return f, sp
}

func TestSpeakerFoundByNameDidOrMAC(t *testing.T) {
	f := newFakeCloud(t)
	s := NewSession(f.endpoints(), Credentials{UserID: "2225000001", PassToken: "V1:good", DeviceID: "an_fixed"})
	for _, key := range []string{"Xiaomi 智能音箱 Pro", "123456", "AA:BB", "mina-dev-1"} {
		if _, err := s.Speaker(context.Background(), key); err != nil {
			t.Errorf("Speaker(%q): %v", key, err)
		}
	}
	if _, err := s.Speaker(context.Background(), "厨房"); err == nil {
		t.Error("unknown speaker should fail")
	}
}

func TestPlayMusicSendsDoubleEncodedPayload(t *testing.T) {
	f, sp := newSpeaker(t)
	if err := sp.PlayMusic(context.Background(), "https://speaker.example/a/abc.mp3"); err != nil {
		t.Fatal(err)
	}
	form := f.ubus[len(f.ubus)-1]
	if form.Get("path") != "mediaplayer" || form.Get("method") != "player_play_music" || form.Get("deviceId") != "mina-dev-1" {
		t.Fatalf("form = %v", form)
	}
	var msg struct {
		StartAudioID string `json:"startaudioid"`
		Music        string `json:"music"` // a JSON string inside JSON
	}
	if err := json.Unmarshal([]byte(form.Get("message")), &msg); err != nil {
		t.Fatalf("message: %v", err)
	}
	var music struct {
		Payload struct {
			AudioItems []struct {
				ItemID struct {
					AudioID string `json:"audio_id"`
				} `json:"item_id"`
				Stream struct {
					URL string `json:"url"`
				} `json:"stream"`
			} `json:"audio_items"`
		} `json:"payload"`
		PlayBehavior string `json:"play_behavior"`
	}
	if err := json.Unmarshal([]byte(msg.Music), &music); err != nil {
		t.Fatalf("music is not a JSON string: %v", err)
	}
	items := music.Payload.AudioItems
	if len(items) != 1 || items[0].Stream.URL != "https://speaker.example/a/abc.mp3" ||
		music.PlayBehavior != "REPLACE_ALL" || items[0].ItemID.AudioID != msg.StartAudioID {
		t.Fatalf("music = %+v start=%s", music, msg.StartAudioID)
	}
}

func TestStatusAndVolume(t *testing.T) {
	f, sp := newSpeaker(t)
	st, err := sp.Status(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if st.State != StatePlaying || st.Volume != 30 {
		t.Fatalf("status = %+v", st)
	}
	if err := sp.SetVolume(context.Background(), 2); err != nil {
		t.Fatal(err)
	}
	var v struct{ Volume int }
	json.Unmarshal([]byte(f.ubus[len(f.ubus)-1].Get("message")), &v)
	if v.Volume != 6 {
		t.Fatalf("volume sent = %d, want clamped to 6", v.Volume)
	}
}

func TestDeviceRejectionIsAnError(t *testing.T) {
	f, sp := newSpeaker(t)
	f.ubusReply = `{"code":0,"data":{"code":-1,"info":"device busy"}}`
	if err := sp.Control(context.Background(), OpPause); err == nil {
		t.Fatal("inner code -1 must be an error")
	}
}

// mibt status as the OH2P answered on 2026-11-15 (names and addresses cut).
func TestBluetoothState(t *testing.T) {
	f, sp := newSpeaker(t)
	f.ubusReply = `{"code":0,"data":{"code":0,"info":"{ \"role\": 2, \"playing\": 1, \"connect\": 1, \"devices_list\": [ { \"connect\": \"true\" } ] }"}}`
	bt, err := sp.Bluetooth(context.Background())
	if err != nil || !bt.Connected || !bt.Playing {
		t.Fatalf("%+v %v", bt, err)
	}
	f.ubusReply = `{"code":0,"data":{"code":0,"info":"{ \"playing\": 0, \"connect\": 0 }"}}`
	if bt, _ := sp.Bluetooth(context.Background()); bt.Connected || bt.Playing {
		t.Fatalf("%+v", bt)
	}
	// 11-15 15:00, PC's Bluetooth switched off: the top-level connect stays 1
	// but no paired device is connected.
	f.ubusReply = `{"code":0,"data":{"code":0,"info":"{ \"playing\": 0, \"connect\": 1, \"devices_list\": [ { \"name\": \"phone\", \"connect\": \"false\" }, { \"name\": \"pc\", \"connect\": \"false\" } ] }"}}`
	if bt, _ := sp.Bluetooth(context.Background()); bt.Connected {
		t.Fatalf("no device connected but %+v", bt)
	}
}
