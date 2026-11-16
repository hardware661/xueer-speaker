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
