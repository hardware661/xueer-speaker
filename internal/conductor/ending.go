package conductor

import "github.com/hardware661/xueer-speaker/internal/xiaomi"

const (
	tailMS    = 15_000 // only the last 15 s of a song can end it
	wrapFromM = 10_000 // position had passed 10 s …
	wrapToMS  = 3_000  // … and fell back under 3 s: the speaker looped the url
)

// songEnded judges a natural ending from two consecutive status reads.
// A stop far from the end is not an ending: that is the owner (or Xiao Ai)
// stopping it, which the caller tells apart using the conversation log.
func songEnded(prev, cur xiaomi.Status) bool {
	if prev.PositionMS > wrapFromM && cur.PositionMS < wrapToMS && cur.PositionMS < prev.PositionMS {
		return prev.DurationMS == 0 || prev.DurationMS-prev.PositionMS <= tailMS
	}
	inTail := prev.DurationMS > 0 && prev.DurationMS-prev.PositionMS <= tailMS
	stopped := cur.State != xiaomi.StatePlaying && cur.State != xiaomi.StatePaused && cur.PositionMS <= prev.PositionMS
	return inTail && stopped
}
