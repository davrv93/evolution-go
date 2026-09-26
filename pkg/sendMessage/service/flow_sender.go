package send_service

// FlowSender implementa flow_service.Sender con /send/text|button|list.
// Vive aquí (y no en pkg/flow) para no crear un ciclo: send_service ya
// importa a whatsmeow_service, y whatsmeow_service importa a pkg/flow.

import (
	instance_model "github.com/evolution-foundation/evolution-go/pkg/instance/model"
	flow_service "github.com/evolution-foundation/evolution-go/pkg/flow/service"
)

type FlowSender struct {
	svc SendService
}

func NewFlowSender(svc SendService) *FlowSender {
	return &FlowSender{svc: svc}
}

var _ flow_service.Sender = (*FlowSender)(nil)

func (f *FlowSender) Texto(inst *instance_model.Instance, numero, texto string) error {
	_, err := f.svc.SendText(&TextStruct{Number: numero, Text: texto}, inst)
	return err
}

func (f *FlowSender) Botones(inst *instance_model.Instance, numero, titulo, descripcion, pie string, botones []flow_service.BotonDTO) error {
	out := make([]Button, 0, len(botones))
	for _, b := range botones {
		out = append(out, Button{Type: "reply", DisplayText: b.Etiqueta, Id: b.ID})
	}
	_, err := f.svc.SendButton(&ButtonStruct{
		Number: numero, Title: titulo, Description: descripcion,
		Footer: pie, Buttons: out,
	}, inst)
	return err
}

func (f *FlowSender) Lista(inst *instance_model.Instance, numero, titulo, descripcion, textoBoton, pie string, secciones []flow_service.SeccionDTO) error {
	out := make([]Section, 0, len(secciones))
	for _, sec := range secciones {
		filas := make([]Row, 0, len(sec.Filas))
		for _, fl := range sec.Filas {
			filas = append(filas, Row{Title: fl.Titulo, Description: fl.Descripcion, RowId: fl.ID})
		}
		out = append(out, Section{Title: sec.Titulo, Rows: filas})
	}
	_, err := f.svc.SendList(&ListStruct{
		Number: numero, Title: titulo, Description: descripcion,
		ButtonText: textoBoton, FooterText: pie, Sections: out,
	}, inst)
	return err
}
