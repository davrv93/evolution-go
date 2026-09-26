package send_service

// Cada encuesta que sale queda en poll_messages (pkg/poll): así el voto,
// que llega como hashes, se puede traducir al texto de la opción en el
// webhook y en GET /polls/:id/results.

import (
	"context"
	"time"

	instance_model "github.com/evolution-foundation/evolution-go/pkg/instance/model"
	poll_service "github.com/evolution-foundation/evolution-go/pkg/poll/service"
)

func (s *sendService) registrarEncuesta(instance *instance_model.Instance, message *MessageSendStruct, data *PollStruct) {
	if s.whatsmeowService == nil || message == nil || data == nil {
		return
	}
	polls := s.whatsmeowService.GetPollService()
	if polls == nil {
		return
	}
	ctx, cancelar := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancelar()
	err := polls.RegistrarEncuesta(ctx, poll_service.Encuesta{
		MessageID: message.Info.ID, InstanceID: instance.Id, ChatJid: message.Info.Chat.String(),
		Pregunta: data.Question, Opciones: data.Options,
	})
	if err != nil {
		// No rompe el envío: la encuesta ya salió; el voto llegará con hashes.
		s.loggerWrapper.GetLogger(instance.Id).LogWarn("[%s] encuesta %s sin registrar: %v", instance.Id, message.Info.ID, err)
	}
}
