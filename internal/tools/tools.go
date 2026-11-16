// Package tools defines the speaker's MCP tools once, for the service (HTTP
// MCP) and the serverless CLI (stdio MCP): speaker_say for a line, and
// speaker for everything else (status, volume, do-not-disturb, do).
package tools

import (
	"context"
	"errors"
	"fmt"

	"github.com/google/jsonschema-go/jsonschema"
	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/hardware661/xueer-speaker/internal/control"
)

// Backend is what the tools act on.
type Backend interface {
	// Say queues a line; source is who asked (from the MCP key).
	Say(ctx context.Context, source, text string) (id string, err error)
	// Status is a JSON-able snapshot: online, login, volume, dnd, playing…
	Status(ctx context.Context) any
	Volume(ctx context.Context) (int, error)
	SetVolume(ctx context.Context, v int) error
	// DND switches do-not-disturb; until is "08:00", "2h" or "".
	DND(ctx context.Context, on bool, until string) (DNDState, error)
	Do(ctx context.Context, text string, silent bool) error
}

// DNDState is do-not-disturb as the tools report it.
type DNDState struct {
	On    bool   `json:"dnd"`
	Until string `json:"until,omitempty"` // local time it ends, "08:00"
	Held  int    `json:"held,omitempty"`  // lines kept quiet (reported when switched off)
}

// Persona shapes the texts the AI reads.
type Persona struct{ Name, Owner string }

func (p Persona) or() Persona {
	if p.Name == "" {
		p.Name = "雪儿"
	}
	if p.Owner == "" {
		p.Owner = "主人"
	}
	return p
}

// SayDescription is speaker_say's description.
func (p Persona) SayDescription() string {
	p = p.or()
	return fmt.Sprintf("用%s的声音跟%s说一句（电脑开着从电脑出声，没开就在音箱上念；勿扰时只记下不出声）。每次回复%s之前调一次。"+
		"保持%s的人设：称呼%s、自称%s，可以带「呐」「嘛」。开头说是哪个项目，再说结果和能不能用；"+
		"坏消息直接说；一句只说一件事，一口气说得完；不念提交号、路径、英文名。"+
		"不要加「回来看看吧」这类固定结尾，只有要%s定事时才说回来做什么。"+
		"80 字以内，不要表情。出错就算了，不重试也不提。被别的 agent 派出来的子 agent 不调。",
		p.Name, p.Owner, p.Owner, p.Name, p.Owner, p.Name, p.Owner)
}

// SpeakerDescription is the speaker tool's description.
func (p Persona) SpeakerDescription() string {
	p = p.or()
	return fmt.Sprintf("操作家里的小爱音箱。action 选一个：\n"+
		"- status：音箱在不在线、小米登录、音量、勿扰（到几点）、在放什么。\n"+
		"- volume：不带 volume 就读当前音量；带 volume（0～100）就设。夜里念短句前可以先调小，太小听不见就调大。\n"+
		"- dnd：勿扰。on=true 打开，until 写%s说的起床时间（如 08:00，或 2h），不写最多 12 小时自动关（写了几点就到几点）；勿扰时 speaker_say 只记下不出声。"+
		"%s交代完任务说要去睡觉就打开；%s回来说话时，先 on=false 关掉再汇报。\n"+
		"- do：让小爱执行一句话，就像%s对它说的，比如「明天早上八点叫我」「十分钟后提醒我关火」「把客厅灯关了」；silent=true 时小爱做完不出声。"+
		"只用于闹钟、提醒、查信息和家里的设备，不准买东西、付款、打电话、发消息（会被拒绝）。",
		p.Owner, p.Owner, p.Owner, p.Owner)
}

// Instructions is the MCP server note every AI client reads.
func (p Persona) Instructions() string {
	p = p.or()
	return "家里的小爱音箱，" + p.Name + "的嘴。每次回复" + p.Owner + "之前调一次 speaker_say；" +
		p.Name + "的口吻、说结果，别加「回来看看吧」尾巴；出错就算了，不重试。音量、勿扰、让小爱做事用 speaker 工具。"
}

type sayIn struct {
	Text string `json:"text"`
}

type sayOut struct {
	ID string `json:"id"`
}

// SpeakerIn is the speaker tool's input.
type SpeakerIn struct {
	Action string `json:"action" jsonschema:"status / volume / dnd / do"`
	Volume *int   `json:"volume,omitempty" jsonschema:"volume 时：要设的音量 0～100；不填就只读"`
	On     *bool  `json:"on,omitempty" jsonschema:"dnd 时：true 打开、false 关掉"`
	Until  string `json:"until,omitempty" jsonschema:"dnd 打开时：到几点自动关，如 08:00 或 2h；不填最多 12 小时"`
	Text   string `json:"text,omitempty" jsonschema:"do 时：让小爱执行的一句话"`
	Silent bool   `json:"silent,omitempty" jsonschema:"do 时：小爱做完不出声"`
}

// Register adds speaker_say and speaker to srv. source maps a call to who
// made it (the service reads it from the MCP key).
func Register(srv *mcp.Server, b Backend, p Persona, source func(*mcp.CallToolRequest) string) {
	inSchema, err := jsonschema.For[sayIn](nil)
	if err != nil {
		panic(err)
	}
	inSchema.Properties["text"].Description = p.or().Name + "口吻的一句话，80 字以内，开头说是哪个项目"
	mcp.AddTool(srv, &mcp.Tool{Name: "speaker_say", Description: p.SayDescription(), InputSchema: inSchema},
		func(ctx context.Context, req *mcp.CallToolRequest, in sayIn) (*mcp.CallToolResult, sayOut, error) {
			id, err := b.Say(ctx, source(req), in.Text)
			if err != nil {
				return nil, sayOut{}, err // the SDK turns it into an isError result
			}
			return nil, sayOut{ID: id}, nil
		})
	spSchema, err := jsonschema.For[SpeakerIn](nil)
	if err != nil {
		panic(err) // a bad struct tag; caught by the tests
	}
	spSchema.Properties["action"].Enum = []any{"status", "volume", "dnd", "do"}
	spSchema.Required = []string{"action"}
	mcp.AddTool(srv, &mcp.Tool{Name: "speaker", Description: p.SpeakerDescription(), InputSchema: spSchema},
		func(ctx context.Context, _ *mcp.CallToolRequest, in SpeakerIn) (*mcp.CallToolResult, any, error) {
			out, err := Run(ctx, b, in)
			return nil, out, err
		})
}

// Run carries out one speaker call; the CLI uses it too.
func Run(ctx context.Context, b Backend, in SpeakerIn) (any, error) {
	switch in.Action {
	case "status":
		return b.Status(ctx), nil
	case "volume":
		if in.Volume != nil {
			if *in.Volume < 0 || *in.Volume > 100 {
				return nil, errors.New("音量要在 0～100")
			}
			if err := b.SetVolume(ctx, *in.Volume); err != nil {
				return nil, err
			}
			return map[string]int{"volume": *in.Volume}, nil
		}
		v, err := b.Volume(ctx)
		if err != nil {
			return nil, err
		}
		return map[string]int{"volume": v}, nil
	case "dnd":
		if in.On == nil {
			return nil, errors.New("dnd 要带 on：true 打开、false 关掉")
		}
		return b.DND(ctx, *in.On, in.Until)
	case "do":
		if err := control.CheckDirective(in.Text); err != nil {
			return nil, err
		}
		if err := b.Do(ctx, in.Text, in.Silent); err != nil {
			return nil, err
		}
		return map[string]bool{"ok": true}, nil
	}
	return nil, fmt.Errorf("action 只能是 status / volume / dnd / do，不认识 %q", in.Action)
}
