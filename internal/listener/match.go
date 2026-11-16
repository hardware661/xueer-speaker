package listener

import (
	"regexp"
	"strings"
	"unicode"
)

// Intent is what a heard sentence asks for.
type Intent int

const (
	IntentNone        Intent = iota
	IntentPlayLiked          // 播放我的歌单
	IntentPlayList           // 播放我的 XX 歌单
	IntentNext               // 下一首 (only ours while our session plays)
	IntentHeardPause         // 暂停
	IntentHeardResume        // 继续
	IntentHeardStop          // 停止 / 别放了
)

// Heard is the parsed sentence.
type Heard struct {
	Kind Intent
	Arg  string // playlist name for IntentPlayList
}

// The trigger is "播放我的歌单": Xiao Ai hears it exactly and does not play
// anything for it by itself (a wake-word-like "雪儿…" or "Husic…" gets
// misheard). A few natural ways around it are accepted too ("播放一下我的
// 歌单", "来点我喜欢的歌").
var (
	playWord   = `(?:播放|放|来)(?:一下|一点|点)?`
	likedRe    = regexp.MustCompile(`^` + playWord + `我(?:的歌单|(?:喜欢|收藏)的(?:歌|音乐)?)$`)
	playlistRe = regexp.MustCompile(`^` + playWord + `我的(.+?)(?:的)?歌单$`)
)

// normalize drops punctuation and spaces.
func normalize(s string) string {
	return strings.Map(func(r rune) rune {
		if unicode.IsPunct(r) || unicode.IsSpace(r) || unicode.IsSymbol(r) {
			return -1
		}
		return r
	}, s)
}

// Match parses what the owner said to Xiao Ai.
func Match(query string) Heard {
	q := normalize(query)
	switch q {
	case "暂停", "暂停播放", "暂停一下":
		return Heard{Kind: IntentHeardPause}
	case "继续", "继续播放", "播放":
		return Heard{Kind: IntentHeardResume}
	case "停止", "停止播放", "关闭", "别放了", "不放了", "停":
		return Heard{Kind: IntentHeardStop}
	case "下一首", "换一首", "切歌", "下首", "播放下一首":
		return Heard{Kind: IntentNext}
	}
	if likedRe.MatchString(q) {
		return Heard{Kind: IntentPlayLiked}
	}
	if m := playlistRe.FindStringSubmatch(q); m != nil {
		return Heard{Kind: IntentPlayList, Arg: m[1]}
	}
	return Heard{}
}
