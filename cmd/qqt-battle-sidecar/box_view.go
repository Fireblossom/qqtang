package main

type boxObjectView struct {
	ID        uint32 `json:"id"`
	Element   uint32 `json:"element"`
	Row       int16  `json:"row"`
	Col       int16  `json:"col"`
	X         int32  `json:"x"`
	Y         int32  `json:"y"`
	Moving    bool   `json:"moving"`
	DestroyAt uint32 `json:"destroy_at"`
	Born      uint32 `json:"born"`
	Hidden    bool   `json:"hidden"`
}
type boxEffectView struct {
	Row   int16  `json:"row"`
	Col   int16  `json:"col"`
	Style uint32 `json:"style"`
	Start uint32 `json:"start"`
	Until uint32 `json:"until"`
}
type boxLauncherView struct {
	Row     int16  `json:"row"`
	Col     int16  `json:"col"`
	Element uint32 `json:"element"`
	Fired   uint32 `json:"fired"`
}
type boxView struct {
	Objects   []boxObjectView   `json:"objects"`
	Effects   []boxEffectView   `json:"effects"`
	Launchers []boxLauncherView `json:"launchers"`
	NPC       *boxNPCView       `json:"npc,omitempty"`
}

func (match *onlineMatch) boxView() *boxView {
	v := &boxView{NPC: match.boxNPCView()}
	for _, b := range match.box.ordered() {
		v.Objects = append(v.Objects, boxObjectView{ID: b.ID, Element: b.Element, Row: b.Cell.Row, Col: b.Cell.Col, X: b.Position.X, Y: b.Position.Y, Moving: b.Moving, DestroyAt: b.DestroyAt, Born: b.BornAt, Hidden: b.Hidden})
	}
	for _, e := range match.box.effects {
		v.Effects = append(v.Effects, boxEffectView{e.Cell.Row, e.Cell.Col, e.Style, e.Start, e.Until})
	}
	for i, at := range boxCorners {
		v.Launchers = append(v.Launchers, boxLauncherView{at.Row, at.Col, boxCornerElements[i], match.box.fired[i]})
	}
	return v
}
