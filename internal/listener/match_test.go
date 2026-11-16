package listener

import "testing"

// 2026-11-14 on the real speaker: "雪儿放歌" was written "小爱放歌" and
// "让 Husic 放歌" became "让放歌" (Xiao Ai drops words it does not know, then
// plays its own songs). "播放我的歌单" was heard exactly, and since the owner
// bound no music account Xiao Ai only says "这里还没有歌曲哦…" — so it is the
// trigger (owner picked A). Next / stop are the native words, taken over only
// while our session plays (the caller checks that).
func TestMatchIntent(t *testing.T) {
	cases := []struct {
		query string
		kind  Intent
		arg   string
	}{
		{"播放我的歌单", IntentPlayLiked, ""},
		{"放我的歌单", IntentPlayLiked, ""},
		{"播放我的歌单。", IntentPlayLiked, ""},
		{"播放我的早上歌单", IntentPlayList, "早上"},
		{"播放我的运动的歌单", IntentPlayList, "运动"},
		{"放我的早上听歌单", IntentPlayList, "早上听"},
		{"下一首", IntentNext, ""},
		{"换一首", IntentNext, ""},
		{"切歌", IntentNext, ""},
		{"暂停", IntentHeardPause, ""},
		{"继续播放", IntentHeardResume, ""},
		{"停止播放", IntentHeardStop, ""},
		{"别放了", IntentHeardStop, ""},
		// the owner said "播放一下我的歌单" and nothing happened.
		{"播放一下我的歌单", IntentPlayLiked, ""},
		{"放一下我的歌单", IntentPlayLiked, ""},
		{"来点我的歌单", IntentPlayLiked, ""},
		{"放点我喜欢的歌", IntentPlayLiked, ""},
		{"播放我喜欢的歌", IntentPlayLiked, ""},
		{"播放我喜欢的", IntentPlayLiked, ""},
		{"播放我收藏的歌", IntentPlayLiked, ""},
		{"播放一下我的开车歌单", IntentPlayList, "开车"},
		{"来点我的开车歌单", IntentPlayList, "开车"},
		{"播放我喜欢的音乐", IntentPlayLiked, ""}, // no music account is bound to Xiao Ai
		{"现在几点", IntentNone, ""},
		{"放首歌", IntentNone, ""}, // Xiao Ai's own job
		{"让放歌", IntentNone, ""},
		{"播放周杰伦的歌", IntentNone, ""},
		{"我的歌单里有什么", IntentNone, ""},
	}
	for _, c := range cases {
		got := Match(c.query)
		if got.Kind != c.kind || got.Arg != c.arg {
			t.Errorf("Match(%q) = %+v, want %v %q", c.query, got, c.kind, c.arg)
		}
	}
}
