package server

import "strings"

// Persona is who speaks on the speaker and how the owner is called.
type Persona struct {
	Name  string // 雪儿
	Owner string // 主人
}

// DefaultPersona is the project's own character.
var DefaultPersona = Persona{Name: "雪儿", Owner: "主人"}

func (p Persona) orDefault() Persona {
	if strings.TrimSpace(p.Name) == "" {
		p.Name = DefaultPersona.Name
	}
	if strings.TrimSpace(p.Owner) == "" {
		p.Owner = DefaultPersona.Owner
	}
	return p
}

// describe is speaker_say's description: what the AI should say and how.
func (p Persona) describe() string {
	p = p.orDefault()
	r := strings.NewReplacer("{name}", p.Name, "{owner}", p.Owner)
	return r.Replace("用{name}的声音跟{owner}说一句（电脑开着从电脑出声，没开就在音箱上念）。每次回复{owner}之前调一次。" +
		"保持{name}的人设：称呼{owner}、自称{name}，可以带「呐」「嘛」。开头说是哪个项目，再说结果和能不能用；" +
		"坏消息直接说；一句只说一件事，一口气说得完；不念提交号、路径、英文名。" +
		"不要加「回来看看吧」这类固定结尾，只有要{owner}定事时才说回来做什么。" +
		"80 字以内，不要表情。出错就算了，不重试也不提。被别的 agent 派出来的子 agent 不调。")
}

// instructions is the MCP server note every AI client reads.
func (p Persona) instructions() string {
	p = p.orDefault()
	return "家里的小爱音箱，" + p.Name + "的嘴。每次回复" + p.Owner + "之前调一次 speaker_say；" +
		p.Name + "的口吻、说结果，别加「回来看看吧」尾巴；出错就算了，不重试。"
}

// textHint is the text argument's description.
func (p Persona) textHint() string {
	return p.orDefault().Name + "口吻的一句话，80 字以内，开头说是哪个项目"
}

func (s *Server) sayDescription() string { return s.cfg.Persona.describe() }

// SayDescription and Instructions are the default persona's texts, for the
// local stdio bridge (mcp-local), which shows them before it reaches the
// server.
var (
	SayDescription = DefaultPersona.describe()
	Instructions   = DefaultPersona.instructions()
)
